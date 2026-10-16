package replay

import (
	"context"
	"encoding/binary"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/moseleydev/tracelet/core"
	"github.com/moseleydev/tracelet/core/fingerprint"
	"github.com/moseleydev/tracelet/core/session"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		q                        string
		writes, txControl, noTxn bool
	}{
		{"SELECT * FROM t", false, false, false},
		{"  /* c */ select 1", false, false, false},
		{"SELECT * FROM t FOR UPDATE", true, false, false},
		{"WITH x AS (DELETE FROM t RETURNING *) SELECT * FROM x", true, false, false},
		{"SELECT * INTO new_table FROM t", true, false, false},
		{"INSERT INTO t VALUES (1)", true, false, false},
		{"update t set a = 1", true, false, false},
		{"BEGIN", false, true, false},
		{"start transaction isolation level serializable", false, true, false},
		{"COMMIT", false, true, false},
		{"end", false, true, false},
		{"ROLLBACK TO SAVEPOINT a", false, true, false},
		{"VACUUM t", true, false, true},
		{"CREATE INDEX CONCURRENTLY i ON t (a)", true, false, true},
		{"ALTER SYSTEM SET work_mem = '1GB'", true, false, true},
		{"PREPARE TRANSACTION 'x'", true, false, true},
		{"COMMIT PREPARED 'x'", true, true, true},
		{"COPY t FROM STDIN", true, false, true},
		{"DROP DATABASE prod", true, false, true},
	}
	for _, c := range cases {
		k := classify(c.q)
		if k.writes != c.writes || k.txControl != c.txControl || k.noTxn != c.noTxn {
			t.Errorf("classify(%q) = writes %v, txControl %v, noTxn %v; want %v %v %v",
				c.q, k.writes, k.txControl, k.noTxn, c.writes, c.txControl, c.noTxn)
		}
	}
}

func TestSameDatabase(t *testing.T) {
	cfg := func(u string) *pgconn.Config {
		c, err := pgconn.ParseConfig(u)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if !sameDatabase("localhost:5432/app", cfg("postgres://u@127.0.0.1:5432/app")) {
		t.Error("localhost and 127.0.0.1 are the same server")
	}
	if sameDatabase("localhost:5432/app", cfg("postgres://u@127.0.0.1:5432/app_staging")) {
		t.Error("a different database name is a different database")
	}
	if sameDatabase("db.prod:5432/app", cfg("postgres://u@db.staging:5432/app")) {
		t.Error("different hosts are different databases")
	}
}

// --- Against a real database ---------------------------------------------

func target(t *testing.T) (string, *pgx.Conn) {
	t.Helper()
	raw := os.Getenv("TRACELET_TEST_POSTGRES_URL")
	if raw == "" {
		t.Skip("TRACELET_TEST_POSTGRES_URL not set")
	}
	ctx := context.Background()
	db, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	_, err = db.Exec(ctx, `
		DROP TABLE IF EXISTS rp_accounts;
		CREATE TABLE rp_accounts (id int PRIMARY KEY, balance int NOT NULL);
		INSERT INTO rp_accounts SELECT g, 100 FROM generate_series(1, 50) g;`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(context.Background(), `DROP TABLE IF EXISTS rp_accounts`) })
	return raw, db
}

func int4(v int32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, uint32(v)); return b }

// rec builds a session entry as the proxy would record it.
type recorder struct {
	t     *testing.T
	w     *session.Writer
	start time.Time
	at    time.Duration
}

