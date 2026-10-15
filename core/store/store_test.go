package store

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/moseleydev/tracelet/core"
)

var t0 = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

func rollup(hash string, bucket time.Time, ds ...time.Duration) core.Rollup {
	r := core.Rollup{Hash: hash, Bucket: bucket}
	for _, d := range ds {
		r.Count++
		r.Total += d
		r.Rows++
		r.Hist.Record(d)
		if d > r.SlowDuration {
			r.SlowDuration = d
		}
	}
	return r
}

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested", "tracelet.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestTopRanksByTotalTimeAcrossBuckets(t *testing.T) {
	s, _ := open(t)
	users := core.Fingerprint{Hash: "u", Query: "SELECT * FROM users WHERE id = ?", Tables: []string{"users"}}
	search := core.Fingerprint{Hash: "s", Query: "SELECT * FROM orders WHERE email LIKE ?", Tables: []string{"orders"}}

	// users: many fast calls (total 300ms). search: one slow call (150ms).
	var fast []time.Duration
	for i := 0; i < 100; i++ {
		fast = append(fast, 3*time.Millisecond)
	}
	err := s.Save([]core.Fingerprint{users, search}, []core.Rollup{
		rollup("u", t0, fast[:50]...),
		rollup("u", t0.Add(core.BucketSize), fast[50:]...),
		rollup("s", t0, 150*time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}

	top, err := s.Top(t0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top[0].Fingerprint.Hash != "u" || top[1].Fingerprint.Hash != "s" {
		t.Fatalf("ranking = %+v", top)
	}
	if top[0].Count != 100 || top[0].Total != 300*time.Millisecond || top[0].Fingerprint.Query != users.Query {
		t.Fatalf("users stats = %+v", top[0])
	}
	if !reflect.DeepEqual(top[1].Fingerprint.Tables, []string{"orders"}) {
		t.Fatalf("tables = %v", top[1].Fingerprint.Tables)
	}

	// The window excludes older buckets.
	recent, _ := s.Top(t0.Add(core.BucketSize), 10)
	if len(recent) != 1 || recent[0].Count != 50 {
		t.Fatalf("windowed top = %+v", recent)
	}
}

func TestSaveReplacesBucketState(t *testing.T) {
	s, _ := open(t)
	fp := core.Fingerprint{Hash: "h", Query: "SELECT ?"}
	s.Save([]core.Fingerprint{fp}, []core.Rollup{rollup("h", t0, time.Millisecond)})
	// A later flush of the same bucket carries its full, updated state.
	s.Save([]core.Fingerprint{fp}, []core.Rollup{rollup("h", t0, time.Millisecond, 2*time.Millisecond)})

	series, err := s.Series("h", t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || series[0].Count != 2 || series[0].Hist.N != 2 {
		t.Fatalf("series = %+v", series)
	}
}

func TestSeriesAndRoundTrip(t *testing.T) {
	s, path := open(t)
	r := rollup("h", t0.Add(core.BucketSize), 5*time.Millisecond)
	r.Errors, r.LastError = 1, "boom"
	r.SlowQuery, r.SlowParams = "SELECT 1", []string{"a", "NULL"}
	s.Save([]core.Fingerprint{{Hash: "h", Query: "SELECT ?"}}, []core.Rollup{r, rollup("h", t0, time.Millisecond)})
	s.Close()

	// Data survives reopening the file.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	series, err := s2.Series("h", t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 || !series[0].Bucket.Equal(t0) {
		t.Fatalf("series should be oldest first: %+v", series)
	}
	got := series[1]
	if got.LastError != "boom" || got.SlowQuery != "SELECT 1" || !reflect.DeepEqual(got.SlowParams, []string{"a", "NULL"}) {
		t.Fatalf("round trip lost data: %+v", got)
	}
	if got.Hist.Percentile(50) != r.Hist.Percentile(50) || got.SlowDuration != 5*time.Millisecond {
		t.Fatalf("histogram or slow duration changed: %+v", got)
	}
	if series[0].SlowParams != nil {
		t.Fatalf("empty params should decode as nil, got %#v", series[0].SlowParams)
	}
}

func TestTables(t *testing.T) {
	s, _ := open(t)
	s.Save([]core.Fingerprint{
		{Hash: "a", Query: "q1", Tables: []string{"users"}},
		{Hash: "b", Query: "q2", Tables: []string{"orders", "users"}},
		{Hash: "c", Query: "q3", Tables: []string{"orders"}},
	}, []core.Rollup{
		rollup("a", t0, 10*time.Millisecond),
		rollup("b", t0, 40*time.Millisecond),
		rollup("c", t0, 5*time.Millisecond, 5*time.Millisecond),
	})

	tables, err := s.Tables(t0)
	if err != nil {
		t.Fatal(err)
	}
	want := []TableStats{
		{Table: "users", Fingerprints: []string{"b", "a"}, Count: 2, Total: 50 * time.Millisecond},
		{Table: "orders", Fingerprints: []string{"b", "c"}, Count: 3, Total: 50 * time.Millisecond},
	}
	// users and orders tie on total; ties sort by name.
	want[0], want[1] = want[1], want[0]
	if !reflect.DeepEqual(tables, want) {
		t.Fatalf("tables =\n %+v\nwant\n %+v", tables, want)
	}
}
