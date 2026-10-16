package aggregate

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/moseleydev/tracelet/core"
	"github.com/moseleydev/tracelet/core/fingerprint"
)

type fakeSaver struct {
	fps     []core.Fingerprint
	rollups map[string]core.Rollup // latest saved state per hash@bucket
	bursts  []core.Bursts
	traces  []core.Trace
	fail    bool
}

func (f *fakeSaver) SaveTraces(traces []core.Trace) error {
	if f.fail {
		return errors.New("disk full")
	}
	f.traces = append(f.traces, traces...)
	return nil
}

func (f *fakeSaver) AddBursts(deltas []core.Bursts) error {
	if f.fail {
		return errors.New("disk full")
	}
	f.bursts = append(f.bursts, deltas...)
	return nil
}

func (f *fakeSaver) Save(fps []core.Fingerprint, rs []core.Rollup) error {
	if f.fail {
		return errors.New("disk full")
	}
	f.fps = append(f.fps, fps...)
	if f.rollups == nil {
		f.rollups = map[string]core.Rollup{}
	}
	for _, r := range rs {
		f.rollups[r.Hash+"@"+r.Bucket.String()] = r
	}
	return nil
}

var t0 = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

func event(query string, at time.Time, d time.Duration) core.Event {
	return core.Event{Query: query, StartedAt: at, Duration: d, Rows: 1, Params: []string{"secret@example.com"}}
}

func TestGroupsByFingerprintAndBucket(t *testing.T) {
	saver := &fakeSaver{}
	a := New(saver, false, nil)
	a.now = func() time.Time { return t0 }

	a.Add(event("SELECT * FROM users WHERE id = 1", t0.Add(1*time.Second), 2*time.Millisecond))
	a.Add(event("SELECT * FROM users WHERE id = 2", t0.Add(2*time.Second), 4*time.Millisecond))
	a.Add(event("SELECT * FROM users WHERE id = 3", t0.Add(12*time.Second), 6*time.Millisecond)) // next bucket
	fp := a.Add(event("DELETE FROM sessions", t0.Add(3*time.Second), time.Millisecond))
	if fp.Query != "DELETE FROM sessions" || fp.Tables[0] != "sessions" {
		t.Fatalf("Add returned %+v", fp)
	}

	if err := a.Flush(false); err != nil {
		t.Fatal(err)
	}
	if len(saver.fps) != 2 {
		t.Fatalf("saved %d fingerprints, want 2", len(saver.fps))
	}
	if len(saver.rollups) != 3 {
		t.Fatalf("saved %d rollups, want 3", len(saver.rollups))
	}
	var first core.Rollup
	for _, r := range saver.rollups {
		if r.Bucket.Equal(t0) && r.Count == 2 {
			first = r
		}
	}
	if first.Total != 6*time.Millisecond || first.SlowDuration != 4*time.Millisecond {
		t.Fatalf("first bucket = %+v", first)
	}
}

func TestBucketsByCompletionTime(t *testing.T) {
	saver := &fakeSaver{}
	a := New(saver, false, nil)
	// Started in one bucket, finished 15s later in the next.
	a.Add(event("SELECT pg_sleep(15)", t0.Add(8*time.Second), 15*time.Second))
	a.Flush(true)
	for _, r := range saver.rollups {
		if !r.Bucket.Equal(t0.Add(20 * time.Second)) {
			t.Fatalf("bucket = %v, want completion bucket %v", r.Bucket, t0.Add(20*time.Second))
		}
	}
}

func TestRawQueryStoredOnlyWithCapture(t *testing.T) {
	for _, capture := range []bool{false, true} {
		saver := &fakeSaver{}
		a := New(saver, capture, nil)
		a.Add(event("SELECT * FROM users WHERE email = 'secret@example.com'", t0, time.Millisecond))
		a.Flush(true)
		for _, r := range saver.rollups {
			hasRaw := r.SlowQuery != "" || r.SlowParams != nil
			if hasRaw != capture {
				t.Fatalf("capture=%v: SlowQuery=%q SlowParams=%v", capture, r.SlowQuery, r.SlowParams)
			}
		}
		for _, fp := range saver.fps {
			if fp.Query != "SELECT * FROM users WHERE email = ?" {
				t.Fatalf("fingerprint leaked a literal: %q", fp.Query)
			}
		}
	}
}

