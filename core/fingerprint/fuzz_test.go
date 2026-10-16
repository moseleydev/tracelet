package fingerprint

import (
	"strings"
	"testing"
)

// FuzzOf checks that any input fingerprints without panicking, and that
// fingerprints are stable and never contain the original literal values.
func FuzzOf(f *testing.F) {
	for _, s := range []string{
		"SELECT * FROM users WHERE email = 'a@b.com' AND id IN (1, 2, 3)",
		"INSERT INTO t (a) VALUES ($1), ($2) ON CONFLICT DO NOTHING",
		"WITH x AS (SELECT 1) SELECT * FROM x JOIN y ON y.id = x.id",
		"SELECT $$a$$, E'\\\\x', B'101', \"Weird\"\"Name\" FROM s.t /* c */ -- d",
		"/* unterminated", "'", "$", "$a", "$1x", "--", "((((", "SELECT 1; SELECT 2",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, q string) {
		a, b := Of(q), Of(q)
		if a.Hash != b.Hash || a.Query != b.Query || a.Template != b.Template {
			t.Fatalf("fingerprint not stable for %q", q)
		}
		if strings.Contains(q, "'secret-value'") && strings.Contains(a.Query, "secret-value") {
			t.Fatalf("literal leaked into fingerprint: %q", a.Query)
		}
	})
}
