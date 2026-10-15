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