func TestFlushOnlySavesChangesAndEvictsOldBuckets(t *testing.T) {
	saver := &fakeSaver{}
	a := New(saver, false, nil)
	now := t0
	a.now = func() time.Time { return now }

	a.Add(event("SELECT 1", t0, time.Millisecond))
	a.Flush(false)
	saver.rollups = nil

	a.Flush(false)
	if len(saver.rollups) != 0 {
		t.Fatal("an unchanged bucket should not be saved again")
	}
	if len(a.open) != 1 {
		t.Fatal("a current bucket should stay open")
	}

	now = t0.Add(core.BucketSize + grace + time.Second)
	a.Flush(false)
	if len(a.open) != 0 {
		t.Fatal("an ended bucket should be evicted")
	}
}

func TestFailedSaveIsRetried(t *testing.T) {
	saver := &fakeSaver{fail: true}
	a := New(saver, false, nil)
	a.Add(event("SELECT 1", t0, time.Millisecond))
	if err := a.Flush(true); err == nil {
		t.Fatal("expected save error")
	}
	saver.fail = false
	if err := a.Flush(true); err != nil {
		t.Fatal(err)
	}
	if len(saver.fps) != 1 || len(saver.rollups) != 1 {
		t.Fatalf("retry saved %d fps, %d rollups", len(saver.fps), len(saver.rollups))
	}
}

func TestRunDrainsAndFlushes(t *testing.T) {
	saver := &fakeSaver{}
	a := New(saver, false, nil)
	events := &sliceSource{}
	for i := 0; i < 3; i++ {
		events.pending = append(events.pending, event("SELECT 1", t0, time.Millisecond))
	}

	seen := 0
	if err := a.Run(events, time.Hour, func(core.Event, core.Fingerprint) { seen++ }); err != nil {
		t.Fatal(err)
	}
	if seen != 3 || len(saver.rollups) != 1 {
		t.Fatalf("seen %d events, saved %d rollups", seen, len(saver.rollups))
	}
	for _, r := range saver.rollups {
		if r.Count != 3 {
			t.Fatalf("count = %d", r.Count)
		}
	}
}

// loop simulates an app on one connection: a parent query, then n child
// queries back to back, each gap apart.
func loop(a *Aggregator, conn uint64, at time.Time, n int, gap time.Duration) time.Time {
	a.Add(core.Event{Query: "SELECT customer_id FROM orders LIMIT 20", StartedAt: at, Duration: time.Millisecond, ConnID: conn})
	at = at.Add(time.Millisecond)
	for i := 0; i < n; i++ {
		at = at.Add(gap)
		a.Add(core.Event{Query: fmt.Sprintf("SELECT email FROM customers WHERE id = %d", i), StartedAt: at, Duration: 200 * time.Microsecond, ConnID: conn})
		at = at.Add(200 * time.Microsecond)
	}
	return at
}

func TestDetectsNPlusOneBursts(t *testing.T) {
	saver := &fakeSaver{}
	a := New(saver, false, nil)
	at := t0
	for i := 0; i < 3; i++ {
		at = loop(a, 1, at, 20, 100*time.Microsecond)
		at = at.Add(time.Second) // next request
	}
	a.Flush(true)

	if len(saver.bursts) != 1 {
		t.Fatalf("bursts = %+v", saver.bursts)
	}
	b := saver.bursts[0]
	child := fingerprint.Of("SELECT email FROM customers WHERE id = 1").Hash
	parent := fingerprint.Of("SELECT customer_id FROM orders LIMIT 20").Hash
	if b.Hash != child || b.ParentHash != parent || b.Runs != 3 || b.Calls != 60 || b.MaxRun != 20 {
		t.Fatalf("burst = %+v", b)
	}
}

func TestIgnoresRepeatsThatAreNotBursts(t *testing.T) {
	cases := map[string]func(a *Aggregator){
		"separate requests far apart": func(a *Aggregator) {
			loop(a, 1, t0, 20, 40*time.Millisecond)
		},
		"short runs": func(a *Aggregator) {
			at := t0
			for i := 0; i < 5; i++ {
				at = loop(a, 1, at, minBurst-1, 100*time.Microsecond).Add(time.Second)
			}
		},
		"no parent query": func(a *Aggregator) {
			at := t0
			for i := 0; i < 20; i++ {
				a.Add(core.Event{Query: "SELECT 1", StartedAt: at, Duration: 100 * time.Microsecond, ConnID: 1})
				at = at.Add(200 * time.Microsecond)
			}
		},
	}
	for name, run := range cases {
		saver := &fakeSaver{}
		a := New(saver, false, nil)
		run(a)
		a.Flush(true)
		if len(saver.bursts) != 0 {
			t.Errorf("%s: unexpected bursts %+v", name, saver.bursts)
		}
	}
}

