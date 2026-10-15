// Package analyzer collects query plans from the database and runs the
// detectors over them, turning stats into recommendations.
package analyzer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/moseleydev/tracelet/detectors"
)

// Explainer asks PostgreSQL how it would run a query, without running it.
//
// Safety: it only ever issues plain EXPLAIN (never EXPLAIN ANALYZE), so the
// query is planned but not executed, even for UPDATE or DELETE. Each
// explain runs in a READ ONLY transaction with short statement and lock
// timeouts, and is rolled back.
type Explainer struct {
	conn *pgx.Conn
}

// Connect opens the explainer's own connection to the database. It connects
// directly, not through the Tracelet proxy, so its queries never show up in
// the stats.
func Connect(ctx context.Context, url string) (*Explainer, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.RuntimeParams["application_name"] = "tracelet-analyzer"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Explainer{conn: conn}, nil
}

// Close closes the connection.
func (x *Explainer) Close(ctx context.Context) error { return x.conn.Close(ctx) }

// Explainable reports whether a query template is worth explaining: a
// single SELECT, UPDATE or DELETE, or an INSERT that reads other tables.
func Explainable(template string) bool {
	word := strings.ToUpper(firstWord(template))
	switch word {
	case "SELECT", "WITH", "UPDATE", "DELETE", "TABLE":
		return true
	case "INSERT":
		return strings.Contains(strings.ToUpper(template), "SELECT")
	}
	return false
}

func firstWord(s string) string {
	s = strings.TrimLeft(s, "( \t\n")
	if i := strings.IndexAny(s, " \t\n("); i >= 0 {
		return s[:i]
	}
	return s
}

var reParam = regexp.MustCompile(`\$(\d+)`)

// Explain returns the generic plan for a query template with $n parameters,
// annotated with table sizes and existing indexes.
func (x *Explainer) Explain(ctx context.Context, template string) (*detectors.Plan, error) {
	if !Explainable(template) {
		return nil, errors.New("not an explainable statement")
	}
	params := 0
	for _, m := range reParam.FindAllStringSubmatch(template, -1) {
		if n, _ := strconv.Atoi(m[1]); n > params {
			params = n
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	simple := pgx.QueryExecModeSimpleProtocol

	tx, err := x.conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	prepared := false
	defer func() {
		tx.Rollback(context.Background())
		// Prepared statements outlive transactions, so drop ours. Only ours:
		// pgx caches its own prepared statements on this connection.
		if prepared {
			x.conn.Exec(context.Background(), "DEALLOCATE tracelet_explain", simple)
		}
	}()

	for _, set := range []string{
		"SET LOCAL statement_timeout = '2s'",
		"SET LOCAL lock_timeout = '100ms'",
		// Plan for any parameter values, not the NULLs passed below.
		"SET LOCAL plan_cache_mode = force_generic_plan",
	} {
		if _, err := tx.Exec(ctx, set, simple); err != nil {
			return nil, fmt.Errorf("%s: %w (PostgreSQL 12 or newer is required)", set, err)
		}
	}
	if _, err := tx.Exec(ctx, "PREPARE tracelet_explain AS "+template, simple); err != nil {
		return nil, err
	}
	prepared = true
	args := strings.TrimSuffix(strings.Repeat("NULL, ", params), ", ")
	explain := "EXPLAIN (FORMAT JSON, VERBOSE) EXECUTE tracelet_explain"
	if params > 0 {
		explain += "(" + args + ")"
	}
	var raw []byte
	if err := tx.QueryRow(ctx, explain, simple).Scan(&raw); err != nil {
		return nil, err
	}
	plan, err := parsePlan(raw)
	if err != nil {
		return nil, err
	}
	if err := x.annotate(ctx, tx, plan); err != nil {
		return nil, fmt.Errorf("reading table details: %w", err)
	}
	return plan, nil
}

// annotate adds table sizes and existing indexes to every node that reads a
// table.
func (x *Explainer) annotate(ctx context.Context, tx pgx.Tx, plan *detectors.Plan) error {
	type tableInfo struct {
		rows    float64
		indexes []detectors.Index
	}
	cache := map[string]*tableInfo{}
	var err error
	plan.Walk(func(n, _ *detectors.Plan) {
		if err != nil || n.Relation == "" {
			return
		}
		key := n.Schema + "." + n.Relation
		info := cache[key]
		if info == nil {
			info = &tableInfo{}
			cache[key] = info
			if err = tx.QueryRow(ctx,
				`SELECT c.reltuples::float8 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
				 WHERE n.nspname = $1 AND c.relname = $2`, n.Schema, n.Relation).Scan(&info.rows); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					err = nil
				}
				return
			}
			var rows pgx.Rows
			rows, err = tx.Query(ctx,
				`SELECT i.relname, ix.indisunique,
				        ARRAY(SELECT pg_get_indexdef(ix.indexrelid, k, true) FROM generate_series(1, ix.indnkeyatts) k ORDER BY k)
				 FROM pg_index ix
				 JOIN pg_class i ON i.oid = ix.indexrelid
				 JOIN pg_class t ON t.oid = ix.indrelid
				 JOIN pg_namespace ns ON ns.oid = t.relnamespace
				 WHERE ns.nspname = $1 AND t.relname = $2 AND ix.indisvalid
				 ORDER BY i.relname`, n.Schema, n.Relation)
			if err != nil {
				return
			}
			for rows.Next() {
				var ix detectors.Index
				if err = rows.Scan(&ix.Name, &ix.Unique, &ix.Columns); err != nil {
					rows.Close()
					return
				}
				info.indexes = append(info.indexes, ix)
			}
			rows.Close()
			if err = rows.Err(); err != nil {
				return
			}
		}
		if info.rows >= 0 { // -1 means the table was never analyzed
			n.TableRows = info.rows
		}
		n.Indexes = info.indexes
	})
	return err
}

// pgNode is PostgreSQL's JSON plan format (the fields Tracelet uses).
type pgNode struct {
	NodeType  string   `json:"Node Type"`
	Relation  string   `json:"Relation Name"`
	Schema    string   `json:"Schema"`
	Index     string   `json:"Index Name"`
	Filter    string   `json:"Filter"`
	IndexCond string   `json:"Index Cond"`
	SortKey   []string `json:"Sort Key"`
	PlanRows  float64  `json:"Plan Rows"`
	TotalCost float64  `json:"Total Cost"`
	Plans     []pgNode `json:"Plans"`
}

func parsePlan(raw []byte) (*detectors.Plan, error) {
	var out []struct {
		Plan pgNode `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("reading EXPLAIN output: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("EXPLAIN returned no plan")
	}
	return convert(out[0].Plan), nil
}

func convert(n pgNode) *detectors.Plan {
	p := &detectors.Plan{
		Node:      n.NodeType,
		Relation:  n.Relation,
		Schema:    n.Schema,
		Index:     n.Index,
		Filter:    n.Filter,
		IndexCond: n.IndexCond,
		SortKey:   n.SortKey,
		EstRows:   n.PlanRows,
		TotalCost: n.TotalCost,
	}
	for _, c := range n.Plans {
		p.Children = append(p.Children, convert(c))
	}
	return p
}
