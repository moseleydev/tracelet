package detectors

import (
	"fmt"
	"time"
)

// fullScan flags queries that read every row of a large table in a way a
// plain index can't fix: wildcard searches, functions on columns, and
// queries with no filter at all. (Filters a normal index can serve are the
// missing-index detector's job.)
type fullScan struct{}

func init() { Register(fullScan{}) }

func (fullScan) Name() string { return "full-scan" }

const minRowsForScan = 10_000

func (fullScan) Detect(in Input) []Finding {
	if in.Plan == nil || in.Stats.Count < minCallsForIndex {
		return nil
	}
	parents := map[*Plan]*Plan{}
	var scan *Plan
	in.Plan.Walk(func(n, parent *Plan) {
		parents[n] = parent
		if n.Node == "Seq Scan" && n.TableRows >= minRowsForScan && (scan == nil || n.TotalCost > scan.TotalCost) {
			scan = n
		}
	})
	if scan == nil {
		return nil
	}

	impact := in.ShareOfTime >= 0.05 || in.Stats.P95 >= 50*time.Millisecond
	sev := Info
	if impact {
		sev = Warning
	}
	if in.ShareOfTime >= 0.25 || in.Stats.P95 >= 250*time.Millisecond {
		sev = Critical
	}
	table := qualifiedName(scan)
	rows := fmtRows(scan.TableRows)

	if col := leadingWildcardColumn(scan.Filter); col != "" {
		idx := &IndexSuggestion{Table: table, Columns: []string{quoteIdent(col) + " gin_trgm_ops"}, Method: "gin", Extension: "pg_trgm"}
		return []Finding{{
			Detector: "full-scan",
			Severity: max(sev, Warning),
			Title:    fmt.Sprintf("Wildcard search scans all of %s", scan.Relation),
			Explanation: fmt.Sprintf("A LIKE pattern that starts with %% can't use a regular index, so every search reads all %s rows of %s. "+
				"A trigram index can answer these searches directly.", rows, scan.Relation),
			Suggestion: "Enable pg_trgm and add a trigram index on the searched column. For word-based search, PostgreSQL full-text search is another option.",
			Fix:        "CREATE EXTENSION IF NOT EXISTS pg_trgm;\n" + idx.SQL(indexName(table, []string{col}, "trgm_idx"), true) + ";",
			Index:      idx,
		}}
	}

	for _, c := range conditions(scan.Filter) {
		if c.fn == "" || c.op == "LIKE" {
			continue
		}
		expr := fmt.Sprintf("%s(%s)", c.fn, c.column)
		idx := &IndexSuggestion{Table: table, Columns: []string{"(" + c.fn + "(" + quoteIdent(c.column) + "))"}}
		return []Finding{{
			Detector: "full-scan",
			Severity: max(sev, Warning),
			Title:    fmt.Sprintf("%s on %s prevents index use", expr, scan.Relation),
			Explanation: fmt.Sprintf("The filter wraps %s in %s(), so even an index on %s can't be used and every call reads all %s rows. "+
				"An index on the expression itself can.", c.column, c.fn, c.column, rows),
			Suggestion: "Create an expression index that matches the filter exactly.",
			Fix:        idx.SQL(indexName(table, []string{c.fn, c.column}, "idx"), true) + ";",
			Index:      idx,
		}}
	}

	if scan.Filter == "" && impact {
		what := "returns"
		if p := parents[scan]; p != nil && (p.Node == "Aggregate" || p.Node == "HashAggregate" || p.Node == "GroupAggregate") {
			what = "aggregates"
		}
		return []Finding{{
			Detector: "full-scan",
			Severity: sev,
			Title:    fmt.Sprintf("Reads every row of %s on each call", scan.Relation),
			Explanation: fmt.Sprintf("This query has no filter on %s, so it %s all %s rows every time it runs, and gets slower as the table grows.",
				scan.Relation, what, rows),
			Suggestion: "Narrow it with a WHERE clause or pagination (LIMIT with a keyset). If you need totals over the whole table, " +
				"cache the result, or keep it in a summary table or materialized view that you refresh periodically.",
		}}
	}
	return nil
}
