package analyzer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/moseleydev/tracelet/core"
	"github.com/moseleydev/tracelet/core/fingerprint"
	"github.com/moseleydev/tracelet/core/store"
	"github.com/moseleydev/tracelet/detectors"
)

func TestExplainable(t *testing.T) {
	for q, want := range map[string]bool{
		"SELECT * FROM t WHERE id = $1":        true,
		"WITH x AS (SELECT 1) SELECT * FROM x": true,
		"UPDATE t SET a = $1 WHERE id = $2":    true,
		"DELETE FROM t WHERE id = $1":          true,
		"INSERT INTO t (a) VALUES ($1)":        false,
		"INSERT INTO t SELECT * FROM u":        true,
		"begin":                                false,
		"CREATE INDEX ON t (a)":                false,
		"":                                     false,
	} {
		if got := Explainable(q); got != want {
			t.Errorf("Explainable(%q) = %v, want %v", q, got, want)
		}
	}
}

// The tests below need a real database:
//
//	TRACELET_TEST_POSTGRES_URL=postgres://localhost:5432/postgres?sslmode=disable go test ./analyzer
func setup(t *testing.T) (string, *pgx.Conn) {
	t.Helper()
	url := os.Getenv("TRACELET_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("TRACELET_TEST_POSTGRES_URL not set")
	}
	ctx := context.Background()
	db, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(ctx) })
	_, err = db.Exec(ctx, `
		DROP TABLE IF EXISTS tl_orders, tl_customers;
		CREATE TABLE tl_customers (id int PRIMARY KEY, email text NOT NULL);
		CREATE TABLE tl_orders (id int PRIMARY KEY, customer_id int NOT NULL, status text NOT NULL, created_at timestamptz NOT NULL);
		INSERT INTO tl_customers SELECT g, 'c' || g || '@example.com' FROM generate_series(1, 20000) g;
		INSERT INTO tl_orders SELECT g, 1 + g % 5000, 'paid', now() - g * interval '1 minute' FROM generate_series(1, 50000) g;
		ANALYZE tl_customers, tl_orders;`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(context.Background(), `DROP TABLE IF EXISTS tl_orders, tl_customers`) })
	// Enable HypoPG where the server has it, for the what-if tests.
	db.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS hypopg`)
	return url, db
}

func hasHypoPG(t *testing.T, db *pgx.Conn) bool {
	var ok bool
	db.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'hypopg')`).Scan(&ok)
	return ok
}

func TestSimulate(t *testing.T) {
	url, db := setup(t)
	ctx := context.Background()
	x, err := Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close(ctx)

	template := fingerprint.Of("SELECT id, status FROM tl_orders WHERE customer_id = 42 ORDER BY created_at DESC LIMIT 20").Template
	plan, err := x.Explain(ctx, template)
	if err != nil {
		t.Fatal(err)
	}
	fs := detectors.Run(detectors.Input{Plan: plan, Stats: core.Stats{Count: 100}})
	if len(fs) == 0 || fs[0].Index == nil {
		t.Fatalf("expected an index suggestion, got %+v", fs)
	}
	sim := x.Simulate(ctx, template, *fs[0].Index)

	if !hasHypoPG(t, db) {
		// Without HypoPG there is no estimate, just instructions.
		if !strings.Contains(sim.Unavailable, "CREATE EXTENSION hypopg") || sim.Speedup != 0 {
			t.Fatalf("simulation without hypopg = %+v", sim)
		}
		t.Skip("hypopg not available on this server; checked the fallback message only")
	}

	if sim.Unavailable != "" || !sim.UsesIndex || sim.CostAfter >= sim.CostBefore || sim.Speedup < 10 {
		t.Fatalf("simulation = %+v", sim)
	}
	t.Logf("estimated speedup %.0fx (cost %.1f -> %.1f)", sim.Speedup, sim.CostBefore, sim.CostAfter)

	// The hypothetical index must not leak into later plans.
	plan, err = x.Explain(ctx, template)
	if err != nil {
		t.Fatal(err)
	}
	plan.Walk(func(n, _ *detectors.Plan) {
		if strings.Contains(n.Index, "btree") || len(n.Indexes) > 1 {
			t.Fatalf("hypothetical index leaked into a later plan: %+v", n)
		}
	})

	// An index the query can't use is reported as unused.
	useless := x.Simulate(ctx, template, detectors.IndexSuggestion{Table: "public.tl_orders", Columns: []string{"status"}})
	if useless.Unavailable != "" || useless.UsesIndex || useless.Speedup != 0 {
		t.Fatalf("useless index simulation = %+v", useless)
	}

	// GIN indexes are outside what HypoPG can model.
	gin := x.Simulate(ctx, template, detectors.IndexSuggestion{Table: "public.tl_customers", Columns: []string{"email gin_trgm_ops"}, Method: "gin", Extension: "pg_trgm"})
	if !strings.Contains(gin.Unavailable, "btree") {
		t.Fatalf("gin simulation = %+v", gin)
	}
}

