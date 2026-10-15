package detectors

import (
	"fmt"
	"strings"
	"time"
)

// missingIndex flags a sequential scan that filters a large table on columns
// no index covers, and writes the CREATE INDEX that fixes it.
type missingIndex struct{}

func init() { Register(missingIndex{}) }

func (missingIndex) Name() string { return "missing-index" }

const (
	minCallsForIndex = 5    // a one-off query is not worth an index
	minRowsForIndex  = 1000 // below this, a scan is cheap anyway
)

func (missingIndex) Detect(in Input) []Finding {
	if in.Plan == nil || in.Stats.Count < minCallsForIndex {
		return nil
	}
	parents := map[*Plan]*Plan{}
	var best *Plan
	in.Plan.Walk(func(n, parent *Plan) {
		parents[n] = parent
		if n.Node == "Seq Scan" && n.Filter != "" && n.TableRows >= minRowsForIndex &&
			!strings.Contains(n.Filter, " OR ") && (best == nil || n.TotalCost > best.TotalCost) {
			best = n
		}
	})
	if best == nil {
		return nil
	}

	var eq, rng []string
	seen := map[string]bool{}
	for _, c := range conditions(best.Filter) {
		if c.fn != "" || seen[c.column] {
			continue
		}
		switch c.op {
		case "=", "IS NULL":
			eq = append(eq, c.column)
		case "<", ">", "<=", ">=":
			rng = append(rng, c.column)
		default:
			continue
		}
		seen[c.column] = true
	}
	if len(eq)+len(rng) == 0 {
		return nil
	}
	// An index already leads with one of these columns: Postgres chose not
	// to use it (the filter matches most rows), so a new index won't help.
	if hasIndexStartingWith(best.Indexes, seen) {
		return nil
	}

	// Equality columns first, then at most one range column. With no range
	// column, the ORDER BY columns can follow so the index also returns rows
	// in order and the Sort disappears.
	cols := append([]string(nil), eq...)
	sorted := false
	if len(rng) > 0 {
		cols = append(cols, rng[0])
	} else if keys := sortColumns(sortAbove(best, parents)); len(keys) > 0 {
		for _, k := range keys {
			if !seen[strings.TrimSuffix(k, " DESC")] {
				cols = append(cols, k)
				sorted = true
			}
		}
	}

	table := best.Table()
	filterCols := append(eq, rng...)
	explanation := fmt.Sprintf(
		"Each call reads all %s rows of %s to find the few that match %s. "+
			"An index lets Postgres jump straight to them instead.",
		fmtRows(best.TableRows), best.Relation, strings.Join(filterCols, " and "))
	if sorted {
		explanation += " Adding the ORDER BY column also returns them already sorted, so the sort step goes away."
	}

	sev := Warning
	if in.Stats.P95 >= 100*time.Millisecond || in.ShareOfTime >= 0.25 || best.TableRows >= 1_000_000 {
		sev = Critical
	}
	return []Finding{{
		Detector:    "missing-index",
		Severity:    sev,
		Title:       fmt.Sprintf("Missing index on %s (%s)", best.Relation, strings.Join(cols, ", ")),
		Explanation: explanation,
		Suggestion:  "Create the index below. CONCURRENTLY builds it without blocking writes, but it can't run inside a transaction block.",
		Fix:         fmt.Sprintf("CREATE INDEX CONCURRENTLY %s ON %s (%s);", indexName(table, cols, "idx"), table, strings.Join(cols, ", ")),
	}}
}
