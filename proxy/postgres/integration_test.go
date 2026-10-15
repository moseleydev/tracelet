package postgres

import (
	"context"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/moseleydev/tracelet/core"
	"github.com/moseleydev/tracelet/proxy"
)

// Integration tests run against a real PostgreSQL server when
// TRACELET_TEST_POSTGRES_URL is set, e.g.
//
//	TRACELET_TEST_POSTGRES_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable go test ./proxy/postgres
func realPostgres(tb testing.TB) *url.URL {
	tb.Helper()
	raw := os.Getenv("TRACELET_TEST_POSTGRES_URL")
	if raw == "" {
		tb.Skip("TRACELET_TEST_POSTGRES_URL not set")
	}
	u, err := url.Parse(raw)
	if err != nil {
		tb.Fatal(err)
	}
	return u
}

// throughProxy starts a proxy in front of the real server and returns a
// connection URL that goes through it.
func throughProxy(tb testing.TB, u *url.URL, sink *proxy.Sink) string {
	tb.Helper()
	srv, err := New(Config{Upstream: u.Host, SSLMode: u.Query().Get("sslmode"), Sink: sink})
	if err != nil {
		tb.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		srv.Serve(ctx, ln)
		close(done)
	}()
	tb.Cleanup(func() {
		cancel()
		<-done
	})

	p := *u
	p.Host = ln.Addr().String()
	return p.String()
}

func TestIntegrationPgx(t *testing.T) {
	u := realPostgres(t)
	sink := proxy.NewSink(1000)
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, throughProxy(t, u, sink))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustExec("CREATE TEMP TABLE people (id int PRIMARY KEY, name text)")
	for i, name := range []string{"ada", "grace", "linus"} {
		mustExec("INSERT INTO people VALUES ($1, $2)", i+1, name)
	}

	var names []string
	rows, err := conn.Query(ctx, "SELECT name FROM people WHERE id > $1 ORDER BY id", 1)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		names = append(names, n)
	}
	if rows.Err() != nil || len(names) != 2 {
		t.Fatalf("query through proxy returned %v, %v", names, rows.Err())
	}

	// Simple protocol and an error, on the same connection.
	if _, err := conn.Exec(ctx, "UPDATE people SET name = upper(name)", pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "SELECT * FROM missing_table"); err == nil {
		t.Fatal("expected an error for a missing table")
	}
	// With arguments, pgx prepares first, so the error comes from Parse and
	// no Execute is ever sent.
	var x int
	if err := conn.QueryRow(ctx, "SELECT x FROM missing_prepared WHERE id = $1", 1).Scan(&x); err == nil {
		t.Fatal("expected an error for a missing table")
	}

	// Collect what the proxy saw.
	conn.Close(ctx)
	var events []core.Event
	for len(sink.Events()) > 0 {
		events = append(events, <-sink.Events())
	}
	find := func(prefix string) core.Event {
		t.Helper()
		for _, e := range events {
			if strings.HasPrefix(e.Query, prefix) {
				return e
			}
		}
		t.Fatalf("no event for %q in %d events", prefix, len(events))
		return core.Event{}
	}

	inserts := 0
	for _, e := range events {
		if strings.HasPrefix(e.Query, "INSERT INTO people") {
			inserts++
			if e.Rows != 1 || e.Params != nil {
				t.Errorf("insert event: %+v", e)
			}
		}
	}
	if inserts != 3 {
		t.Errorf("saw %d insert events, want 3", inserts)
	}
	if e := find("SELECT name FROM people"); e.Rows != 2 || e.Error != "" {
		t.Errorf("select event: %+v", e)
	}
	if e := find("UPDATE people"); e.Rows != 3 {
		t.Errorf("simple-protocol update event: %+v", e)
	}
	if e := find("SELECT * FROM missing_table"); !strings.Contains(e.Error, "42P01") {
		t.Errorf("error event: %+v", e)
	}
	if e := find("SELECT x FROM missing_prepared"); !strings.Contains(e.Error, "42P01") {
		t.Errorf("failed-prepare event: %+v", e)
	}
}

// BenchmarkDirect and BenchmarkViaProxy measure round-trip latency of a
// trivial query with and without Tracelet in the path. Compare with:
//
//	go test ./proxy/postgres -run '^$' -bench . -benchtime 5000x
func BenchmarkDirect(b *testing.B) {
	benchSelect1(b, realPostgres(b).String())
}

func BenchmarkViaProxy(b *testing.B) {
	sink := proxy.NewSink(1 << 16)
	go func() {
		for range sink.Events() {
		}
	}()
	benchSelect1(b, throughProxy(b, realPostgres(b), sink))
}

func benchSelect1(b *testing.B, connURL string) {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connURL)
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close(ctx)

	var n int
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := conn.QueryRow(ctx, "SELECT $1::int", i).Scan(&n); err != nil {
			b.Fatal(err)
		}
	}
}
