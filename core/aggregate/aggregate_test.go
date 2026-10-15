package aggregate

import (
	"errors"
	"testing"
	"time"

	"github.com/moseleydev/tracelet/core"
)

type fakeSaver struct {
	fps     []core.Fingerprint
	rollups map[string]core.Rollup // latest saved state per hash@bucket
	fail    bool
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
	events := make(chan core.Event, 3)
	for i := 0; i < 3; i++ {
		events <- event("SELECT 1", t0, time.Millisecond)
	}
	close(events)

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
