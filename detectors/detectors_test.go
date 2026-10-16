package detectors

import (
	"strings"
	"testing"
	"time"

	"github.com/moseleydev/tracelet/core"
)

func seqScan(table, filter string, rows float64, indexes ...Index) *Plan {
	return &Plan{Node: "Seq Scan", Relation: table, Schema: "public", Filter: filter, TableRows: rows, TotalCost: rows / 10, Indexes: indexes}
}

func stats(calls int64, p95 time.Duration, template string) core.Stats {
	return core.Stats{Count: calls, P95: p95, Fingerprint: core.Fingerprint{Template: template}}
}

var pkey = Index{Name: "pkey", Columns: []string{"id"}, Unique: true}

func only(t *testing.T, d Detector, in Input) Finding {
	t.Helper()
	fs := d.Detect(in)
	if len(fs) != 1 {
		t.Fatalf("%s: got %d findings, want 1: %+v", d.Name(), len(fs), fs)
	}
	return fs[0]
}

func none(t *testing.T, d Detector, in Input, why string) {
	t.Helper()
	if fs := d.Detect(in); len(fs) != 0 {
		t.Fatalf("%s (%s): unexpected findings %+v", d.Name(), why, fs)
	}
}

func TestMissingIndexWithSortKey(t *testing.T) {
	// SELECT ... FROM shop_orders WHERE customer_id = $1 ORDER BY created_at DESC LIMIT 20
	plan := &Plan{Node: "Limit", Children: []*Plan{{
		Node: "Sort", SortKey: []string{"shop_orders.created_at DESC"},
		Children: []*Plan{seqScan("shop_orders", "(shop_orders.customer_id = $1)", 300000, pkey)},
	}}}
	f := only(t, missingIndex{}, Input{Stats: stats(400, 40*time.Millisecond, ""), ShareOfTime: 0.5, Plan: plan})

	want := "CREATE INDEX CONCURRENTLY shop_orders_customer_id_created_at_idx ON public.shop_orders (customer_id, created_at DESC);"
	if f.Fix != want {
		t.Fatalf("fix\n got  %s\n want %s", f.Fix, want)
	}
	if f.Severity != Critical || !strings.Contains(f.Explanation, "~300,000 rows") || !strings.Contains(f.Explanation, "already sorted") {
		t.Fatalf("finding = %+v", f)
	}
	// The same index, in the form HypoPG can simulate.
	if f.Index == nil || !f.Index.Simulatable() ||
		f.Index.SQL("", false) != "CREATE INDEX ON public.shop_orders (customer_id, created_at DESC)" {
		t.Fatalf("index suggestion = %+v", f.Index)
	}
}

func TestMissingIndexEqualityThenRange(t *testing.T) {
	plan := seqScan("events", "((events.created_at >= $1) AND (events.user_id = $2))", 50000)
	f := only(t, missingIndex{}, Input{Stats: stats(50, 5*time.Millisecond, ""), Plan: plan})
	if !strings.Contains(f.Fix, "(user_id, created_at)") || f.Severity != Warning {
		t.Fatalf("finding = %+v", f)
	}
}

func TestMissingIndexStaysQuiet(t *testing.T) {
	d := missingIndex{}
	in := func(p *Plan, calls int64) Input { return Input{Stats: stats(calls, 0, ""), Plan: p} }
	none(t, d, in(seqScan("orders", "(orders.customer_id = $1)", 300000,
		Index{Name: "orders_customer_idx", Columns: []string{"customer_id"}}), 100), "index already exists")
	none(t, d, in(seqScan("tiny", "(tiny.code = $1)", 50), 100), "small table")
	none(t, d, in(seqScan("orders", "(orders.customer_id = $1)", 300000), 2), "rarely called")
	none(t, d, in(seqScan("orders", "((orders.a = $1) OR (orders.b = $2))", 300000), 100), "OR filter")
	none(t, d, in(seqScan("users", "(lower(users.email) = $1)", 300000), 100), "function on column")
	none(t, d, in(&Plan{Node: "Index Scan", Relation: "orders", Index: "orders_pkey", TableRows: 300000}, 100), "already uses an index")
	none(t, d, Input{Stats: stats(100, 0, "")}, "no plan")
}

func TestFullScanLeadingWildcard(t *testing.T) {
	plan := &Plan{Node: "Limit", Children: []*Plan{
		seqScan("shop_customers", "(shop_customers.email ~~ (('%'::text || $1) || '%'::text))", 20000, pkey),
	}}
	f := only(t, fullScan{}, Input{Stats: stats(30, 20*time.Millisecond, ""), Plan: plan})
	if !strings.Contains(f.Fix, "USING gin (email gin_trgm_ops)") || !strings.Contains(f.Fix, "CREATE EXTENSION IF NOT EXISTS pg_trgm") {
		t.Fatalf("fix = %s", f.Fix)
	}
	if f.Index == nil || f.Index.Simulatable() {
		t.Fatalf("GIN trigram indexes can't be simulated by HypoPG: %+v", f.Index)
	}
	// The missing-index detector must not recommend a useless btree here.
	none(t, missingIndex{}, Input{Stats: stats(30, 0, ""), Plan: plan}, "LIKE with leading wildcard")
}

