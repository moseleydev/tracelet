package detectors

import (
	"regexp"
	"strconv"
	"strings"
)

// Helpers for reading PostgreSQL plan text. Filters come from EXPLAIN
// VERBOSE, so columns are qualified with their table alias, e.g.
// "((orders.customer_id = $1) AND (orders.status = 'paid'::text))".

// cond is one comparison on a column found in a filter.
type cond struct {
	column string
	op     string // "=", "<", ">", "<=", ">=", "<>", "IS NULL", "LIKE"
	fn     string // function wrapping the column, e.g. "lower", if any
}

const ident = `"?[A-Za-z_][A-Za-z0-9_$]*"?`

var (
	// [qualifier.]column op  — the column is on the left of the operator.
	reColumnCond = regexp.MustCompile(`(?:^|[(\s])(?:` + ident + `\.)?(` + ident + `)\s+(=|<>|!=|<=|>=|<|>|IS NULL|~~\*?|!~~\*?)\s*`)
	// func([qualifier.]column) op
	reFuncCond = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\((?:` + ident + `\.)?(` + ident + `)\)\s+(=|<=|>=|<|>|~~\*?)\s`)
	// column ~~ '%...  (a LIKE whose pattern starts with a wildcard)
	reLeadingWildcard = regexp.MustCompile(`(?:` + ident + `\.)?(` + ident + `)\s+~~\*?\s+\(*'%`)
)

// conditions lists the column comparisons in a filter.
func conditions(filter string) []cond {
	var out []cond
	funcCols := map[string]bool{}
	for _, m := range reFuncCond.FindAllStringSubmatch(filter, -1) {
		name := strings.ToLower(m[1])
		if name == "any" || name == "all" {
			continue
		}
		col := unquoteIdent(m[2])
		funcCols[col] = true
		out = append(out, cond{column: col, op: normOp(m[3]), fn: name})
	}
	for _, m := range reColumnCond.FindAllStringSubmatch(filter, -1) {
		col := unquoteIdent(m[1])
		if funcCols[col] || isPlanKeyword(col) {
			continue
		}
		out = append(out, cond{column: col, op: normOp(m[2])})
	}
	return out
}

// leadingWildcardColumn returns the column searched with LIKE '%...', if any.
func leadingWildcardColumn(filter string) string {
	if m := reLeadingWildcard.FindStringSubmatch(filter); m != nil {
		return unquoteIdent(m[1])
	}
	return ""
}

func normOp(op string) string {
	switch {
	case op == "!=":
		return "<>"
	case strings.HasPrefix(op, "~~"), strings.HasPrefix(op, "!~~"):
		return "LIKE"
	}
	return op
}

func isPlanKeyword(s string) bool {
	switch strings.ToUpper(s) {
	case "AND", "OR", "NOT", "ANY", "ALL", "SUBPLAN", "NULL", "TRUE", "FALSE":
		return true
	}
	return false
}

// sortColumns turns VERBOSE sort keys such as "orders.created_at DESC" into
// index column definitions ("created_at DESC"). It gives up on expressions.
func sortColumns(keys []string) []string {
	var out []string
	for _, k := range keys {
		parts := strings.Fields(k)
		if len(parts) == 0 {
			return nil
		}
		col := parts[0]
		if i := strings.LastIndexByte(col, '.'); i >= 0 {
			col = col[i+1:]
		}
		if strings.ContainsAny(col, "()") {
			return nil
		}
		def := unquoteIdent(col)
		if len(parts) > 1 && strings.EqualFold(parts[1], "DESC") {
			def += " DESC"
		}
		out = append(out, def)
	}
	return out
}

func unquoteIdent(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// indexName builds a conventional, valid index name: table_col1_col2_idx.
func indexName(table string, cols []string, suffix string) string {
	if i := strings.LastIndexByte(table, '.'); i >= 0 {
		table = table[i+1:]
	}
	parts := []string{table}
	for _, c := range cols {
		c = strings.TrimSuffix(c, " DESC")
		c = strings.NewReplacer("(", "_", ")", "", " ", "_").Replace(c)
		parts = append(parts, c)
	}
	name := strings.Join(parts, "_") + "_" + suffix
	if len(name) > 63 { // PostgreSQL's identifier limit
		name = name[:63]
	}
	return name
}

// hasIndexStartingWith reports whether any index's first column is one of cols.
func hasIndexStartingWith(indexes []Index, cols map[string]bool) bool {
	for _, ix := range indexes {
		if len(ix.Columns) > 0 && cols[unquoteIdent(ix.Columns[0])] {
			return true
		}
	}
	return false
}

// sortAbove returns the sort keys of a Sort node directly above a scan
// (possibly through Limit, Gather or Incremental Sort), or nil.
func sortAbove(scan *Plan, parents map[*Plan]*Plan) []string {
	for p := parents[scan]; p != nil; p = parents[p] {
		switch p.Node {
		case "Sort", "Incremental Sort":
			return p.SortKey
		case "Gather", "Gather Merge", "Limit":
			continue
		default:
			return nil
		}
	}
	return nil
}

// fmtRows renders an estimated row count for people: "~300,000".
func fmtRows(n float64) string {
	digits := strconv.FormatInt(int64(n+0.5), 10)
	var b strings.Builder
	b.WriteByte('~')
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(d)
	}
	return b.String()
}