func newSession(t *testing.T, upstream string) (*recorder, string) {
	path := filepath.Join(t.TempDir(), "s.trc.gz")
	start := time.Now()
	w, err := session.Create(path, session.Header{Upstream: upstream, StartedAt: start}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return &recorder{t: t, w: w, start: start}, path
}

func (r *recorder) add(conn uint64, x *core.Replay) {
	r.at += time.Millisecond
	r.w.Record(core.Event{StartedAt: r.start.Add(r.at), Duration: 500 * time.Microsecond, ConnID: conn, Replay: x},
		fingerprint.Of(x.Query).Hash)
}

func simple(q string) *core.Replay { return &core.Replay{Simple: true, Query: q} }

func balances(t *testing.T, db *pgx.Conn) (sum, rows int) {
	if err := db.QueryRow(context.Background(), `SELECT coalesce(sum(balance), 0), count(*) FROM rp_accounts`).Scan(&sum, &rows); err != nil {
		t.Fatal(err)
	}
	return
}

// A session full of writes, some of them adversarial.
func writeHeavySession(t *testing.T, upstream string) string {
	r, path := newSession(t, upstream)
	// Reads, with a binary parameter, as pgx sends them.
	r.add(1, &core.Replay{Query: "SELECT balance FROM rp_accounts WHERE id = $1", ParamOIDs: []uint32{23},
		ParamFormats: []int16{1}, Params: [][]byte{int4(7)}, ResultFormats: []int16{1}})
	// Plain writes outside any transaction.
	r.add(1, &core.Replay{Query: "UPDATE rp_accounts SET balance = balance + $1 WHERE id = $2", ParamOIDs: []uint32{23, 23},
		ParamFormats: []int16{1}, Params: [][]byte{int4(1000), int4(1)}})
	r.add(1, simple("INSERT INTO rp_accounts VALUES (999, 1)"))
	r.add(1, simple("DELETE FROM rp_accounts WHERE id = 2"))
	// An application transaction that commits.
	r.add(2, simple("BEGIN"))
	r.add(2, simple("UPDATE rp_accounts SET balance = 0"))
	r.add(2, simple("COMMIT"))
	// A transaction that fails, then commits anyway (PostgreSQL rolls it back).
	r.add(3, simple("BEGIN"))
	r.add(3, simple("INSERT INTO rp_accounts VALUES (1, 1)")) // duplicate key
	r.add(3, simple("UPDATE rp_accounts SET balance = -1"))
	r.add(3, simple("COMMIT"))
	// Attempts to escape the outer transaction.
	r.add(4, simple("UPDATE rp_accounts SET balance = 5; COMMIT;"))
	r.add(4, simple("VACUUM rp_accounts"))
	r.add(4, simple("TRUNCATE rp_accounts"))
	r.add(4, simple("COMMIT"))
	r.add(4, simple("END"))
	if err := r.w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRollbackModeNeverChangesData(t *testing.T) {
	url, db := target(t)
	sumBefore, rowsBefore := balances(t, db)
	path := writeHeavySession(t, "elsewhere:5432/prod")

	res, err := Run(context.Background(), path, Options{Target: url, Mode: Rollback, Speed: 0})
	if err != nil {
		t.Fatal(err)
	}
	if sum, rows := balances(t, db); sum != sumBefore || rows != rowsBefore {
		t.Fatalf("rollback replay changed the database: sum %d->%d, rows %d->%d", sumBefore, sum, rowsBefore, rows)
	}
	if res.Skipped["several statements in one query"] != 1 || res.Skipped["can't run inside a transaction"] != 1 {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
	// The writes really ran (inside the rolled-back transaction).
	if res.Sent < 10 {
		t.Fatalf("only %d statements were replayed", res.Sent)
	}
	// The expected failures: the duplicate key, and the statement after it
	// in the same (now aborted) application transaction.
	if res.Errors != 2 {
		for _, q := range res.Queries {
			if q.Errors > 0 {
				t.Logf("%s: %s", q.Query, q.LastError)
			}
		}
		t.Fatalf("errors = %d, want 2", res.Errors)
	}
}

func TestReadModeSkipsWritesAndIsReadOnly(t *testing.T) {
	url, db := target(t)
	sumBefore, rowsBefore := balances(t, db)
	r, path := newSession(t, "")
	r.add(1, simple("SELECT count(*) FROM rp_accounts"))
	r.add(1, simple("UPDATE rp_accounts SET balance = 0"))
	// A read that calls a function: allowed, it changes no data.
	r.add(1, simple("SELECT set_config('x.y', '1', false) FROM rp_accounts LIMIT 1"))
	r.add(1, simple("CREATE TEMP TABLE sneaky AS SELECT 1"))
	r.w.Close()

	res, err := Run(context.Background(), path, Options{Target: url, Speed: 0})
	if err != nil {
		t.Fatal(err)
	}
	if sum, rows := balances(t, db); sum != sumBefore || rows != rowsBefore {
		t.Fatal("read replay changed the database")
	}
	if res.Skipped["write (read mode)"] != 2 || res.Sent != 2 || res.Errors != 0 {
		t.Fatalf("sent %d, errors %d, skipped %+v", res.Sent, res.Errors, res.Skipped)
	}

	// Even if a write reached the server, the connection is read-only.
	cfg, _ := pgconn.ParseConfig(url)
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	conn, err := pgconn.ConnectConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	_, err = conn.Exec(context.Background(), "UPDATE rp_accounts SET balance = 0").ReadAll()
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("write on a read-only replay connection: %v", err)
	}
}

func TestRefusesToReplayIntoTheSource(t *testing.T) {
	raw, _ := target(t)
	u, _ := url.Parse(raw)
	upstream := u.Host + u.Path // as the recorder writes it
	path := writeHeavySession(t, upstream)

	if _, err := Run(context.Background(), path, Options{Target: raw, Mode: Rollback}); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("rollback into the source: %v", err)
	}
	if _, err := Run(context.Background(), path, Options{Target: raw, Mode: Read}); err == nil || !strings.Contains(err.Error(), "--allow-source") {
		t.Fatalf("read from the source without --allow-source: %v", err)
	}
	if _, err := Run(context.Background(), path, Options{Target: raw, Mode: Read, AllowSource: true, Speed: 0}); err != nil {
		t.Fatalf("read from the source with --allow-source: %v", err)
	}
	if _, err := Run(context.Background(), path, Options{Mode: Read}); err == nil {
		t.Fatal("a replay without a target must be refused")
	}
}

func TestScaleAndSpeed(t *testing.T) {
	url, _ := target(t)
	r, path := newSession(t, "")
	for i := 0; i < 20; i++ {
		r.add(uint64(1+i%2), &core.Replay{Query: "SELECT balance FROM rp_accounts WHERE id = $1", ParamOIDs: []uint32{23},
			ParamFormats: []int16{0}, Params: [][]byte{[]byte("3")}})
	}
	r.w.Close()

	// 20 queries recorded over ~20ms; at speed 0.5 that takes ~40ms.
	start := time.Now()
	res, err := Run(context.Background(), path, Options{Target: url, Speed: 0.5, Scale: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 60 || res.Errors != 0 {
		t.Fatalf("sent %d, errors %d; want 60, 0", res.Sent, res.Errors)
	}
	if elapsed := time.Since(start); elapsed < 35*time.Millisecond {
		t.Fatalf("replay at half speed finished in %v; it should follow the recorded pace", elapsed)
	}
	q := res.Queries[0]
	if q.Recorded.Count != 20 || q.Replayed.Count != 60 || q.Replayed.P95 <= 0 || !strings.Contains(q.Query, "WHERE id = ?") {
		t.Fatalf("query result = %+v", q)
	}
}
