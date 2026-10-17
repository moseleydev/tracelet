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

// ident matches a plain or "quoted" identifier as EXPLAIN prints it.
const ident = `(?:"(?:[^"]|"")+"|[A-Za-z_][A-Za-z0-9_$]*)`

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

// sortColumns turns VERBOSE sort keys such as `orders.created_at DESC` or
// `o."Created At"` into index column definitions ("created_at DESC",
// "\"Created At\""). It gives up (returns nil) on expressions.
func sortColumns(keys []string) []string {
	var out []string
	for _, k := range keys {
		m := reSortKey.FindStringSubmatch(strings.TrimSpace(k))
		if m == nil {
			return nil
		}
		def := quoteIdent(unquoteIdent(m[1]))
		if strings.EqualFold(m[2], "DESC") {
			def += " DESC"
		}
		out = append(out, def)
	}
	return out
}

var reSortKey = regexp.MustCompile(`^(?:` + ident + `\.)?(` + ident + `)(?:\s+(ASC|DESC))?(?:\s+NULLS\s+(?:FIRST|LAST))?$`)

func unquoteIdent(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}

var plainIdent = regexp.MustCompile(`^[a-z_][a-z0-9_$]*$`)

// quoteIdent makes a name safe to put in generated SQL, quoting it when it
// has capitals, spaces or other special characters, or is a reserved word.
func quoteIdent(name string) string {
	if plainIdent.MatchString(name) && !reserved[strings.ToUpper(name)] {
		return name
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// qualifiedName is a scan node's table, quoted for SQL.
func qualifiedName(p *Plan) string {
	if p.Schema != "" {
		return quoteIdent(p.Schema) + "." + quoteIdent(p.Relation)
	}
	return quoteIdent(p.Relation)
}

// PostgreSQL's reserved key words, which must be quoted as identifiers.
var reserved = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`ALL ANALYSE ANALYZE AND ANY ARRAY AS ASC ASYMMETRIC BOTH CASE CAST
		CHECK COLLATE COLUMN CONSTRAINT CREATE CURRENT_CATALOG CURRENT_DATE CURRENT_ROLE CURRENT_TIME
		CURRENT_TIMESTAMP CURRENT_USER DEFAULT DEFERRABLE DESC DISTINCT DO ELSE END EXCEPT FALSE FETCH
		FOR FOREIGN FROM GRANT GROUP HAVING IN INITIALLY INTERSECT INTO LATERAL LEADING LIMIT LOCALTIME
		LOCALTIMESTAMP NOT NULL OFFSET ON ONLY OR ORDER PLACING PRIMARY REFERENCES RETURNING SELECT
		SESSION_USER SOME SYMMETRIC TABLE THEN TO TRAILING TRUE UNION UNIQUE USER USING VARIADIC WHEN
		WHERE WINDOW WITH`) {
		reserved[w] = true
	}
}

// indexName builds a conventional, valid index name: table_col1_col2_idx.
func indexName(table string, cols []string, suffix string) string {
	if i := strings.LastIndexByte(table, '.'); i >= 0 {
		table = table[i+1:]
	}
	parts := []string{table}
	for _, c := range cols {
		parts = append(parts, strings.TrimSuffix(c, " DESC"))
	}
	// Lowercase letters, digits and underscores only, so the name never
	// needs quoting.
	var b strings.Builder
	for _, r := range strings.ToLower(strings.Join(parts, "_") + "_" + suffix) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "_"):
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" || name[0] >= '0' && name[0] <= '9' {
		name = "idx_" + name
	}
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