func TestExplainRealPlans(t *testing.T) {
	url, db := setup(t)
	ctx := context.Background()
	x, err := Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close(ctx)

	// Missing index, with a sort the index could also serve.
	plan, err := x.Explain(ctx, fingerprint.Of(
		"SELECT id, status FROM tl_orders WHERE customer_id = 42 ORDER BY created_at DESC LIMIT 20").Template)
	if err != nil {
		t.Fatal(err)
	}
	var scan *detectors.Plan
	plan.Walk(func(n, _ *detectors.Plan) {
		if n.Node == "Seq Scan" {
			scan = n
		}
	})
	if scan == nil || scan.Relation != "tl_orders" || scan.Schema != "public" || scan.TableRows < 40000 {
		t.Fatalf("expected an annotated seq scan, got %+v", scan)
	}
	if len(scan.Indexes) != 1 || scan.Indexes[0].Columns[0] != "id" || !scan.Indexes[0].Unique {
		t.Fatalf("indexes = %+v", scan.Indexes)
	}
	f := (detectors.Input{})
	f.Plan, f.Stats = plan, core.Stats{Count: 100}
	findings := detectors.Run(f)
	if len(findings) == 0 || !strings.Contains(findings[0].Fix, "ON public.tl_orders (customer_id, created_at DESC)") {
		t.Fatalf("findings from a real plan = %+v", findings)
	}

	// UPDATE is planned but never executed, and the next explain works
	// (the prepared statement was cleaned up).
	if _, err := x.Explain(ctx, "UPDATE tl_orders SET status = $1 WHERE customer_id = $2"); err != nil {
		t.Fatal(err)
	}
	var changed int
	db.QueryRow(ctx, `SELECT count(*) FROM tl_orders WHERE status <> 'paid'`).Scan(&changed)
	if changed != 0 {
		t.Fatalf("EXPLAIN modified %d rows", changed)
	}

	// A leading-wildcard search keeps its shape in the template.
	plan, err = x.Explain(ctx, fingerprint.Of("SELECT id FROM tl_customers WHERE email LIKE '%abc%'").Template)
	if err != nil {
		t.Fatal(err)
	}
	findings = detectors.Run(detectors.Input{Plan: plan, Stats: core.Stats{Count: 100, P95: time.Second}})
	if len(findings) != 1 || findings[0].Detector != "full-scan" || !strings.Contains(findings[0].Fix, "gin_trgm_ops") {
		t.Fatalf("wildcard findings = %+v", findings)
	}

	// Errors are reported, and the connection stays usable.
	if _, err := x.Explain(ctx, "SELECT * FROM tl_missing WHERE id = $1"); err == nil {
		t.Fatal("expected an error for a missing table")
	}
	if _, err := x.Explain(ctx, "SELECT * FROM tl_customers WHERE id = $1"); err != nil {
		t.Fatalf("explain after an error: %v", err)
	}
}

func TestPassProducesFindings(t *testing.T) {
	url, _ := setup(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	slow := fingerprint.Of("SELECT id FROM tl_orders WHERE customer_id = $1")
	fast := fingerprint.Of("SELECT email FROM tl_customers WHERE id = $1")
	now := time.Now().Truncate(core.BucketSize)
	mk := func(hash string, n int, d time.Duration) core.Rollup {
		r := core.Rollup{Hash: hash, Bucket: now}
		for i := 0; i < n; i++ {
			r.Count++
			r.Total += d
			r.Hist.Record(d)
		}
		return r
	}
	if err := st.Save([]core.Fingerprint{slow, fast}, []core.Rollup{mk(slow.Hash, 50, 20*time.Millisecond), mk(fast.Hash, 500, 200*time.Microsecond)}); err != nil {
		t.Fatal(err)
	}
	// The fast lookup runs in N+1 loops after the slow query.
	st.AddBursts([]core.Bursts{{Hash: fast.Hash, ParentHash: slow.Hash, Runs: 25, Calls: 500, MaxRun: 20, LastSeen: time.Now()}})

	a := &Analyzer{Store: st, DatabaseURL: url}
	a.Window, a.Logf = time.Hour, t.Logf
	defer func() {
		if a.explainer != nil {
			a.explainer.Close(context.Background())
		}
	}()
	if err := a.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}

	sums, err := Summaries(st, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range sums {
		got[s.Detector] = s.Hash
	}
	if got["missing-index"] != slow.Hash || got["n-plus-one"] != fast.Hash {
		t.Fatalf("summaries = %+v", sums)
	}
	plans, _ := st.Plans()
	if plans[slow.Hash].Plan == nil || plans[fast.Hash].Plan == nil {
		t.Fatalf("plans not stored: %+v", plans)
	}
	// The index suggestion carries a what-if result (an estimate, or why
	// there is none), and it survives a reload from the store.
	for _, s := range sums {
		if s.Detector == "missing-index" && (s.Simulation == nil || (s.Simulation.Unavailable == "" && s.Simulation.Speedup < 2)) {
			t.Fatalf("missing-index simulation = %+v", s.Simulation)
		}
	}
}

// Generated fixes must be valid SQL even for awkward names: run one.
func TestFixWithQuotedIdentifiersRuns(t *testing.T) {
	url, db := setup(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `
		DROP TABLE IF EXISTS "Weird Orders";
		CREATE TABLE "Weird Orders" (id int PRIMARY KEY, "Customer ID" int, "user" int);
		INSERT INTO "Weird Orders" SELECT g, g % 500, g % 7 FROM generate_series(1, 30000) g;
		ANALYZE "Weird Orders";`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(context.Background(), `DROP TABLE IF EXISTS "Weird Orders"`) })

	x, err := Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close(ctx)
	plan, err := x.Explain(ctx, `SELECT id FROM "Weird Orders" WHERE "Customer ID" = $1 AND "user" = $2`)
	if err != nil {
		t.Fatal(err)
	}
	fs := detectors.Run(detectors.Input{Plan: plan, Stats: core.Stats{Count: 100}})
	if len(fs) == 0 || fs[0].Fix == "" {
		t.Fatalf("expected a fix, got %+v", fs)
	}
	t.Log(fs[0].Fix)
	if _, err := db.Exec(ctx, fs[0].Fix); err != nil {
		t.Fatalf("generated fix is not valid SQL: %v\n%s", err, fs[0].Fix)
	}
}
