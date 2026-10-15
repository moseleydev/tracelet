package detectors

import (
	"fmt"
	"regexp"
	"strings"
)

// nPlusOne flags a query the app runs once per row of a previous query:
// the same statement repeated back to back on one connection.
type nPlusOne struct{}

func init() { Register(nPlusOne{}) }

func (nPlusOne) Name() string { return "n-plus-one" }

const (
	minBurstRuns   = 3 // seen in at least this many separate loops
	minAvgBurstLen = 5 // with at least this many calls per loop on average
)

var reSingleEq = regexp.MustCompile(`= (\$\d+)`)

func (nPlusOne) Detect(in Input) []Finding {
	b := in.Bursts
	if b == nil || b.Runs < minBurstRuns {
		return nil
	}
	avg := b.Calls / b.Runs
	if avg < minAvgBurstLen {
		return nil
	}

	sev := Warning
	if avg >= 20 || in.ShareOfTime >= 0.2 {
		sev = Critical
	}

	after := "the query before it"
	if in.Parent != nil && in.Parent.Query != "" {
		q := in.Parent.Query
		if len(q) > 90 {
			q = q[:89] + "…"
		}
		after = "`" + q + "`"
	}

	f := Finding{
		Detector: "n-plus-one",
		Severity: sev,
		Title:    fmt.Sprintf("N+1 queries: runs ~%d times in a row", avg),
		Explanation: fmt.Sprintf("This query ran %d times back to back on the same connection, right after %s, in %d separate loops (up to %d in one). "+
			"That's an N+1 pattern: the app loads a list, then queries once per item. Every call is a round trip, so a page of 50 items costs 51 queries where 1 or 2 would do.",
			avg, after, b.Runs, b.MaxRun),
	}

	tmpl := in.Stats.Fingerprint.Template
	switch strings.ToUpper(firstWord(tmpl)) {
	case "INSERT":
		f.Suggestion = "Insert all the rows in one statement, with a multi-row VALUES list or COPY, instead of one INSERT per row."
	case "UPDATE", "DELETE":
		f.Suggestion = "Change all the rows in one statement, for example with WHERE id = ANY($1) and an array of ids."
	default:
		f.Suggestion = "Load all the rows at once: pass every id in a single query (WHERE id = ANY($1)), JOIN it into the first query, " +
			"or turn on your ORM's eager loading (includes, preload, select_related, joinedload, Include)."
		if m := reSingleEq.FindAllStringSubmatchIndex(tmpl, -1); len(m) == 1 {
			param := tmpl[m[0][2]:m[0][3]]
			f.Fix = tmpl[:m[0][0]] + "= ANY(" + param + ")" + tmpl[m[0][1]:] + "\n-- " + param + " is an array of all the ids"
		}
	}
	return []Finding{f}
}

func firstWord(s string) string {
	s = strings.TrimLeft(s, "( \t\n")
	if i := strings.IndexAny(s, " \t\n("); i >= 0 {
		return s[:i]
	}
	return s
}
