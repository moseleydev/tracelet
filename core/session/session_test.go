package session

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/moseleydev/tracelet/core"
)

func TestWriteAndReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.trc.gz")
	start := time.Now()
	w, err := Create(path, Header{Tracelet: "test", Upstream: "db:5432/app", StartedAt: start}, 0)
	if err != nil {
		t.Fatal(err)
	}
	replay := &core.Replay{Query: "SELECT * FROM t WHERE id = $1", ParamOIDs: []uint32{23}, ParamFormats: []int16{1},
		Params: [][]byte{{0, 0, 0, 7}, nil}, ResultFormats: []int16{1}}
	w.Record(core.Event{StartedAt: start.Add(1500 * time.Microsecond), Duration: 2 * time.Millisecond, ConnID: 3, Rows: 1,
		TraceID: "abc", Route: "GET /t/:id", Replay: replay}, "h1")
	w.Record(core.Event{StartedAt: start.Add(5 * time.Millisecond), Error: "boom"}, "h2")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if st := w.Stats(); st.Entries != 2 || st.Dropped != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("session file mode %v, want 0600 (it contains query values)", fi.Mode().Perm())
	}

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Header.Upstream != "db:5432/app" || r.Header.Format != FormatVersion {
		t.Fatalf("header = %+v", r.Header)
	}
	e, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if e.Offset != 1500 || e.Duration != 2000 || e.Conn != 3 || e.Hash != "h1" || e.Route != "GET /t/:id" || !reflect.DeepEqual(e.Replay, replay) {
		t.Fatalf("entry = %+v (replay %+v)", e, e.Replay)
	}
	if e, err = r.Next(); err != nil || e.Error != "boom" || e.Replay != nil {
		t.Fatalf("second entry = %+v, %v", e, err)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("end = %v, want EOF", err)
	}
}

func TestSizeLimitStopsRecording(t *testing.T) {
	w, err := Create(filepath.Join(t.TempDir(), "s.trc.gz"), Header{}, 300)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		w.Record(core.Event{StartedAt: time.Now(), Replay: &core.Replay{Query: "SELECT * FROM some_table WHERE id = $1"}}, "h")
	}
	w.Close()
	if st := w.Stats(); !st.Full || st.Dropped == 0 || st.Entries == 50 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestTruncatedFileEndsCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.trc.gz")
	w, _ := Create(path, Header{}, 0)
	for i := 0; i < 1000; i++ {
		w.Record(core.Event{StartedAt: time.Now()}, "h")
	}
	w.Close()
	raw, _ := os.ReadFile(path)
	os.WriteFile(path, raw[:len(raw)*2/3], 0o600) // as if the process died mid-write

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	n := 0
	for {
		_, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("after %d entries: %v", n, err)
		}
		n++
	}
	if n == 0 || n >= 1000 {
		t.Fatalf("read %d entries from a truncated file", n)
	}
}

func TestOpenRejectsOtherFiles(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.txt")
	os.WriteFile(plain, []byte("hello"), 0o600)
	if _, err := Open(plain); err == nil {
		t.Fatal("a non-gzip file should be rejected")
	}
	gz := filepath.Join(dir, "other.gz")
	f, _ := os.Create(gz)
	zw := gzip.NewWriter(f)
	zw.Write([]byte(`{"something":"else"}` + "\n"))
	zw.Close()
	f.Close()
	if _, err := Open(gz); err == nil {
		t.Fatal("a gzip file without a session header should be rejected")
	}
}

func TestRecorder(t *testing.T) {
	r := &Recorder{Dir: filepath.Join(t.TempDir(), "sessions"), Upstream: "db/app"}
	r.Record(core.Event{}, "ignored") // not recording: no-op
	if r.Flag.Load() || r.Status().Recording {
		t.Fatal("recorder should start idle")
	}
	if _, err := r.Start(""); err != nil {
		t.Fatal(err)
	}
	if !r.Flag.Load() {
		t.Fatal("Start must turn on capture in the proxy")
	}
	if _, err := r.Start(""); err != ErrRecording {
		t.Fatalf("second Start = %v, want ErrRecording", err)
	}
	r.Record(core.Event{StartedAt: time.Now()}, "h")
	st, err := r.Stop()
	if err != nil || st.Entries != 1 || r.Flag.Load() {
		t.Fatalf("stop: %+v, %v, flag %v", st, err, r.Flag.Load())
	}
	list, err := r.List()
	if err != nil || len(list) != 1 || list[0].Size == 0 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if fi, _ := os.Stat(r.Dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("sessions dir mode %v", fi.Mode().Perm())
	}
	if s := r.Status(); s.Recording || s.Last == nil || s.Last.Entries != 1 {
		t.Fatalf("status after stop = %+v", s)
	}
}
