// Package aggregate turns the raw event stream into per-fingerprint rollups.
//
// It runs in a single goroutine downstream of the proxy, so fingerprinting
// and bookkeeping never touch the application's hot path.
package aggregate

import (
	"sort"
	"time"

	"github.com/moseleydev/tracelet/core"
	"github.com/moseleydev/tracelet/core/fingerprint"
	"github.com/moseleydev/tracelet/core/sqlcomment"
)

// Saver persists fingerprints, rollups and burst patterns. *store.Store
// implements it.
type Saver interface {
	Save(fps []core.Fingerprint, rollups []core.Rollup) error
	AddBursts(deltas []core.Bursts) error
	SaveTraces(traces []core.Trace) error
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

	conns  map[uint64]*connState   // per-connection run tracking
	bursts map[string]*core.Bursts // bursts seen since the last flush

	traces map[string]*openTrace // requests still receiving queries
	closed []core.Trace          // finished requests waiting to be saved

	panics int // internal errors recovered by safeAdd
}

// openTrace collects the queries of one request until it goes quiet.
type openTrace struct {
	route     string
	spans     []rawSpan
	truncated bool
	lastSeen  time.Time
}

type rawSpan struct {
	hash  string
	start time.Time
	dur   time.Duration
	rows  int64
	err   bool
	conn  uint64
}

