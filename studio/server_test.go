package studio

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moseleydev/tracelet/core"
	"github.com/moseleydev/tracelet/core/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	now := time.Now().Truncate(core.BucketSize)
	r := core.Rollup{Hash: "abc", Bucket: now}
	for _, d := range []time.Duration{2 * time.Millisecond, 4 * time.Millisecond} {
		r.Count++
		r.Total += d
		r.Hist.Record(d)
	}
	err = st.Save(
		[]core.Fingerprint{{Hash: "abc", Query: "SELECT * FROM users WHERE id = ?", Tables: []string{"users"}}},
		[]core.Rollup{r},
	)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func getJSON(t *testing.T, srv *httptest.Server, path string, v any) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s", path, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
}

func TestAPI(t *testing.T) {
	srv := httptest.NewServer(New(testStore(t), nil, Info{Version: "test", Data: "t.db"}))
	defer srv.Close()

	var top []core.Stats
	getJSON(t, srv, "/api/top?since=15m", &top)
	if len(top) != 1 || top[0].Count != 2 || top[0].Fingerprint.Tables[0] != "users" {
		t.Fatalf("top = %+v", top)
	}

	var tables []store.TableStats
	getJSON(t, srv, "/api/tables?since=0", &tables)
	if len(tables) != 1 || tables[0].Table != "users" {
		t.Fatalf("tables = %+v", tables)
	}

	var series []SeriesPoint
	getJSON(t, srv, "/api/series?hash=abc", &series)
	if len(series) != 1 || series[0].Count != 2 || series[0].Mean != 3000 || series[0].Max != 4000 {
		t.Fatalf("series = %+v", series)
	}

	var info struct {
		Version string `json:"version"`
		Live    bool   `json:"live"`
	}
	getJSON(t, srv, "/api/info", &info)
	if info.Version != "test" || info.Live {
		t.Fatalf("info = %+v", info)
	}

	for _, bad := range []string{"/api/top?since=yesterday", "/api/series"} {
		resp, err := http.Get(srv.URL + bad)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s: %s, want 400", bad, resp.Status)
		}
	}
}

func TestEmptyListsAreArrays(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := httptest.NewServer(New(st, nil, Info{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/top")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := bufio.NewReader(resp.Body).ReadString('\n')
	if strings.TrimSpace(body) != "[]" {
		t.Fatalf("empty top = %q, want []", body)
	}
}

func TestLiveStream(t *testing.T) {
	hub := NewHub()
	stop := make(chan struct{})
	defer close(stop)
	go hub.Run(stop)

	srv := httptest.NewServer(New(testStore(t), hub, Info{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/live")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	r := bufio.NewReader(resp.Body)
	r.ReadString('\n') // ": connected"

	e := core.Event{
		Query:     "SELECT * FROM users WHERE email = 'secret@example.com'",
		StartedAt: time.Now(),
		Duration:  1500 * time.Microsecond,
		Rows:      1,
		Error:     "boom",
	}
	fp := core.Fingerprint{Hash: "abc", Query: "SELECT * FROM users WHERE email = ?", Tables: []string{"users"}}

	deadline := time.After(3 * time.Second)
	for {
		hub.Publish(e, fp)
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var batch liveBatch
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &batch); err != nil {
			t.Fatal(err)
		}
		if len(batch.Events) == 0 {
			select {
			case <-deadline:
				t.Fatal("no events received")
			default:
				continue
			}
		}
		got := batch.Events[0]
		if got.Hash != "abc" || got.Duration != 1500 || !got.Error || got.Tables[0] != "users" {
			t.Fatalf("event = %+v", got)
		}
		if strings.Contains(line, "secret@example.com") {
			t.Fatal("raw query literals must never reach the browser")
		}
		if batch.Total < len(batch.Events) {
			t.Fatalf("total %d < events %d", batch.Total, len(batch.Events))
		}
		return
	}
}

func TestLiveOfflineWithoutHub(t *testing.T) {
	srv := httptest.NewServer(New(testStore(t), nil, Info{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/live")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, _ := bufio.NewReader(resp.Body).ReadString('\n')
	if line != "event: offline\n" {
		t.Fatalf("first line = %q", line)
	}
}

func TestHubSamplesHeavyTraffic(t *testing.T) {
	hub := NewHub()
	ch := hub.subscribe()
	for i := 0; i < maxBatchEvents*3; i++ {
		hub.Publish(core.Event{StartedAt: time.Now()}, core.Fingerprint{Hash: "x"})
	}
	hub.flush()
	var batch liveBatch
	json.Unmarshal(<-ch, &batch)
	if len(batch.Events) != maxBatchEvents || batch.Total != maxBatchEvents*3 {
		t.Fatalf("events = %d, total = %d", len(batch.Events), batch.Total)
	}
}
