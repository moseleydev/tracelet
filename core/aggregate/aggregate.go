// Package aggregate turns the raw event stream into per-fingerprint rollups.
//
// It runs in a single goroutine downstream of the proxy, so fingerprinting
// and bookkeeping never touch the application's hot path.
package aggregate

import (
	"time"

	"github.com/moseleydev/tracelet/core"
	"github.com/moseleydev/tracelet/core/fingerprint"
)

// Saver persists fingerprints and rollups. *store.Store implements it.
type Saver interface {
	Save(fps []core.Fingerprint, rollups []core.Rollup) error
}

// Aggregator groups events by fingerprint and time bucket. It is not safe for
// concurrent use; drive it from one goroutine, usually via Run.
type Aggregator struct {
	saver         Saver
	captureParams bool
	now           func() time.Time
	logf          func(format string, args ...any)

	cache  map[string]core.Fingerprint // raw query text -> fingerprint
	known  map[string]bool             // fingerprint hashes already saved
	newFps []core.Fingerprint

	open  map[key]*core.Rollup
	dirty map[key]bool
}

type key struct {
	hash   string
	bucket int64
}

const (
	// maxCache bounds the raw-query cache. Apps using prepared statements
	// repeat the exact same text, so hits are the norm; apps that inline
	// literals produce many distinct strings, so the cache resets when full.
	maxCache = 10_000

	// grace is how long a bucket stays open after it ends, for events still
	// in flight. Events are bucketed by completion time, so this can be short.
	grace = 5 * time.Second
)

// New returns an Aggregator that saves to saver. When captureParams is false,
// raw query text and parameters are never stored, only fingerprints.
func New(saver Saver, captureParams bool, logf func(string, ...any)) *Aggregator {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Aggregator{
		saver:         saver,
		captureParams: captureParams,
		now:           time.Now,
		logf:          logf,
		cache:         map[string]core.Fingerprint{},
		known:         map[string]bool{},
		open:          map[key]*core.Rollup{},
		dirty:         map[key]bool{},
	}
}

// Fingerprint returns the fingerprint of query, using the cache.
func (a *Aggregator) Fingerprint(query string) core.Fingerprint {
	if fp, ok := a.cache[query]; ok {
		return fp
	}
	fp := fingerprint.Of(query)
	if len(a.cache) >= maxCache {
		clear(a.cache)
	}
	a.cache[query] = fp
	return fp
}

// Add records one event and returns its fingerprint.
func (a *Aggregator) Add(e core.Event) core.Fingerprint {
	fp := a.Fingerprint(e.Query)
	if !a.known[fp.Hash] {
		a.known[fp.Hash] = true
		a.newFps = append(a.newFps, fp)
	}

	// Bucket by completion time: an event always lands in a current bucket,
	// however long the query ran.
	start := e.StartedAt.Add(e.Duration).Truncate(core.BucketSize)
	k := key{fp.Hash, start.Unix()}
	r := a.open[k]
	if r == nil {
		r = &core.Rollup{Hash: fp.Hash, Bucket: start.UTC()}
		a.open[k] = r
	}
	a.dirty[k] = true

	r.Count++
	r.Rows += e.Rows
	r.Total += e.Duration
	r.Hist.Record(e.Duration)
	if e.Error != "" {
		r.Errors++
		r.LastError = e.Error
	}
	if e.Duration >= r.SlowDuration {
		r.SlowDuration = e.Duration
		if a.captureParams {
			r.SlowQuery, r.SlowParams = e.Query, e.Params
		}
	}
	return fp
}

// Flush saves every bucket that changed since the last flush and closes
// buckets that have ended. With final set, all buckets are closed.
func (a *Aggregator) Flush(final bool) error {
	if len(a.dirty) == 0 && len(a.newFps) == 0 {
		a.evict(final)
		return nil
	}
	rollups := make([]core.Rollup, 0, len(a.dirty))
	for k := range a.dirty {
		rollups = append(rollups, *a.open[k])
	}
	if err := a.saver.Save(a.newFps, rollups); err != nil {
		return err // keep everything dirty and retry next flush
	}
	a.newFps = a.newFps[:0]
	clear(a.dirty)
	a.evict(final)
	return nil
}

func (a *Aggregator) evict(all bool) {
	cutoff := a.now().Add(-core.BucketSize - grace)
	for k, r := range a.open {
		if (all || r.Bucket.Before(cutoff)) && !a.dirty[k] {
			delete(a.open, k)
		}
	}
}

// Run consumes events until the channel closes, flushing every interval and
// once more at the end. onEvent, if set, sees each event and its fingerprint.
func (a *Aggregator) Run(events <-chan core.Event, interval time.Duration, onEvent func(core.Event, core.Fingerprint)) error {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case e, ok := <-events:
			if !ok {
				return a.Flush(true)
			}
			fp := a.Add(e)
			if onEvent != nil {
				onEvent(e, fp)
			}
		case <-tick.C:
			if err := a.Flush(false); err != nil {
				a.logf("saving stats: %v", err)
			}
		}
	}
}