func TestBurstsAreTrackedPerConnection(t *testing.T) {
	saver := &fakeSaver{}
	a := New(saver, false, nil)
	// Two connections interleave their N+1 loops; each is still detected.
	at := t0
	for _, conn := range []uint64{1, 2} {
		a.Add(core.Event{Query: "SELECT id FROM posts LIMIT 10", StartedAt: at, Duration: time.Millisecond, ConnID: conn})
	}
	at = at.Add(time.Millisecond)
	for i := 0; i < 10; i++ {
		for _, conn := range []uint64{1, 2} {
			a.Add(core.Event{Query: "SELECT name FROM authors WHERE id = 1", StartedAt: at, Duration: 100 * time.Microsecond, ConnID: conn})
		}
		at = at.Add(300 * time.Microsecond)
	}
	a.Flush(true)
	if len(saver.bursts) != 1 || saver.bursts[0].Runs != 2 || saver.bursts[0].Calls != 20 {
		t.Fatalf("bursts = %+v", saver.bursts)
	}
}

// commented appends a sqlcommenter comment like the SDKs write.
func commented(q, trace, route string) string {
	return fmt.Sprintf("%s /*route='%s',traceparent='00-%s-00f067aa0ba902b7-01'*/", q, url.PathEscape(route), trace)
}

const traceA = "4bf92f3577b34da6a3ce929d0e0e4736"

func TestGroupsEventsIntoTraces(t *testing.T) {
	saver := &fakeSaver{}
	a := New(saver, false, nil)
	// Completion order differs from start order, and connections differ.
	a.Add(core.Event{Query: commented("SELECT * FROM items WHERE order_id = $1", traceA, "GET /orders/:id"), StartedAt: t0.Add(5 * time.Millisecond), Duration: 2 * time.Millisecond, Rows: 3, ConnID: 2})
	a.Add(core.Event{Query: commented("SELECT * FROM orders WHERE id = $1", traceA, "GET /orders/:id"), StartedAt: t0, Duration: 4 * time.Millisecond, Rows: 1, ConnID: 1})
	a.Add(core.Event{Query: commented("SELECT * FROM missing", traceA, "GET /orders/:id"), StartedAt: t0.Add(8 * time.Millisecond), Duration: time.Millisecond, Error: "boom", ConnID: 1})
	a.Add(core.Event{Query: "SELECT 1", StartedAt: t0, Duration: time.Millisecond, ConnID: 3}) // no request context
	a.Flush(true)

	if len(saver.traces) != 1 {
		t.Fatalf("traces = %+v", saver.traces)
	}
	tr := saver.traces[0]
	if tr.ID != traceA || tr.Route != "GET /orders/:id" || tr.Queries != 3 || tr.Errors != 1 ||
		!tr.Start.Equal(t0) || tr.Duration != 9*time.Millisecond || tr.DBTime != 7*time.Millisecond {
		t.Fatalf("trace = %+v", tr)
	}
	if tr.Spans[0].Hash != fingerprint.Of("SELECT * FROM orders WHERE id = $1").Hash || tr.Spans[1].Offset != 5*time.Millisecond || !tr.Spans[2].Error {
		t.Fatalf("spans out of order or wrong: %+v", tr.Spans)
	}
	// The comment is not part of the fingerprint, and the cache isn't
	// flooded by a new trace id per request.
	a.Add(core.Event{Query: commented("SELECT * FROM orders WHERE id = $1", "5bf92f3577b34da6a3ce929d0e0e4736", "GET /orders/:id"), StartedAt: t0, ConnID: 1})
	if len(a.cache) != 4 {
		t.Fatalf("cache has %d entries, want 4 (one per query shape)", len(a.cache))
	}
}

func TestTraceClosesWhenRequestGoesQuiet(t *testing.T) {
	saver := &fakeSaver{}
	a := New(saver, false, nil)
	now := t0
	a.now = func() time.Time { return now }
	a.Add(core.Event{Query: commented("SELECT 1", traceA, "/x"), StartedAt: t0, ConnID: 1})

	now = t0.Add(traceIdle / 2)
	a.Flush(false)
	if len(saver.traces) != 0 {
		t.Fatal("a request still within the idle window must stay open")
	}
	now = t0.Add(traceIdle + time.Second)
	a.Flush(false)
	if len(saver.traces) != 1 {
		t.Fatal("a quiet request should be saved")
	}
}

