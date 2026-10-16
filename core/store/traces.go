package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/moseleydev/tracelet/core"
)

const (
	traceRetention = 24 * time.Hour
	maxTraces      = 20_000
)

// SaveTraces stores finished requests. A request that paused longer than
// the aggregator's idle timeout arrives in parts; parts with the same id are
// merged. Old traces are pruned so the file stays small.
func (s *Store) SaveTraces(traces []core.Trace) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, t := range traces {
		var (
			start     int64
			spans     string
			truncated bool
			route     string
		)
		err := tx.QueryRow(`SELECT started_at, spans, truncated, route FROM traces WHERE id = ?`, t.ID).
			Scan(&start, &spans, &truncated, &route)
		switch {
		case err == nil:
			var old []core.Span
			json.Unmarshal([]byte(spans), &old)
			t = mergeTraces(core.Trace{ID: t.ID, Route: route, Start: time.Unix(0, start), Truncated: truncated, Spans: old}, t)
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		raw, _ := json.Marshal(t.Spans)
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO traces (id, route, started_at, duration_ns, queries, db_ns, errors, truncated, spans)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.Route, t.Start.UnixNano(), int64(t.Duration), t.Queries, int64(t.DBTime), t.Errors, t.Truncated, string(raw),
		); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`DELETE FROM traces WHERE started_at < ?`, time.Now().Add(-traceRetention).UnixNano()); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM traces WHERE id IN (
		SELECT id FROM traces ORDER BY started_at DESC LIMIT -1 OFFSET ?)`, maxTraces); err != nil {
		return err
	}
	return tx.Commit()
}

// mergeTraces combines two parts of one request, recomputing the summary.
func mergeTraces(a, b core.Trace) core.Trace {
	start := a.Start
	if b.Start.Before(start) {
		start = b.Start
	}
	out := core.Trace{ID: a.ID, Route: a.Route, Start: start, Truncated: a.Truncated || b.Truncated}
	if out.Route == "" {
		out.Route = b.Route
	}
	for _, part := range []core.Trace{a, b} {
		shift := part.Start.Sub(start)
		for _, sp := range part.Spans {
			sp.Offset += shift
			out.Spans = append(out.Spans, sp)
		}
	}
	sort.SliceStable(out.Spans, func(i, j int) bool { return out.Spans[i].Offset < out.Spans[j].Offset })
	for _, sp := range out.Spans {
		out.DBTime += sp.Duration
		if sp.Error {
			out.Errors++
		}
		out.Duration = max(out.Duration, sp.Offset+sp.Duration)
	}
	out.Queries = len(out.Spans)
	return out
}

// RouteStats summarizes the requests to one route.
type RouteStats struct {
	Route      string        `json:"route"` // "" for requests without a route
	Requests   int           `json:"requests"`
	Queries    int           `json:"queries"`     // across all requests
	AvgQueries float64       `json:"avg_queries"` // per request
	MaxQueries int           `json:"max_queries"`
	DBTime     time.Duration `json:"db_time"` // across all requests
	P50        time.Duration `json:"p50"`     // database time per request
	P95        time.Duration `json:"p95"`
	Errors     int           `json:"errors"`
}

// Routes returns per-route request stats since the given time, highest
// total database time first.
func (s *Store) Routes(since time.Time) ([]RouteStats, error) {
	rows, err := s.db.Query(`SELECT route, queries, db_ns, errors FROM traces WHERE started_at >= ?`, since.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byRoute := map[string]*RouteStats{}
	perRequest := map[string][]time.Duration{}
	for rows.Next() {
		var route string
		var queries, errs int
		var db int64
		if err := rows.Scan(&route, &queries, &db, &errs); err != nil {
			return nil, err
		}
		r := byRoute[route]
		if r == nil {
			r = &RouteStats{Route: route}
			byRoute[route] = r
		}
		r.Requests++
		r.Queries += queries
		r.MaxQueries = max(r.MaxQueries, queries)
		r.DBTime += time.Duration(db)
		r.Errors += errs
		perRequest[route] = append(perRequest[route], time.Duration(db))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]RouteStats, 0, len(byRoute))
	for route, r := range byRoute {
		ds := perRequest[route]
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		r.P50, r.P95 = percentile(ds, 50), percentile(ds, 95)
		r.AvgQueries = float64(r.Queries) / float64(r.Requests)
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DBTime != out[j].DBTime {
			return out[i].DBTime > out[j].DBTime
		}
		return out[i].Route < out[j].Route
	})
	return out, nil
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted))*p/100+0.5) - 1
	return sorted[min(max(i, 0), len(sorted)-1)]
}

// TraceQuery selects traces for listing.
type TraceQuery struct {
	Since time.Time
	// Route filters to one route when RouteSet is true ("" selects
	// requests that had no route).
	Route    string
	RouteSet bool
	// Order is "recent" (default), "slowest" (database time) or "queries".
	Order string
	Limit int
}

// Traces lists traces without their spans.
func (s *Store) Traces(q TraceQuery) ([]core.Trace, error) {
	where, args := "started_at >= ?", []any{q.Since.UnixNano()}
	if q.RouteSet {
		where += " AND route = ?"
		args = append(args, q.Route)
	}
	order := "started_at DESC"
	switch q.Order {
	case "slowest":
		order = "db_ns DESC"
	case "queries":
		order = "queries DESC, db_ns DESC"
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, route, started_at, duration_ns, queries, db_ns, errors, truncated
		FROM traces WHERE `+where+` ORDER BY `+order+` LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Trace
	for rows.Next() {
		t, err := scanTrace(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Trace returns one trace with its spans, or nil if it is unknown.
func (s *Store) Trace(id string) (*core.Trace, error) {
	row := s.db.QueryRow(`SELECT id, route, started_at, duration_ns, queries, db_ns, errors, truncated, spans
		FROM traces WHERE id = ?`, id)
	t, err := scanTrace(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func scanTrace(r interface{ Scan(...any) error }, withSpans bool) (core.Trace, error) {
	var (
		t            core.Trace
		start, d, db int64
		spans        string
		dest         = []any{&t.ID, &t.Route, &start, &d, &t.Queries, &db, &t.Errors, &t.Truncated}
	)
	if withSpans {
		dest = append(dest, &spans)
	}
	if err := r.Scan(dest...); err != nil {
		return t, err
	}
	t.Start, t.Duration, t.DBTime = time.Unix(0, start), time.Duration(d), time.Duration(db)
	if withSpans {
		if err := json.Unmarshal([]byte(spans), &t.Spans); err != nil {
			return t, err
		}
	}
	return t, nil
}
