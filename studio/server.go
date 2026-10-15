// Package studio serves Tracelet Studio: the web UI (built from this
// directory with Vite and embedded into the binary), a JSON API over the
// stats store, and a live stream of queries.
package studio

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/moseleydev/tracelet/core/store"
)

//go:embed all:dist
var distFS embed.FS

// Info describes the running Tracelet instance to the UI.
type Info struct {
	Version       string    `json:"version"`
	Upstream      string    `json:"upstream,omitempty"` // empty when only viewing saved data
	Proxy         string    `json:"proxy,omitempty"`
	Data          string    `json:"data"`
	CaptureParams bool      `json:"capture_params"`
	StartedAt     time.Time `json:"started_at"`
}

// Server is the studio HTTP handler.
type Server struct {
	store *store.Store
	hub   *Hub // nil when there is no live proxy
	info  Info
	mux   *http.ServeMux
}

// New returns a studio Server. hub may be nil for read-only viewing.
func New(st *store.Store, hub *Hub, info Info) *Server {
	s := &Server{store: st, hub: hub, info: info, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/info", s.handleInfo)
	s.mux.HandleFunc("GET /api/top", s.handleTop)
	s.mux.HandleFunc("GET /api/tables", s.handleTables)
	s.mux.HandleFunc("GET /api/series", s.handleSeries)
	s.mux.HandleFunc("GET /api/live", s.handleLive)
	s.mux.Handle("/", uiHandler())
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	info := s.info
	writeJSON(w, struct {
		Info
		Live bool `json:"live"`
	}{info, s.hub != nil})
}

func (s *Server) handleTop(w http.ResponseWriter, r *http.Request) {
	since, err := parseSince(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	top, err := s.store.Top(since, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, nonNil(top))
}

func (s *Server) handleTables(w http.ResponseWriter, r *http.Request) {
	since, err := parseSince(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tables, err := s.store.Tables(since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, nonNil(tables))
}

// SeriesPoint is one time bucket of a query's latency chart. Durations are
// in microseconds.
type SeriesPoint struct {
	At     int64  `json:"at"` // bucket start, unix milliseconds
	Count  int64  `json:"count"`
	Errors int64  `json:"errors"`
	Mean   int64  `json:"mean"`
	P50    int64  `json:"p50"`
	P95    int64  `json:"p95"`
	Max    int64  `json:"max"`
	Error  string `json:"error,omitempty"`
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	hash := r.URL.Query().Get("hash")
	if hash == "" {
		http.Error(w, "hash is required", http.StatusBadRequest)
		return
	}
	since, err := parseSince(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rollups, err := s.store.Series(hash, since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	points := make([]SeriesPoint, 0, len(rollups))
	for _, ru := range rollups {
		p := SeriesPoint{
			At:     ru.Bucket.UnixMilli(),
			Count:  ru.Count,
			Errors: ru.Errors,
			P50:    ru.Hist.Percentile(50).Microseconds(),
			P95:    ru.Hist.Percentile(95).Microseconds(),
			Max:    ru.Hist.Max.Microseconds(),
			Error:  ru.LastError,
		}
		if ru.Count > 0 {
			p.Mean = (ru.Total / time.Duration(ru.Count)).Microseconds()
		}
		points = append(points, p)
	}
	writeJSON(w, points)
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if s.hub == nil {
		// Viewing saved data: tell the browser there is no live feed.
		fmt.Fprint(w, "event: offline\ndata: {}\n\n")
		flusher.Flush()
		return
	}

	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

// parseSince reads ?since=15m (a duration back from now) or ?since=0 for
// all data. The default is one hour.
func parseSince(r *http.Request) (time.Time, error) {
	v := r.URL.Query().Get("since")
	if v == "" {
		v = "1h"
	}
	if v == "0" {
		return time.Time{}, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return time.Time{}, fmt.Errorf("invalid since %q: use a duration like 15m, or 0", v)
	}
	return time.Now().Add(-d), nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// uiHandler serves the built single-page app, or a short notice when the
// binary was built without it.
func uiHandler() http.Handler {
	dist, _ := fs.Sub(distFS, "dist")
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintln(w, "Tracelet Studio was not built into this binary.")
			fmt.Fprintln(w, "Run \"make build\" (needs Node 20+) or use \"make studio-dev\" for development.")
		})
	}
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hashed assets can be cached forever; index.html never.
		if len(r.URL.Path) > len("/assets/") && r.URL.Path[:len("/assets/")] == "/assets/" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