func TestNPlusOneWithinTraceAcrossConnections(t *testing.T) {
	saver := &fakeSaver{}
	a := New(saver, false, nil)
	for r := 0; r < 3; r++ {
		trace := fmt.Sprintf("%032x", r+1)
		at := t0.Add(time.Duration(r) * time.Second)
		a.Add(core.Event{Query: commented("SELECT id FROM posts LIMIT 10", trace, "GET /feed"), StartedAt: at, Duration: time.Millisecond, ConnID: 1})
		for i := 0; i < 10; i++ {
			at = at.Add(2 * time.Millisecond)
			// A pool that hands out a different connection every time.
			a.Add(core.Event{Query: commented(fmt.Sprintf("SELECT name FROM authors WHERE id = %d", i), trace, "GET /feed"),
				StartedAt: at, Duration: 100 * time.Microsecond, ConnID: uint64(1 + i%4)})
		}
	}
	a.Flush(true)
	if len(saver.bursts) != 1 {
		t.Fatalf("bursts = %+v", saver.bursts)
	}
	b := saver.bursts[0]
	if b.Runs != 3 || b.Calls != 30 || b.Route != "GET /feed" || b.ParentHash != fingerprint.Of("SELECT id FROM posts LIMIT 10").Hash {
		t.Fatalf("burst = %+v", b)
	}
}

// sliceSource delivers its events in one batch, then reports closed.
type sliceSource struct{ pending []core.Event }

func (s *sliceSource) Batch(spare []core.Event) ([]core.Event, bool) {
	if len(s.pending) == 0 {
		return spare[:0], false
	}
	b := append(spare[:0], s.pending...)
	s.pending = nil
	return b, true
}

type panicSource struct{ sent bool }

func (p *panicSource) Batch(spare []core.Event) ([]core.Event, bool) {
	if p.sent {
		return nil, false
	}
	p.sent = true
	return []core.Event{{Query: "SELECT * FROM a", StartedAt: t0}, {Query: "SELECT * FROM b", StartedAt: t0}}, true
}

func TestRunSurvivesInternalErrors(t *testing.T) {
	saver := &fakeSaver{}
	var logged []string
	a := New(saver, false, func(f string, args ...any) { logged = append(logged, fmt.Sprintf(f, args...)) })
	seen := 0
	err := a.Run(&panicSource{}, time.Hour, func(e core.Event, _ core.Fingerprint) {
		seen++
		if e.Query == "SELECT * FROM a" {
			panic("bug in a consumer")
		}
	})
	if err != nil || seen != 2 || len(logged) != 1 || len(saver.rollups) != 2 {
		t.Fatalf("err %v, seen %d, logged %q, rollups %d", err, seen, logged, len(saver.rollups))
	}
}

func TestRedactValues(t *testing.T) {
	for in, want := range map[string]string{
		`invalid input syntax for type integer: "abc123" (SQLSTATE 22P02)`: `invalid input syntax for type integer: "?" (SQLSTATE 22P02)`,
		`relation "users" does not exist (SQLSTATE 42P01)`:                 `relation "users" does not exist (SQLSTATE 42P01)`,
		`invalid input value for enum mood: "secret"`:                      `invalid input value for enum mood: "?"`,
		`syntax error at or near 'a@b.com'`:                                `syntax error at or near '?'`,
		`column "email" of relation "users" does not exist`:                `column "email" of relation "users" does not exist`,
		`unterminated: "x`: `unterminated: "?`,
	} {
		if got := redactValues(in); got != want {
			t.Errorf("redactValues(%q)\n got  %q\n want %q", in, got, want)
		}
	}
}

func TestStoredErrorsAreRedactedUnlessCapturing(t *testing.T) {
	for _, capture := range []bool{false, true} {
		saver := &fakeSaver{}
		a := New(saver, capture, nil)
		a.Add(core.Event{Query: "SELECT * FROM t WHERE id = $1", StartedAt: t0, Error: `invalid input syntax for type integer: "4111-1111" (SQLSTATE 22P02)`})
		a.Flush(true)
		for _, r := range saver.rollups {
			if leaked := strings.Contains(r.LastError, "4111"); leaked != capture {
				t.Fatalf("capture=%v: last error %q", capture, r.LastError)
			}
		}
	}
}
