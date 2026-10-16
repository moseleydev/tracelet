package core

import (
	"sort"
	"time"
)

// Fingerprint is a normalized query shape with literals replaced by
// placeholders, e.g. "SELECT * FROM users WHERE email = ?". All events with
// the same fingerprint are grouped together.
type Fingerprint struct {
	Hash   string   `json:"hash"`
	Query  string   `json:"query"`
	Tables []string `json:"tables"`
	// Template is the query with literals replaced by $n parameters, ready
	// to prepare and EXPLAIN. It never contains values. Empty when the query
	// cannot be prepared (e.g. several statements).
	Template string `json:"-"`
}

// Rollup aggregates every event of one fingerprint that completed within one
// time bucket. Rollups are what Tracelet stores; Stats are computed from them.
type Rollup struct {
	Hash   string        `json:"hash"`
	Bucket time.Time     `json:"bucket"` // start of the bucket
	Count  int64         `json:"count"`
	Errors int64         `json:"errors"`
	Rows   int64         `json:"rows"`
	Total  time.Duration `json:"total"`
	Hist   Histogram     `json:"-"`

	// The slowest event in the bucket. SlowQuery and SlowParams hold the raw
	// query and parameter values only when parameter capture is enabled,
	// since literals in raw SQL can contain personal data.
	SlowDuration time.Duration `json:"slow_duration"`
	SlowQuery    string        `json:"slow_query,omitempty"`
	SlowParams   []string      `json:"slow_params,omitempty"`
	LastError    string        `json:"last_error,omitempty"`
}

// Stats is the aggregate view of one fingerprint over a time window.
type Stats struct {
	Fingerprint Fingerprint   `json:"fingerprint"`
	Count       int64         `json:"count"`
	Errors      int64         `json:"errors"`
	Total       time.Duration `json:"total"` // time spent in this query overall: the main ranking signal
	Mean        time.Duration `json:"mean"`
	P50         time.Duration `json:"p50"`
	P95         time.Duration `json:"p95"`
	P99         time.Duration `json:"p99"`
	Max         time.Duration `json:"max"`
	MeanRows    float64       `json:"mean_rows"`
	FirstSeen   time.Time     `json:"first_seen"`
	LastSeen    time.Time     `json:"last_seen"`
	LastError   string        `json:"last_error,omitempty"`
}

// Summarize combines a fingerprint's rollups into Stats. bucketSize is used
// to compute LastSeen as the end of the latest bucket.
func Summarize(fp Fingerprint, rollups []Rollup, bucketSize time.Duration) Stats {
	s := Stats{Fingerprint: fp}
	if len(rollups) == 0 {
		return s
	}
	sorted := append([]Rollup(nil), rollups...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Bucket.Before(sorted[j].Bucket) })

	var h Histogram
	var rows int64
	for _, r := range sorted {
		s.Count += r.Count
		s.Errors += r.Errors
		s.Total += r.Total
		rows += r.Rows
		h.Merge(&r.Hist)
		if r.LastError != "" {
			s.LastError = r.LastError
		}
	}
	s.FirstSeen = sorted[0].Bucket
	s.LastSeen = sorted[len(sorted)-1].Bucket.Add(bucketSize)
	if s.Count > 0 {
		s.Mean = s.Total / time.Duration(s.Count)
		s.MeanRows = float64(rows) / float64(s.Count)
	}
	s.P50, s.P95, s.P99 = h.Percentile(50), h.Percentile(95), h.Percentile(99)
	s.Max = h.Max
	return s
}

// BucketSize is the width of each rollup time bucket. Ten seconds keeps
// storage small while still giving the studio smooth latency-over-time
// charts.
const BucketSize = 10 * time.Second

// Bursts records one fingerprint running many times in a row on the same
// connection: the signature of an N+1 loop, where the app runs one query per
// row returned by a previous ("parent") query.
type Bursts struct {
	Hash       string    `json:"hash"`
	ParentHash string    `json:"parent_hash"` // the query that ran just before the latest burst
	Runs       int64     `json:"runs"`        // number of bursts seen
	Calls      int64     `json:"calls"`       // calls made inside bursts
	MaxRun     int64     `json:"max_run"`     // longest burst
	LastSeen   time.Time `json:"last_seen"`
	Route      string    `json:"route,omitempty"` // request route of the latest burst, when known
}

// Trace is one application request: every query that carried the same
// trace id, in the order they started.
type Trace struct {
	ID        string        `json:"id"`
	Route     string        `json:"route"`
	Start     time.Time     `json:"start"`
	Duration  time.Duration `json:"duration"` // first query start to last query end
	Queries   int           `json:"queries"`
	DBTime    time.Duration `json:"db_time"` // sum of query durations
	Errors    int           `json:"errors"`
	Truncated bool          `json:"truncated,omitempty"` // more queries than Tracelet keeps per trace
	Spans     []Span        `json:"spans,omitempty"`
}

// Span is one query within a trace.
type Span struct {
	Hash     string        `json:"h"`
	Offset   time.Duration `json:"o"` // start, relative to the trace start
	Duration time.Duration `json:"d"`
	Rows     int64         `json:"r"`
	Error    bool          `json:"e,omitempty"`
	ConnID   uint64        `json:"c,omitempty"`
}
