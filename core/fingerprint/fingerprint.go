// Package fingerprint groups queries by shape. It replaces literal values
// with placeholders so that
//
//	SELECT * FROM users WHERE id = 42
//	SELECT * FROM users WHERE id = 7
//
// both become "SELECT * FROM users WHERE id = ?" and share one hash. It also
// extracts the tables a query touches.
//
// It is a tokenizer, not a full parser: it never rejects input, and anything
// it does not recognize passes through unchanged. That keeps it fast, pure
// Go, and robust to dialect differences.
package fingerprint

import (
	"hash/fnv"
	"sort"
	"strconv"
	"strings"

	"github.com/moseleydev/tracelet/core"
)

// Of returns the fingerprint of a PostgreSQL query.
func Of(query string) core.Fingerprint {
	toks := tokenize(query)
	tmpl := template(toks) // before collapse, which reuses toks' storage
	toks = collapse(toks)
	for len(toks) > 0 && toks[len(toks)-1].text == ";" {
		toks = toks[:len(toks)-1] // "SELECT 1;" and "SELECT 1" are the same query
	}

	var text, key strings.Builder
	text.Grow(len(query))
	key.Grow(len(query))
	for i, t := range toks {
		if i > 0 && spaceBetween(toks[i-1], t) {
			text.WriteByte(' ')
			key.WriteByte(' ')
		}
		text.WriteString(t.text)
		if t.kind == tIdent {
			key.WriteString(strings.ToLower(t.text)) // keywords and unquoted names are case-insensitive
		} else {
			key.WriteString(t.text)
		}
	}

	display := text.String()
	if len(toks) == 0 {
		// Nothing but comments or whitespace, such as pgx's "-- ping" health
		// check. Show what was sent rather than a blank line.
		display = strings.Join(strings.Fields(query), " ")
		if len(display) > 80 {
			display = display[:80] + "…"
		}
		if display == "" {
			display = "(empty query)"
		}
		key.WriteString(display)
	}

	h := fnv.New64a()
	h.Write([]byte(key.String()))
	return core.Fingerprint{
		Hash:     strconv.FormatUint(h.Sum64(), 16),
		Query:    display,
		Tables:   tables(toks),
		Template: tmpl,
	}
}

type kind uint8

const (
	tIdent   kind = iota // keyword or unquoted identifier
	tQuoted              // "quoted identifier"
	tLiteral             // replaced literal or parameter: ?
	tGroup               // collapsed list: (...)
	tPunct               // ( ) , ; .
	tOp                  // operators: = < > + - * / :: etc.
)

type token struct {
	kind  kind
	text  string
	param string // the original "$1" for bind parameters; empty for literals
	// For string literals: whether they start or end with a LIKE wildcard,
	// so templates can keep the pattern's shape without its value.
	leadPct, trailPct bool
	onlyPct           bool // the literal is nothing but % signs, e.g. '%'
}

const placeholder = "?"

