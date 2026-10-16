package session

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moseleydev/tracelet/core"
)

// Recorder starts and stops recording for a running proxy. The proxy reads
// Flag on its hot path, and Record is called with each finished query.
type Recorder struct {
	// Flag is shared with the proxy: while true it captures replay detail.
	Flag atomic.Bool

	Dir      string // where new sessions are written
	Upstream string // host:port/db, for the session header
	Version  string
	Limit    int64 // bytes per session; <= 0 uses DefaultLimit

	mu   sync.Mutex
	w    atomic.Pointer[Writer]
	last *Stats
}

// Status is the recorder's state.
type Status struct {
	Recording bool   `json:"recording"`
	Current   *Stats `json:"current,omitempty"`
	Last      *Stats `json:"last,omitempty"` // the most recently finished session
}

// ErrRecording is returned by Start while a session is already running.
var ErrRecording = errors.New("already recording")

// Start begins a new session. With an empty path it is written to Dir with a
// timestamped name.
func (r *Recorder) Start(path string) (Stats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.w.Load() != nil {
		return Stats{}, ErrRecording
	}
	if path == "" {
		if err := os.MkdirAll(r.Dir, 0o700); err != nil {
			return Stats{}, err
		}
		path = filepath.Join(r.Dir, time.Now().Format("2006-01-02T15-04-05")+".trc.gz")
	}
	w, err := Create(path, Header{Tracelet: r.Version, Upstream: r.Upstream}, r.Limit)
	if err != nil {
		return Stats{}, err
	}
	r.w.Store(w)
	r.Flag.Store(true)
	return w.Stats(), nil
}

// Stop ends the current session and finishes its file.
func (r *Recorder) Stop() (Stats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Flag.Store(false)
	w := r.w.Swap(nil)
	if w == nil {
		return Stats{}, errors.New("not recording")
	}
	err := w.Close()
	st := w.Stats()
	r.last = &st
	return st, err
}

// Record adds a finished query to the current session, if any.
func (r *Recorder) Record(e core.Event, hash string) {
	if w := r.w.Load(); w != nil {
		w.Record(e, hash)
	}
}

// Status reports whether a session is running and how it is going.
func (r *Recorder) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Status{Last: r.last}
	if w := r.w.Load(); w != nil {
		st := w.Stats()
		s.Recording, s.Current = true, &st
	}
	return s
}

// Info describes a session file on disk.
type Info struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Size     int64     `json:"size"` // compressed bytes
	Modified time.Time `json:"modified"`
}

// List returns the sessions in Dir, newest first.
func (r *Recorder) List() ([]Info, error) {
	entries, err := os.ReadDir(r.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".trc.gz") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Info{Name: e.Name(), Path: filepath.Join(r.Dir, e.Name()), Size: fi.Size(), Modified: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}
