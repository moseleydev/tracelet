package replay

import (
	"sort"
	"sync"
	"time"

	"github.com/moseleydev/tracelet/core"
	"github.com/moseleydev/tracelet/core/fingerprint"
	"github.com/moseleydev/tracelet/core/session"
)

// accumulator collects per-query timings from many streams at once.
type accumulator struct {
	mu      sync.Mutex
	queries map[string]*queryAcc
	sent    int64
	errors  int64
	skipped map[string]int
}

type queryAcc struct {
	result   *QueryResult
	recorded core.Histogram
	replayed core.Histogram
}

func newAccumulator(recorded map[string][]session.Entry) *accumulator {
	a := &accumulator{queries: map[string]*queryAcc{}, skipped: map[string]int{}}
	for hash, entries := range recorded {
		q := &queryAcc{result: &QueryResult{Hash: hash}}
		for _, e := range entries {
			d := time.Duration(e.Duration) * time.Microsecond
			q.recorded.Record(d)
			q.result.Recorded.Count++
			q.result.Recorded.Total += d
			if q.result.Query == "" && e.Replay != nil {
				q.result.Query = fingerprint.Of(e.Replay.Query).Query // shape only, no values
			}
		}
		a.queries[hash] = q
	}
	return a
}

func (a *accumulator) get(hash string) *queryAcc {
	q := a.queries[hash]
	if q == nil {
		q = &queryAcc{result: &QueryResult{Hash: hash}}
		a.queries[hash] = q
	}
	return q
}

func (a *accumulator) add(e session.Entry, d time.Duration, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	q := a.get(e.Hash)
	a.sent++
	q.replayed.Record(d)
	q.result.Replayed.Count++
	q.result.Replayed.Total += d
	if err != nil {
		a.errors++
		q.result.Errors++
		q.result.LastError = err.Error()
	}
}

func (a *accumulator) skip(e session.Entry, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	q := a.get(e.Hash)
	if q.result.Skipped == nil {
		q.result.Skipped = map[string]int{}
	}
	q.result.Skipped[reason]++
	a.skipped[reason]++
}

func (a *accumulator) results() ([]*QueryResult, int64, int64, map[string]int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*QueryResult, 0, len(a.queries))
	for _, q := range a.queries {
		q.result.Recorded.P50, q.result.Recorded.P95 = q.recorded.Percentile(50), q.recorded.Percentile(95)
		q.result.Replayed.P50, q.result.Replayed.P95 = q.replayed.Percentile(50), q.replayed.Percentile(95)
		out = append(out, q.result)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Recorded.Total != out[j].Recorded.Total {
			return out[i].Recorded.Total > out[j].Recorded.Total
		}
		return out[i].Hash < out[j].Hash
	})
	return out, a.sent, a.errors, a.skipped
}
