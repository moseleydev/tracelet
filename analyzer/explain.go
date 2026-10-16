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
// timeouts, and is rolled back. What-if simulations use HypoPG
// hypothetical indexes, which live only in the planner's memory for this
// connection: nothing is written and no index is ever built.
type Explainer struct {
	conn *pgx.Conn

	hypoStatus    string // "" when HypoPG is usable, else how to enable it
	hypoCheckedAt time.Time
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
	var plan *detectors.Plan
	err := x.session(ctx, func(s *session) error {
		var err error
		if plan, err = s.explain("tracelet_explain", template); err != nil {
			return err
		}
		if err := x.annotate(s.ctx, s.tx, plan); err != nil {
			return fmt.Errorf("reading table details: %w", err)
		}
		return nil
	})
	return plan, err
}

// Simulate estimates what the suggested index would do for a query, using a
// HypoPG hypothetical index: it exists only in this session's planner, is
// never built, and is removed before Simulate returns. Problems (such as
// HypoPG not being installed) are reported in Simulation.Unavailable.
func (x *Explainer) Simulate(ctx context.Context, template string, idx detectors.IndexSuggestion) detectors.Simulation {
	sim := detectors.Simulation{At: time.Now()}
	if !idx.Simulatable() {
		sim.Unavailable = notSimulatable(idx)
		return sim
	}
	if msg := x.hypopgStatus(ctx); msg != "" {
		sim.Unavailable = msg
		return sim
	}

	err := x.session(ctx, func(s *session) error {
		before, err := s.explain("tracelet_before", template)
		if err != nil {
			return err
		}
		name, err := s.hypothetical(idx.SQL("", false))
		if err != nil {
			return err
		}
		// A new statement, so it is planned with the hypothetical index.
		// (An already prepared one would keep its cached plan.)
		after, err := s.explain("tracelet_after", template)
		if err != nil {
			return err
		}
		sim.CostBefore, sim.CostAfter = before.TotalCost, after.TotalCost
		after.Walk(func(n, _ *detectors.Plan) {
			if n.Index == name {
				sim.UsesIndex = true
			}
		})
		if sim.UsesIndex && sim.CostAfter > 0 {
			sim.Speedup = sim.CostBefore / sim.CostAfter
		}
		return nil
	})
	if err != nil {
		sim.Unavailable = "simulation failed: " + err.Error()
	}
	return sim
}

func notSimulatable(idx detectors.IndexSuggestion) string {
	msg := "HypoPG can only simulate btree, hash and brin indexes"
	if idx.Extension != "" {
		msg += ", and this one needs the " + idx.Extension + " extension"
	}
	return msg + ", so there is no estimate for it."
}

// hypopgStatus returns "" when HypoPG is ready to use, or what to do to
// enable it. Tracelet never installs extensions itself.
func (x *Explainer) hypopgStatus(ctx context.Context) string {
	if time.Since(x.hypoCheckedAt) < 10*time.Minute {
		return x.hypoStatus
	}
	var installed, available bool
	err := x.conn.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'hypopg'),
		EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'hypopg')`).Scan(&installed, &available)
	switch {
	case err != nil:
		return "could not check for HypoPG: " + err.Error()
	case installed:
		x.hypoStatus = ""
	case available:
		x.hypoStatus = "Run CREATE EXTENSION hypopg; in this database to see estimated speedups before creating indexes."
	default:
		x.hypoStatus = "Install the HypoPG extension (for example the postgresql-16-hypopg package), then run CREATE EXTENSION hypopg; to see estimated speedups before creating indexes."
	}
	x.hypoCheckedAt = time.Now()
	return x.hypoStatus
}

// session is one read-only planning transaction. Everything it sets up
// (prepared statements, hypothetical indexes) is torn down when it ends.
type session struct {
	ctx      context.Context
	tx       pgx.Tx
	prepared []string
	hypo     bool
}

const simple = pgx.QueryExecModeSimpleProtocol

func (x *Explainer) session(ctx context.Context, fn func(*session) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tx, err := x.conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	s := &session{ctx: ctx, tx: tx}
	defer func() {
		bg := context.Background()
		tx.Rollback(bg)
		// Prepared statements and hypothetical indexes outlive the
		// transaction, so remove ours explicitly. Only ours: pgx caches its
		// own prepared statements on this connection.
		for _, name := range s.prepared {
			x.conn.Exec(bg, "DEALLOCATE "+name, simple)
		}
		if s.hypo {
			x.conn.Exec(bg, "SELECT hypopg_reset()", simple)
		}
	}()

	for _, set := range []string{
		"SET LOCAL statement_timeout = '2s'",
		"SET LOCAL lock_timeout = '100ms'",
		// Plan for any parameter values, not the NULLs passed below.
		"SET LOCAL plan_cache_mode = force_generic_plan",
	} {
		if _, err := tx.Exec(ctx, set, simple); err != nil {
			return fmt.Errorf("%s: %w (PostgreSQL 12 or newer is required)", set, err)
		}
	}
	return fn(s)
}

// explain prepares template under name and returns its generic plan.
func (s *session) explain(name, template string) (*detectors.Plan, error) {
	params := 0
	for _, m := range reParam.FindAllStringSubmatch(template, -1) {
		if n, _ := strconv.Atoi(m[1]); n > params {
			params = n
		}
	}
	if _, err := s.tx.Exec(s.ctx, "PREPARE "+name+" AS "+template, simple); err != nil {
		return nil, err
	}
	s.prepared = append(s.prepared, name)
	stmt := "EXPLAIN (FORMAT JSON, VERBOSE) EXECUTE " + name
	if params > 0 {
		stmt += "(" + strings.TrimSuffix(strings.Repeat("NULL, ", params), ", ") + ")"
	}
	var raw []byte
	if err := s.tx.QueryRow(s.ctx, stmt, simple).Scan(&raw); err != nil {
		return nil, err
	}
	return parsePlan(raw)
}

// hypothetical creates a HypoPG index and returns the name plans will use
// for it.
func (s *session) hypothetical(createIndex string) (string, error) {
	s.hypo = true
	var name string
	err := s.tx.QueryRow(s.ctx, "SELECT indexname FROM hypopg_create_index($1)", simple, createIndex).Scan(&name)
	return name, err
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
