// Package core holds the types shared by every part of Tracelet: the events
// the proxy captures, and the aggregated stats the analyzer and studio read.
package core

import "time"

// Event is one database operation observed by the proxy.
//
// Events are produced on the hot path, so they carry raw facts only.
// Fingerprinting, aggregation and analysis happen later, off the hot path.
type Event struct {
	ID        string        `json:"id"`
	Engine    Engine        `json:"engine"`
	Query     string        `json:"query"`
	Params    []string      `json:"params,omitempty"` // redacted unless explicitly enabled
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration"`
	Rows      int64         `json:"rows"`
	Error     string        `json:"error,omitempty"`
	ConnID    uint64        `json:"conn_id"`

	// Context propagated from the application, e.g. via sqlcommenter
	// comments (/* traceparent='...',route='/search' */). Empty when the
	// app has no Tracelet SDK.
	TraceID string `json:"trace_id,omitempty"`
	Route   string `json:"route,omitempty"`
}

// Engine identifies the database protocol an event came from.
type Engine string

const (
	Postgres Engine = "postgres"
	MySQL    Engine = "mysql"
	MongoDB  Engine = "mongodb"
	Redis    Engine = "redis"
)