func tokenize(q string) []token {
	toks := make([]token, 0, len(q)/4+8) // roughly one token per 4-5 bytes of SQL
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			i++

		case c == '-' && at(q, i+1) == '-': // line comment
			for i < len(q) && q[i] != '\n' {
				i++
			}

		case c == '/' && at(q, i+1) == '*': // block comment, may nest
			depth := 0
			for i < len(q) {
				if q[i] == '/' && at(q, i+1) == '*' {
					depth++
					i += 2
				} else if q[i] == '*' && at(q, i+1) == '/' {
					depth--
					i += 2
					if depth == 0 {
						break
					}
				} else {
					i++
				}
			}

		case c == '\'':
			end := skipString(q, i, false)
			t := token{kind: tLiteral, text: placeholder}
			if body := strings.TrimSuffix(q[i+1:end], "'"); body != "" {
				t.leadPct, t.trailPct = body[0] == '%', body[len(body)-1] == '%'
				t.onlyPct = strings.Trim(body, "%") == ""
			}
			toks = append(toks, t)
			i = end

		case (c == 'E' || c == 'e') && at(q, i+1) == '\'':
			i = skipString(q, i+1, true)
			toks = append(toks, token{kind: tLiteral, text: placeholder})

		case (c == 'B' || c == 'b' || c == 'X' || c == 'x' || c == 'N' || c == 'n') && at(q, i+1) == '\'':
			i = skipString(q, i+1, false)
			toks = append(toks, token{kind: tLiteral, text: placeholder})

		case c == '"':
			j := i + 1
			for j < len(q) {
				if q[j] == '"' {
					if at(q, j+1) == '"' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			toks = append(toks, token{kind: tQuoted, text: q[i:j]})
			i = j

		case c == '$' && isDigit(at(q, i+1)): // $1 parameter
			start := i
			i++
			for i < len(q) && isDigit(q[i]) {
				i++
			}
			toks = append(toks, token{kind: tLiteral, text: placeholder, param: q[start:i]})

		case c == '$': // $tag$ ... $tag$ dollar-quoted string
			j := i + 1
			for j < len(q) && isIdentChar(q[j]) && q[j] != '$' {
				j++
			}
			if at(q, j) != '$' {
				toks = append(toks, token{kind: tOp, text: "$"})
				i++
				continue
			}
			tag := q[i : j+1]
			end := strings.Index(q[j+1:], tag)
			if end < 0 {
				i = len(q)
			} else {
				i = j + 1 + end + len(tag)
			}
			toks = append(toks, token{kind: tLiteral, text: placeholder})

		case isDigit(c) || (c == '.' && isDigit(at(q, i+1))):
			i = skipNumber(q, i)
			// Fold a unary minus into the literal: "x = -5" matches "x = 5".
			if n := len(toks); n > 0 && toks[n-1].text == "-" && (n == 1 || unaryContext(toks[n-2])) {
				toks = toks[:n-1]
			}
			toks = append(toks, token{kind: tLiteral, text: placeholder})

		case isIdentStart(c):
			j := i + 1
			for j < len(q) && isIdentChar(q[j]) {
				j++
			}
			toks = append(toks, token{kind: tIdent, text: q[i:j]})
			i = j

		case c == '(' || c == ')' || c == ',' || c == ';' || c == '.' || c == '[' || c == ']':
			toks = append(toks, token{kind: tPunct, text: string(c)})
			i++

		default: // operators: greedily take operator characters
			j := i
			for j < len(q) && strings.IndexByte("+-*/<>=~!@#%^&|`?:", q[j]) >= 0 {
				// Stop before a comment start inside an operator run.
				if (q[j] == '-' && at(q, j+1) == '-') || (q[j] == '/' && at(q, j+1) == '*') {
					break
				}
				j++
			}
			if j == i {
				j = i + 1 // unknown byte (e.g. non-ASCII outside a string): keep as-is
			}
			toks = append(toks, token{kind: tOp, text: q[i:j]})
			i = j
		}
	}
	return toks
}

// unaryContext reports whether a '-' following prev is a sign rather than a
// subtraction.
func unaryContext(prev token) bool {
	switch prev.kind {
	case tOp:
		return true
	case tPunct:
		return prev.text == "(" || prev.text == "," || prev.text == "["
	case tIdent:
		return isKeyword(prev.text)
	}
	return false
}

func skipString(q string, i int, backslash bool) int {
	i++ // opening quote
	for i < len(q) {
		switch {
		case backslash && q[i] == '\\':
			i += 2
		case q[i] == '\'' && at(q, i+1) == '\'':
			i += 2
		case q[i] == '\'':
			return i + 1
		default:
			i++
		}
	}
	return i
}

func skipNumber(q string, i int) int {
	for i < len(q) && (isDigit(q[i]) || q[i] == '.' || q[i] == '_') {
		i++
	}
	if i < len(q) && (q[i] == 'e' || q[i] == 'E') {
		j := i + 1
		if j < len(q) && (q[j] == '+' || q[j] == '-') {
			j++
		}
		if j < len(q) && isDigit(q[j]) {
			i = j
			for i < len(q) && isDigit(q[i]) {
				i++
			}
		}
	}
	return i
}

// collapse folds lists of placeholders so that queries differing only in list
// length share a fingerprint:
//
//	IN (?, ?, ?)            -> IN (...)
//	VALUES (?, ?), (?, ?)   -> VALUES (...)
//
// It works in place: the output never gets ahead of the input it reads.
func collapse(toks []token) []token {
	out := toks[:0]
	for i := 0; i < len(toks); i++ {
		if toks[i].text == "(" {
			if end, ok := placeholderList(toks, i); ok {
				// Drop ", (...)" repeats: multi-row VALUES and similar.
				if n := len(out); n >= 2 && out[n-1].text == "," && out[n-2].kind == tGroup {
					out = out[:n-1]
				} else {
					out = append(out, token{kind: tGroup, text: "(...)"})
				}
				i = end
				continue
			}
		}
		out = append(out, toks[i])
	}
	return out
}

// placeholderList reports whether toks[i] opens "(?, ?, ...)" with at least
// two placeholders, returning the index of the closing paren.
func placeholderList(toks []token, i int) (int, bool) {
	n := 0
	for j := i + 1; j < len(toks); j++ {
		switch {
		case toks[j].kind == tLiteral || toks[j].kind == tGroup:
			n++
		case toks[j].text == ",":
		case toks[j].text == ")":
			return j, n >= 2
		default:
			return 0, false
		}
	}
	return 0, false
}