func TestFullScanFunctionOnColumn(t *testing.T) {
	plan := seqScan("users", "(lower(users.email) = $1)", 80000)
	f := only(t, fullScan{}, Input{Stats: stats(30, 20*time.Millisecond, ""), Plan: plan})
	if f.Fix != "CREATE INDEX CONCURRENTLY users_lower_email_idx ON public.users ((lower(email)));" {
		t.Fatalf("fix = %s", f.Fix)
	}
	if f.Index == nil || !f.Index.Simulatable() {
		t.Fatalf("expression index should be simulatable: %+v", f.Index)
	}
}

func TestFullScanWholeTable(t *testing.T) {
	plan := &Plan{Node: "Aggregate", Children: []*Plan{seqScan("orders", "", 300000)}}
	f := only(t, fullScan{}, Input{Stats: stats(100, 80*time.Millisecond, ""), ShareOfTime: 0.1, Plan: plan})
	if f.Fix != "" || !strings.Contains(f.Explanation, "aggregates all ~300,000 rows") {
		t.Fatalf("finding = %+v", f)
	}
	none(t, fullScan{}, Input{Stats: stats(100, time.Millisecond, ""), ShareOfTime: 0.01, Plan: plan}, "cheap and rare enough")
	none(t, fullScan{}, Input{Stats: stats(100, time.Second, ""), Plan: seqScan("orders", "(orders.customer_id = $1)", 300000)},
		"indexable filter is missing-index's job")
}

func TestNPlusOne(t *testing.T) {
	parent := &core.Fingerprint{Query: "SELECT customer_id FROM shop_orders ORDER BY id DESC LIMIT ?"}
	in := Input{
		Stats:  stats(1200, time.Millisecond, "SELECT email FROM shop_customers WHERE id = $1"),
		Bursts: &core.Bursts{Runs: 60, Calls: 1200, MaxRun: 20},
		Parent: parent,
	}
	f := only(t, nPlusOne{}, in)
	if f.Severity != Critical || !strings.Contains(f.Title, "~20 times") || !strings.Contains(f.Explanation, parent.Query) {
		t.Fatalf("finding = %+v", f)
	}
	if !strings.HasPrefix(f.Fix, "SELECT email FROM shop_customers WHERE id = ANY($1)") {
		t.Fatalf("fix = %s", f.Fix)
	}

	in.Stats.Fingerprint.Template = "INSERT INTO items(order_id, sku) VALUES ($1, $2)"
	if f := only(t, nPlusOne{}, in); f.Fix != "" || !strings.Contains(f.Suggestion, "multi-row VALUES") {
		t.Fatalf("insert finding = %+v", f)
	}

	none(t, nPlusOne{}, Input{Stats: in.Stats, Bursts: &core.Bursts{Runs: 2, Calls: 40}}, "too few loops")
	none(t, nPlusOne{}, Input{Stats: in.Stats, Bursts: &core.Bursts{Runs: 10, Calls: 30}}, "loops too short")
	none(t, nPlusOne{}, Input{Stats: in.Stats}, "no bursts")
}

func TestConditions(t *testing.T) {
	got := conditions(`((o.customer_id = $1) AND ("O".status <> 'x'::text) AND (o.deleted_at IS NULL) AND (lower(o.email) = $2) AND (o.total > ANY ($3)))`)
	want := []cond{
		{column: "email", op: "=", fn: "lower"},
		{column: "customer_id", op: "="},
		{column: "status", op: "<>"},
		{column: "deleted_at", op: "IS NULL"},
		{column: "total", op: ">"},
	}
	if len(got) != len(want) {
		t.Fatalf("conditions = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cond %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestGeneratedSQLQuotesIdentifiers(t *testing.T) {
	plan := &Plan{Node: "Sort", SortKey: []string{`o."Created At" DESC`}, Children: []*Plan{{
		Node: "Seq Scan", Relation: "Order Items", Schema: "Sales", TableRows: 100000,
		Filter: `((o."Customer ID" = $1) AND (o."user" = $2))`,
	}}}
	f := only(t, missingIndex{}, Input{Stats: stats(100, 0, ""), Plan: plan})
	want := `CREATE INDEX CONCURRENTLY order_items_customer_id_user_created_at_idx ON "Sales"."Order Items" ("Customer ID", "user", "Created At" DESC);`
	if f.Fix != want {
		t.Fatalf("fix\n got  %s\n want %s", f.Fix, want)
	}

	for in, want := range map[string]string{
		"email": "email", "Email": `"Email"`, "order": `"order"`, "user": `"user"`, `we"ird`: `"we""ird"`, "a b": `"a b"`,
	} {
		if got := quoteIdent(in); got != want {
			t.Errorf("quoteIdent(%q) = %s, want %s", in, got, want)
		}
	}
}
