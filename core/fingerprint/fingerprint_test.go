package fingerprint

import (
	"reflect"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT * FROM users WHERE id = 42", "SELECT * FROM users WHERE id = ?"},
		{"SELECT name FROM users WHERE email = 'a@b.com' AND age > -5", "SELECT name FROM users WHERE email = ? AND age > ?"},
		{"UPDATE accounts SET balance = balance - 100 WHERE id = 7", "UPDATE accounts SET balance = balance - ? WHERE id = ?"},
		{"SELECT * FROM t WHERE id IN (1, 2, 3)", "SELECT * FROM t WHERE id IN (...)"},
		{"INSERT INTO orders (user_id, amount) VALUES ($1, $2), ($3, $4), ($5, $6)", "INSERT INTO orders(user_id, amount) VALUES (...)"},
		{"SELECT count(*) FROM orders WHERE email LIKE '%abc%'", "SELECT count(*) FROM orders WHERE email LIKE ?"},
		{"/* route=/search */ SELECT 1 -- trailing", "SELECT ?"},
		{`SELECT * FROM "Users" WHERE name = E'it\'s'`, `SELECT * FROM "Users" WHERE name = ?`},
		{"SELECT $$hello$$, $tag$x$tag$", "SELECT ?, ?"},
		{"SELECT x::int, 1.5e-3, .5 FROM t", "SELECT x::int, ?, ? FROM t"},
		{"SELECT * FROM t WHERE x = ANY($1)", "SELECT * FROM t WHERE x = ANY (?)"},
		{"SELECT now() - interval '1 day'", "SELECT now() - interval ?"},
		{"SELECT  *\n\tFROM   t\nWHERE a=1", "SELECT * FROM t WHERE a = ?"},
		{"SELECT 'unterminated", "SELECT ?"},
		{"-- ping", "-- ping"},
		{"  ", "(empty query)"},
	}
	for _, c := range cases {
		if got := Of(c.in).Query; got != c.want {
			t.Errorf("Of(%q).Query\n got  %q\n want %q", c.in, got, c.want)
		}
	}
}

func TestSameShapeSameHash(t *testing.T) {
	groups := [][]string{
		{
			"SELECT * FROM users WHERE id = 42",
			"select * from users where id = $1",
			"SELECT *   FROM users WHERE id = -7 -- comment",
			"SELECT * FROM users WHERE id = 3;",
		},
		{
			"SELECT * FROM t WHERE id IN (1, 2)",
			"SELECT * FROM t WHERE id IN ($1, $2, $3, $4)",
		},
		{
			"INSERT INTO t (a) VALUES (1, 2)",
			"INSERT INTO t (a) VALUES (1, 2), (3, 4), (5, 6)",
		},
	}
	for _, g := range groups {
		want := Of(g[0]).Hash
		for _, q := range g[1:] {
			if got := Of(q).Hash; got != want {
				t.Errorf("%q and %q should share a fingerprint", g[0], q)
			}
		}
	}

	if Of("SELECT * FROM users").Hash == Of("SELECT * FROM orders").Hash {
		t.Error("different tables must not share a fingerprint")
	}
	if Of(`SELECT * FROM "Users"`).Hash == Of(`SELECT * FROM "users"`).Hash {
		t.Error("quoted identifiers are case-sensitive")
	}
}

func TestTables(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"SELECT * FROM users WHERE id = 1", []string{"users"}},
		{"SELECT u.name FROM users u JOIN orders o ON o.user_id = u.id LEFT JOIN public.items AS i ON true", []string{"orders", "public.items", "users"}},
		{"SELECT * FROM a, b AS bb, c WHERE a.id = b.id", []string{"a", "b", "c"}},
		{"WITH recent AS (SELECT * FROM orders), top AS (SELECT * FROM recent) SELECT * FROM top", []string{"orders"}},
		{"SELECT extract(year FROM created_at) FROM events", []string{"events"}},
		{"SELECT * FROM t WHERE a IS DISTINCT FROM b", []string{"t"}},
		{"SELECT * FROM generate_series(1, 10) g", []string{}},
		{"SELECT * FROM a WHERE id IN (SELECT a_id FROM b) AND EXISTS (SELECT 1 FROM c)", []string{"a", "b", "c"}},
		{"SELECT * FROM (SELECT * FROM inner_t) sub", []string{"inner_t"}},
		{"UPDATE accounts SET x = 1 WHERE id = 7", []string{"accounts"}},
		{"SELECT * FROM jobs WHERE id = 1 FOR UPDATE SKIP LOCKED", []string{"jobs"}},
		{"INSERT INTO t (id) VALUES (1) ON CONFLICT (id) DO UPDATE SET x = excluded.x", []string{"t"}},
		{"DELETE FROM sessions WHERE expires_at < now()", []string{"sessions"}},
		{`SELECT * FROM "Users"`, []string{"Users"}},
		{"CREATE TABLE IF NOT EXISTS orders AS SELECT g AS id FROM generate_series(1, 300) g", []string{"orders"}},
		{"TRUNCATE TABLE logs", []string{"logs"}},
		{"COPY people (id, name) FROM STDIN", []string{"people"}},
		{"SELECT 1", []string{}},
	}
	for _, c := range cases {
		if got := Of(c.in).Tables; !reflect.DeepEqual(got, c.want) {
			t.Errorf("Of(%q).Tables = %v, want %v", c.in, got, c.want)
		}
	}
}

func BenchmarkFingerprint(b *testing.B) {
	q := "SELECT u.id, u.name, o.total FROM users u JOIN orders o ON o.user_id = u.id WHERE u.email = 'a@b.com' AND o.status IN ('paid', 'shipped') ORDER BY o.created_at DESC LIMIT 20"
	for i := 0; i < b.N; i++ {
		Of(q)
	}
}