func spaceBetween(prev, next token) bool {
	switch {
	case prev.text == "(" || prev.text == "." || prev.text == "[" || prev.text == "::":
		return false
	case next.text == ")" || next.text == "," || next.text == ";" || next.text == "." ||
		next.text == "]" || next.text == "::":
		return false
	case next.text == "(" || next.text == "[" || next.kind == tGroup:
		// Function calls hug their parenthesis; keywords do not.
		return prev.kind != tIdent && prev.kind != tQuoted || isKeyword(prev.text)
	}
	return true
}

// tables returns the distinct tables a query reads or writes, sorted.
func tables(toks []token) []string {
	found := map[string]bool{}
	ctes := map[string]bool{}

	// parens tracks open parentheses: whether each is a function call, and
	// its first inner token (a call containing SELECT is a subquery).
	type paren struct {
		call  bool
		first string
	}
	var parens []paren

	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if len(parens) > 0 && parens[len(parens)-1].first == "" && t.text != "(" {
			parens[len(parens)-1].first = strings.ToUpper(t.text)
		}
		switch t.text {
		case "(":
			call := i > 0 && (toks[i-1].kind == tIdent || toks[i-1].kind == tQuoted) && !isKeyword(toks[i-1].text)
			parens = append(parens, paren{call: call})
			continue
		case ")":
			if len(parens) > 0 {
				parens = parens[:len(parens)-1]
			}
			continue
		}
		if t.kind != tIdent {
			continue
		}

		prev := ""
		if i > 0 {
			prev = strings.ToUpper(toks[i-1].text)
		}
		switch strings.ToUpper(t.text) {
		case "AS":
			// "name AS (" after WITH or a comma defines a CTE, not a table.
			if i >= 2 && at2(toks, i+1) == "(" {
				p := strings.ToUpper(toks[i-2].text)
				if p == "WITH" || p == "RECURSIVE" || p == "," {
					ctes[strings.ToLower(unquote(toks[i-1].text))] = true
				}
			}

		case "FROM":
			if prev == "DISTINCT" {
				continue // IS DISTINCT FROM
			}
			if n := len(parens); n > 0 && parens[n-1].call && parens[n-1].first != "SELECT" && parens[n-1].first != "WITH" {
				continue // EXTRACT(year FROM d), SUBSTRING(s FROM 1), ...
			}
			i = readTableList(toks, i+1, found)

		case "JOIN", "INTO", "COPY", "TABLE":
			readTable(toks, i+1, found)

		case "UPDATE":
			if prev == "" || prev == ";" || prev == "(" || prev == ")" {
				readTable(toks, i+1, found)
			}
		}
	}

	out := make([]string, 0, len(found))
	for name := range found {
		if !ctes[strings.ToLower(name)] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// readTableList reads "a [AS x], b [y], ..." after FROM and returns the index
// of the last token consumed.
func readTableList(toks []token, i int, found map[string]bool) int {
	for {
		end := readTable(toks, i, found)
		if end < 0 {
			return i - 1
		}
		i = end + 1
		// Optional alias.
		if at2(toks, i) != "" && strings.EqualFold(toks[i].text, "AS") {
			i++
		}
		if i < len(toks) && (toks[i].kind == tIdent && !isKeyword(toks[i].text) || toks[i].kind == tQuoted) {
			i++
		}
		if at2(toks, i) != "," {
			return i - 1
		}
		i++
	}
}

// readTable records the (possibly schema-qualified) table name starting at
// toks[i] and returns the index of its last token, or -1 if there is none.
func readTable(toks []token, i int, found map[string]bool) int {
	for i < len(toks) && toks[i].kind == tIdent {
		switch strings.ToUpper(toks[i].text) {
		case "ONLY", "LATERAL", "IF", "NOT", "EXISTS", "TABLE", "UNLOGGED", "TEMP", "TEMPORARY":
			i++
			continue
		}
		break
	}
	if i >= len(toks) || (toks[i].kind != tIdent && toks[i].kind != tQuoted) || isKeyword(toks[i].text) {
		return -1
	}
	name := unquote(toks[i].text)
	for i+2 < len(toks) && toks[i+1].text == "." && (toks[i+2].kind == tIdent || toks[i+2].kind == tQuoted) {
		name += "." + unquote(toks[i+2].text)
		i += 2
	}
	if i+1 < len(toks) && (toks[i+1].text == "(" || toks[i+1].kind == tGroup) && !isWriteTarget(toks, i) {
		return -1 // set-returning function such as generate_series(...)
	}
	found[name] = true
	return i
}

// isWriteTarget reports whether the name at toks[i] is the target of
// INSERT INTO t (cols) or COPY t (cols), where a paren follows the table.
func isWriteTarget(toks []token, i int) bool {
	j := i - 1
	for j >= 1 && toks[j].text == "." {
		j -= 2 // skip schema qualifier
	}
	if j >= 0 && strings.EqualFold(toks[j].text, "ONLY") {
		j--
	}
	if j < 0 {
		return false
	}
	switch strings.ToUpper(toks[j].text) {
	case "INTO", "COPY", "TABLE", "EXISTS": // EXISTS: CREATE TABLE IF NOT EXISTS t (...)
		return true
	}
	return false
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}

func at(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func at2(toks []token, i int) string {
	if i < len(toks) {
		return toks[i].text
	}
	return ""
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}
func isIdentChar(c byte) bool { return isIdentStart(c) || isDigit(c) || c == '$' }

// keywords that may precede "(" or appear where a table name is expected but
// are never table names themselves.
var keywords = map[string]bool{}

func init() {
	for _, k := range strings.Fields(`
		ALL AND ANY ARRAY AS ASC BETWEEN BY CASE CAST CROSS DEFAULT DELETE DESC
		DISTINCT DO ELSE END EXCEPT EXISTS FETCH FOR FROM FULL GROUP HAVING ILIKE
		IN INNER INSERT INTERSECT INTO IS JOIN LATERAL LEFT LIKE LIMIT NATURAL NOT
		NULL OFFSET ON OR ORDER OUTER OVER RETURNING RIGHT ROW ROWS SELECT SET
		SIMILAR SOME STDIN STDOUT THEN UNION UPDATE USING VALUES WHEN WHERE WINDOW
		WITH`) {
		keywords[k] = true
	}
}

func isKeyword(s string) bool { return keywords[strings.ToUpper(s)] }

// template rebuilds the query with every literal replaced by a numbered bind
// parameter, so it can be prepared and explained without knowing any values:
//
//	SELECT * FROM users WHERE email = 'a@b.com' AND id = $1
//	-> SELECT * FROM users WHERE email = $2 AND id = $1
//
// Typed literals such as interval '1 day' become $n::interval. It returns ""
// for empty input and for multi-statement queries, which cannot be prepared.
func template(toks []token) string {
	for len(toks) > 0 && toks[len(toks)-1].text == ";" {
		toks = toks[:len(toks)-1]
	}
	if len(toks) == 0 {
		return ""
	}
	next := 0
	for _, t := range toks {
		if t.text == ";" {
			return ""
		}
		if t.param != "" {
			if n, err := strconv.Atoi(t.param[1:]); err == nil && n > next {
				next = n
			}
		}
	}

	var b strings.Builder
	b.Grow(len(toks) * 6)
	var prev token
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		out := t
		switch {
		case t.kind == tLiteral && t.param != "":
			out.text = t.param
		case t.kind == tLiteral && t.onlyPct:
			out.text = "'%'" // a bare wildcard carries no data; keep it
		case t.kind == tLiteral:
			next++
			out.text = "$" + strconv.Itoa(next)
			// Keep a LIKE pattern's wildcards so EXPLAIN still sees a
			// leading-% search: '%abc%' -> '%' || $1 || '%'.
			if t.leadPct {
				out.text = "'%' || " + out.text
			}
			if t.trailPct {
				out.text += " || '%'"
			}
		case t.kind == tIdent && typedLiterals[strings.ToLower(t.text)] &&
			i+1 < len(toks) && toks[i+1].kind == tLiteral && toks[i+1].param == "":
			next++
			out = token{kind: tLiteral, text: "$" + strconv.Itoa(next) + "::" + strings.ToLower(t.text)}
			i++ // the literal is consumed
		}
		if b.Len() > 0 && spaceBetween(prev, out) {
			b.WriteByte(' ')
		}
		b.WriteString(out.text)
		prev = out
	}
	return b.String()
}

// typedLiterals are type names that can prefix a string literal, as in
// DATE '2024-01-01'. A bare parameter cannot follow them, so they become casts.
var typedLiterals = map[string]bool{
	"interval": true, "date": true, "time": true, "timestamp": true, "timestamptz": true,
	"uuid": true, "json": true, "jsonb": true, "inet": true, "cidr": true,
}

// MultiStatement reports whether q holds more than one statement, ignoring
// semicolons inside strings, quoted names and comments, and a trailing one.
func MultiStatement(q string) bool {
	toks := tokenize(q)
	for len(toks) > 0 && toks[len(toks)-1].text == ";" {
		toks = toks[:len(toks)-1]
	}
	for _, t := range toks {
		if t.text == ";" {
			return true
		}
	}
	return false
}
