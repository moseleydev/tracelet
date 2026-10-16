package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/moseleydev/tracelet/core"
	"github.com/moseleydev/tracelet/detectors"
)

func TestUpgradesVersion1File(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	// A data file as written by Tracelet before schema version 2.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema + `
		INSERT INTO fingerprints (hash, query, tables, first_seen) VALUES ('h', 'SELECT ?', '[]', 1);
		PRAGMA user_version = 1;`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fps, err := s.Fingerprints()
	if err != nil || fps["h"].Query != "SELECT ?" {
		t.Fatalf("old data lost: %v %+v", err, fps)
	}

	// The upgraded file accepts templates, and fills one in for an old row.
	if err := s.Save([]core.Fingerprint{{Hash: "h", Query: "SELECT ?", Template: "SELECT $1"}}, nil); err != nil {
		t.Fatal(err)
	}
	fps, _ = s.Fingerprints()
	if fps["h"].Template != "SELECT $1" {
		t.Fatalf("template = %q", fps["h"].Template)
	}

	var version int
	s.db.QueryRow("PRAGMA user_version").Scan(&version)
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
}

func TestPlansRoundTrip(t *testing.T) {
	s, _ := open(t)
	plan := &detectors.Plan{Node: "Limit", Children: []*detectors.Plan{{
		Node: "Seq Scan", Relation: "orders", Schema: "public", Filter: "(customer_id = $1)",
		TableRows: 300000, Indexes: []detectors.Index{{Name: "orders_pkey", Columns: []string{"id"}, Unique: true}},
	}}}
	at := time.Now().Truncate(time.Millisecond)
	if err := s.SavePlan(PlanRecord{Hash: "a", ExplainedAt: at, Plan: plan}); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePlan(PlanRecord{Hash: "b", ExplainedAt: at, Error: "syntax error"}); err != nil {
		t.Fatal(err)
	}
	plans, err := s.Plans()
	if err != nil {
		t.Fatal(err)
	}
	got := plans["a"].Plan
	if got == nil || got.Children[0].Indexes[0].Name != "orders_pkey" || got.Children[0].TableRows != 300000 {
		t.Fatalf("plan = %+v", got)
	}
	if !plans["a"].ExplainedAt.Equal(at) || plans["b"].Plan != nil || plans["b"].Error != "syntax error" {
		t.Fatalf("records = %+v", plans)
	}
}

func TestReplaceFindings(t *testing.T) {
	s, _ := open(t)
	s.ReplaceFindings("a", []detectors.Finding{
		{Detector: "missing-index", Severity: detectors.Critical, Title: "Missing index",
			Index:      &detectors.IndexSuggestion{Table: "public.orders", Columns: []string{"customer_id"}},
			Simulation: &detectors.Simulation{CostBefore: 3559, CostAfter: 16.5, UsesIndex: true, Speedup: 215.7}},
		{Detector: "n-plus-one", Severity: detectors.Warning, Title: "N+1"},
	})
	s.ReplaceFindings("b", []detectors.Finding{{Detector: "full-scan", Severity: detectors.Info, Title: "Scan"}})

	all, err := s.Findings()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Severity != detectors.Critical || all[2].Severity != detectors.Info {
		t.Fatalf("findings = %+v", all)
	}
	if ix, sim := all[0].Index, all[0].Simulation; ix == nil || ix.Columns[0] != "customer_id" || sim == nil || sim.Speedup != 215.7 || !sim.UsesIndex {
		t.Fatalf("index/simulation lost: %+v %+v", ix, sim)
	}
	if all[1].Index != nil || all[1].Simulation != nil {
		t.Fatalf("findings without an index should have none: %+v", all[1])
	}

	// Fixed: the index was added, so the finding goes away.
	s.ReplaceFindings("a", nil)
	all, _ = s.Findings()
	if len(all) != 1 || all[0].Hash != "b" {
		t.Fatalf("after fix = %+v", all)
	}
}

func TestBurstsAccumulate(t *testing.T) {
	s, _ := open(t)
	t1 := time.Now().Add(-time.Minute)
	t2 := time.Now()
	s.AddBursts([]core.Bursts{{Hash: "child", ParentHash: "p1", Runs: 2, Calls: 40, MaxRun: 20, LastSeen: t1}})
	s.AddBursts([]core.Bursts{{Hash: "child", ParentHash: "p2", Runs: 1, Calls: 30, MaxRun: 30, LastSeen: t2}})

	got, err := s.Bursts(t1.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	b := got["child"]
	if b.Runs != 3 || b.Calls != 70 || b.MaxRun != 30 || b.ParentHash != "p2" || !b.LastSeen.Equal(t2) {
		t.Fatalf("bursts = %+v", b)
	}
	if old, _ := s.Bursts(t2.Add(time.Second)); len(old) != 0 {
		t.Fatal("bursts outside the window should be excluded")
	}
}
