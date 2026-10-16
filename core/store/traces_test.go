package store

import (
	"testing"
	"time"

	"github.com/moseleydev/tracelet/core"
)

func trace(id, route string, start time.Time, durs ...time.Duration) core.Trace {
	t := core.Trace{ID: id, Route: route, Start: start}
	var off time.Duration
	for _, d := range durs {
		t.Spans = append(t.Spans, core.Span{Hash: "h", Offset: off, Duration: d})
		t.DBTime += d
		off += d
	}
	t.Queries, t.Duration = len(durs), off
	return t
}

func TestSaveAndListTraces(t *testing.T) {
	s, _ := open(t)
	now := time.Now()
	ms := time.Millisecond
	err := s.SaveTraces([]core.Trace{
		trace("a", "GET /orders", now.Add(-3*time.Second), 10*ms, 10*ms),
		trace("b", "GET /orders", now.Add(-2*time.Second), 50*ms),
		trace("c", "GET /health", now.Add(-time.Second), ms),
		trace("d", "", now, ms, ms, ms),
	})
	if err != nil {
		t.Fatal(err)
	}

	recent, err := s.Traces(TraceQuery{Since: now.Add(-time.Hour)})
	if err != nil || len(recent) != 4 || recent[0].ID != "d" || recent[0].Spans != nil {
		t.Fatalf("recent = %+v, %v", recent, err)
	}
	slow, _ := s.Traces(TraceQuery{Since: now.Add(-time.Hour), Route: "GET /orders", RouteSet: true, Order: "slowest"})
	if len(slow) != 2 || slow[0].ID != "b" {
		t.Fatalf("slowest orders = %+v", slow)
	}
	noRoute, _ := s.Traces(TraceQuery{Since: now.Add(-time.Hour), RouteSet: true})
	if len(noRoute) != 1 || noRoute[0].ID != "d" {
		t.Fatalf("no-route = %+v", noRoute)
	}

	full, err := s.Trace("a")
	if err != nil || full == nil || len(full.Spans) != 2 || full.Spans[1].Offset != 10*ms || full.DBTime != 20*ms {
		t.Fatalf("trace a = %+v, %v", full, err)
	}
	if missing, err := s.Trace("nope"); missing != nil || err != nil {
		t.Fatalf("unknown trace = %+v, %v", missing, err)
	}

	routes, err := s.Routes(now.Add(-time.Hour))
	if err != nil || len(routes) != 3 {
		t.Fatalf("routes = %+v, %v", routes, err)
	}
	r := routes[0]
	if r.Route != "GET /orders" || r.Requests != 2 || r.AvgQueries != 1.5 || r.MaxQueries != 2 || r.DBTime != 70*ms || r.P95 != 50*ms {
		t.Fatalf("orders route = %+v", r)
	}
}

func TestSaveTracesMergesParts(t *testing.T) {
	s, _ := open(t)
	start := time.Now().Truncate(time.Millisecond)
	first := trace("x", "POST /import", start, 5*time.Millisecond)
	// The request went quiet past the idle timeout, then ran more queries.
	second := trace("x", "", start.Add(3*time.Second), 7*time.Millisecond)
	s.SaveTraces([]core.Trace{first})
	s.SaveTraces([]core.Trace{second})

	got, _ := s.Trace("x")
	if got.Queries != 2 || got.Route != "POST /import" || got.Spans[1].Offset != 3*time.Second ||
		got.Duration != 3*time.Second+7*time.Millisecond || got.DBTime != 12*time.Millisecond {
		t.Fatalf("merged = %+v", got)
	}
}

func TestOldTracesArePruned(t *testing.T) {
	s, _ := open(t)
	s.SaveTraces([]core.Trace{trace("old", "/x", time.Now().Add(-48*time.Hour), time.Millisecond)})
	s.SaveTraces([]core.Trace{trace("new", "/x", time.Now(), time.Millisecond)})
	if old, _ := s.Trace("old"); old != nil {
		t.Fatal("traces older than the retention window should be deleted")
	}
}