// connState follows the sequence of queries on one connection to spot the
// same query repeating back to back (an N+1 loop).
type connState struct {
	hash    string // fingerprint of the current run
	parent  string // fingerprint that ran just before the run started
	run     int64
	lastEnd time.Time
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

	// A burst is at least minBurst calls of one query in a row on one
	// connection, each starting within burstGap of the previous one ending.
	// The gap is tight on purpose: inside an N+1 loop the app only spends
	// microseconds between rows, while separate requests that happen to
	// reuse a pooled connection are usually much further apart.
	minBurst = 5
	burstGap = 10 * time.Millisecond

	connIdle = time.Minute // forget connections idle this long

	// A request is finished once no query has carried its trace id for
	// traceIdle. Very long or chatty requests are capped so one runaway
	// job can't exhaust memory.
	traceIdle     = 2 * time.Second
	maxTraceSpans = 1000
	maxOpenTraces = 10_000
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
		conns:         map[uint64]*connState{},
		bursts:        map[string]*core.Bursts{},
		traces:        map[string]*openTrace{},
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
func (a *Aggregator) Add(e core.Event) core.Fingerprint { return a.add(&e) }

// add records e, first filling its trace id and route from any sqlcommenter
// comment, so callers see the annotated event.
func (a *Aggregator) add(e *core.Event) core.Fingerprint {
	// Cache by the query without its comment: the comment carries a fresh
	// trace id on every request.
	fp := a.Fingerprint(sqlcomment.Annotate(e))
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
	if e.TraceID != "" {
		a.addToTrace(e, fp.Hash)
	} else {
		a.trackRun(*e, fp.Hash) // no request context: fall back to per-connection runs
	}
	return fp
}

func (a *Aggregator) addToTrace(e *core.Event, hash string) {
	t := a.traces[e.TraceID]
	if t == nil {
		if len(a.traces) >= maxOpenTraces {
			return
		}
		t = &openTrace{}
		a.traces[e.TraceID] = t
	}
	if t.route == "" {
		t.route = e.Route
	}
	t.lastSeen = a.now()
	if len(t.spans) >= maxTraceSpans {
		t.truncated = true
		return
	}
	t.spans = append(t.spans, rawSpan{hash: hash, start: e.StartedAt, dur: e.Duration, rows: e.Rows, err: e.Error != "", conn: e.ConnID})
}

// finishTrace turns collected queries into a Trace and looks for N+1 runs
// within the request.
func (a *Aggregator) finishTrace(id string, t *openTrace) core.Trace {
	sort.SliceStable(t.spans, func(i, j int) bool { return t.spans[i].start.Before(t.spans[j].start) })
	tr := core.Trace{ID: id, Route: t.route, Start: t.spans[0].start, Queries: len(t.spans), Truncated: t.truncated}
	var end time.Time
	for _, s := range t.spans {
		tr.Spans = append(tr.Spans, core.Span{Hash: s.hash, Offset: s.start.Sub(tr.Start), Duration: s.dur, Rows: s.rows, Error: s.err, ConnID: s.conn})
		tr.DBTime += s.dur
		if s.err {
			tr.Errors++
		}
		if e := s.start.Add(s.dur); e.After(end) {
			end = e
		}
	}
	tr.Duration = end.Sub(tr.Start)

	// N+1 within one request: the same query back to back, right after a
	// different one. Unlike per-connection runs, this works even when the
	// app's pool hands out a different connection for each query.
	for i := 0; i < len(t.spans); {
		j := i
		for j < len(t.spans) && t.spans[j].hash == t.spans[i].hash {
			j++
		}
		if i > 0 {
			last := t.spans[j-1]
			a.recordBurst(t.spans[i].hash, t.spans[i-1].hash, int64(j-i), last.start.Add(last.dur), t.route)
		}
		i = j
	}
	return tr
}

func (a *Aggregator) trackRun(e core.Event, hash string) {
	if e.ConnID == 0 {
		return
	}
	st := a.conns[e.ConnID]
	if st == nil {
		st = &connState{}
		a.conns[e.ConnID] = st
	}
	contiguous := !st.lastEnd.IsZero() && e.StartedAt.Sub(st.lastEnd) <= burstGap
	if contiguous && hash == st.hash {
		st.run++
	} else {
		a.endRun(st)
		st.parent = ""
		if contiguous {
			st.parent = st.hash
		}
		st.hash, st.run = hash, 1
	}
	st.lastEnd = e.StartedAt.Add(e.Duration)
}

// endRun records the connection's current run if it was a burst.
func (a *Aggregator) endRun(st *connState) {
	a.recordBurst(st.hash, st.parent, st.run, st.lastEnd, "")
	st.run = 0
}

// recordBurst counts a run of one query if it is long enough to be an N+1.
func (a *Aggregator) recordBurst(hash, parent string, run int64, end time.Time, route string) {
	if run < minBurst || parent == "" || parent == hash {
		return
	}
	b := a.bursts[hash]
	if b == nil {
		b = &core.Bursts{Hash: hash}
		a.bursts[hash] = b
	}
	b.ParentHash = parent
	b.Runs++
	b.Calls += run
	b.MaxRun = max(b.MaxRun, run)
	b.LastSeen = end
	if route != "" {
		b.Route = route
	}
}

// Flush saves every bucket that changed since the last flush and closes
// buckets that have ended. With final set, all buckets are closed.
func (a *Aggregator) Flush(final bool) error {
	now := a.now()

	// Finish requests that have gone quiet.
	for id, t := range a.traces {
		if final || now.Sub(t.lastSeen) > traceIdle {
			a.closed = append(a.closed, a.finishTrace(id, t))
			delete(a.traces, id)
		}
	}
	if len(a.closed) > 0 {
		if err := a.saver.SaveTraces(a.closed); err != nil {
			return err // keep them and retry next flush
		}
		a.closed = a.closed[:0]
	}

	// Close runs on connections that have gone quiet.
	for id, st := range a.conns {
		if final || now.Sub(st.lastEnd) > burstGap {
			a.endRun(st)
		}
		if final || now.Sub(st.lastEnd) > connIdle {
			delete(a.conns, id)
		}
	}
	if len(a.bursts) > 0 {
		deltas := make([]core.Bursts, 0, len(a.bursts))
		for _, b := range a.bursts {
			deltas = append(deltas, *b)
		}
		if err := a.saver.AddBursts(deltas); err != nil {
			return err // keep the deltas and retry next flush
		}
		clear(a.bursts)
	}

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
// safeAdd records one event. A bug triggered by an unusual query skips that
// event instead of crashing Tracelet, which would cut every application
// connection going through the proxy.
func (a *Aggregator) safeAdd(e *core.Event, onEvent func(core.Event, core.Fingerprint)) {
	defer func() {
		if r := recover(); r != nil {
			a.panics++
			if a.panics <= 3 {
				a.logf("skipped a query after an internal error (traffic is unaffected): %v", r)
			}
		}
	}()
	fp := a.add(e)
	if onEvent != nil {
		onEvent(*e, fp)
	}
}

// Source supplies events in batches. *proxy.Sink implements it.
type Source interface {
	// Batch returns the events emitted since the last call, reusing spare's
	// storage, waiting briefly when there are none. ok is false once the
	// source is closed and drained.
	Batch(spare []core.Event) (batch []core.Event, ok bool)
}

// Run consumes events until the source is closed, flushing every interval
// and once more at the end. onEvent, if set, sees each event and its
// fingerprint.
func (a *Aggregator) Run(src Source, interval time.Duration, onEvent func(core.Event, core.Fingerprint)) error {
	next := time.Now().Add(interval)
	var spare []core.Event
	for {
		batch, ok := src.Batch(spare)
		for i := range batch {
			a.safeAdd(&batch[i], onEvent)
		}
		clear(batch) // drop references to query strings before reuse
		spare = batch
		if !ok {
			return a.Flush(true)
		}
		if now := time.Now(); !now.Before(next) {
			if err := a.Flush(false); err != nil {
				a.logf("saving stats: %v", err)
			}
			next = now.Add(interval)
		}
	}
}
