// Package sqlcomment reads request context that applications attach to
// their queries as sqlcommenter comments (https://google.github.io/sqlcommenter/):
//
//	SELECT * FROM users WHERE id = $1 /*route='GET%20%2Fusers%2F%3Aid',traceparent='00-4bf9…-00f0…-01'*/
//
// The Tracelet SDKs write these, and so do the sqlcommenter and OpenTelemetry
// integrations for many ORMs, so Tracelet can group queries by request
// without knowing anything about the application.
package sqlcomment

import (
	"net/url"
	"strings"

	"github.com/moseleydev/tracelet/core"
)

// Fields is the context carried by one comment.
type Fields map[string]string

// TraceID returns the W3C trace id from traceparent, or "".
func (f Fields) TraceID() string {
	// version-traceid(32 hex)-parentid(16 hex)-flags(2 hex)
	parts := strings.Split(f["traceparent"], "-")
	if len(parts) != 4 || len(parts[1]) != 32 || !isHex(parts[1]) || strings.Trim(parts[1], "0") == "" {
		return ""
	}
	return parts[1]
}

// Route returns the request route: "route" if present, else
// "controller#action" as Rails and Django integrations send it.
func (f Fields) Route() string {
	if r := f["route"]; r != "" {
		return r
	}
	if c := f["controller"]; c != "" {
		if a := f["action"]; a != "" {
			return c + "#" + a
		}
		return c
	}
	return ""
}

// Parse splits a trailing sqlcommenter comment off query. It returns the
// query without the comment and the decoded fields, or the query unchanged
// and nil when there is no well-formed sqlcommenter comment.
func Parse(query string) (string, Fields) {
	s := strings.TrimRight(query, " \t\r\n;")
	if !strings.HasSuffix(s, "*/") {
		return query, nil
	}
	start := strings.LastIndex(s, "/*")
	if start < 0 {
		return query, nil
	}
	fields := parsePairs(s[start+2 : len(s)-2])
	if fields == nil {
		return query, nil
	}
	return strings.TrimRight(s[:start], " \t\r\n"), fields
}

// parsePairs reads key='value',key2='value2'. It returns nil if the text is
// not in that form (an ordinary comment).
func parsePairs(text string) Fields {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	out := Fields{}
	for len(text) > 0 {
		eq := strings.IndexByte(text, '=')
		if eq <= 0 || eq+1 >= len(text) || text[eq+1] != '\'' {
			return nil
		}
		key, err := url.PathUnescape(strings.TrimSpace(text[:eq]))
		if err != nil || strings.ContainsAny(key, " '") {
			return nil
		}
		// Value: up to the next unescaped quote.
		var val strings.Builder
		i := eq + 2
		for ; i < len(text); i++ {
			if text[i] == '\\' && i+1 < len(text) && text[i+1] == '\'' {
				val.WriteByte('\'')
				i++
				continue
			}
			if text[i] == '\'' {
				break
			}
			val.WriteByte(text[i])
		}
		if i >= len(text) {
			return nil // unterminated value
		}
		v, err := url.PathUnescape(val.String())
		if err != nil {
			return nil
		}
		out[key] = v
		text = strings.TrimSpace(text[i+1:])
		if text == "" {
			break
		}
		if text[0] != ',' {
			return nil
		}
		text = strings.TrimSpace(text[1:])
	}
	return out
}

// Annotate fills e.TraceID and e.Route from the query's comment, if they
// are not already set, and returns the query without the comment.
func Annotate(e *core.Event) string {
	stripped, f := Parse(e.Query)
	if f == nil {
		return e.Query
	}
	if e.TraceID == "" {
		e.TraceID = f.TraceID()
	}
	if e.Route == "" {
		e.Route = f.Route()
	}
	return stripped
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
