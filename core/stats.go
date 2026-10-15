package core

import "time"

// Fingerprint is a normalized query shape with literals replaced by
// placeholders, e.g. "SELECT * FROM users WHERE email = $1". All events with
// the same fingerprint are grouped together.
type Fingerprint struct {
	Hash   string   `json:"hash"`
	Query  string   `json:"query"`
	Tables []string `json:"tables"`
}

// Stats is the aggregate view of one fingerprint over a time window.
type Stats struct {
	Fingerprint Fingerprint   `json:"fingerprint"`
	Count       int64         `json:"count"`
	Total       time.Duration `json:"total"` // Count × mean: the main ranking signal
	P50         time.Duration `json:"p50"`
	P95         time.Duration `json:"p95"`
	P99         time.Duration `json:"p99"`
	MeanRows    float64       `json:"mean_rows"`
	FirstSeen   time.Time     `json:"first_seen"`
	LastSeen    time.Time     `json:"last_seen"`
}
