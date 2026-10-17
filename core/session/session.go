// Package session reads and writes recorded traffic (.trc.gz files): every
// query an application sent, with enough of its wire-protocol detail to
// replay it exactly.
//
// The format is gzip-compressed JSON Lines: one header line, then one line
// per query, in completion order. It is meant to be inspectable with
// standard tools (zcat file.trc.gz | head).
//
// Unlike Tracelet's stats, session files contain real query text and
// parameter values. They are written readable by their owner only.
package session

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moseleydev/tracelet/core"
)

// FormatVersion is written in every header and checked on read.
const FormatVersion = 1

// Header is the first line of a session file.
type Header struct {
	Format    int       `json:"tracelet_session"`
	Tracelet  string    `json:"tracelet"`
	StartedAt time.Time `json:"started_at"`
	// Upstream is the recorded database as host:port/dbname, without
	// credentials. Replay uses it to refuse writing back to the source.
	Upstream string `json:"upstream"`
}

// Entry is one recorded query.
type Entry struct {
	Offset   int64  `json:"t"` // start, microseconds since the session started
	Duration int64  `json:"d"` // microseconds
	Conn     uint64 `json:"c"`
	Rows     int64  `json:"r"`
	Error    string `json:"e,omitempty"`
	Hash     string `json:"h"` // fingerprint
	TraceID  string `json:"tr,omitempty"`
	Route    string `json:"rt,omitempty"`
	// Replay is the protocol detail needed to send the query again. Nil
	// for queries that cannot be replayed.
	Replay *core.Replay `json:"x,omitempty"`
}

// Writer records entries to a session file from its own goroutine, so a
// slow disk never slows the caller: when its buffer is full, entries are
// dropped and counted.
type Writer struct {
	path    string
	start   time.Time
	ch      chan Entry
	done    chan error
	limit   int64 // stop after this many uncompressed bytes
	written atomic.Int64
	entries atomic.Int64
	dropped atomic.Int64
	full    atomic.Bool
	once    sync.Once
}

// DefaultLimit caps a session at 1 GiB of uncompressed JSON (roughly
// 100–200 MB on disk).
const DefaultLimit = 1 << 30

// Create starts a session file at path. limit <= 0 uses DefaultLimit.
func Create(path string, h Header, limit int64) (*Writer, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	h.Format = FormatVersion
	if h.StartedAt.IsZero() {
		h.StartedAt = time.Now()
	}
	w := &Writer{path: path, start: h.StartedAt, ch: make(chan Entry, 1<<16), done: make(chan error, 1), limit: limit}
	go w.loop(f, h)
	return w, nil
}

func (w *Writer) loop(f *os.File, h Header) {
	bw := bufio.NewWriterSize(f, 256<<10)
	gz := gzip.NewWriter(bw)
	enc := json.NewEncoder(&countingWriter{w: gz, n: &w.written})
	err := enc.Encode(h)
	for e := range w.ch {
		if err != nil {
			continue // keep draining so Record never blocks
		}
		if w.written.Load() >= w.limit {
			w.full.Store(true)
			w.dropped.Add(1)
			continue
		}
		if err = enc.Encode(e); err == nil {
			w.entries.Add(1)
		}
	}
	err = errors.Join(err, gz.Close(), bw.Flush(), f.Close())
	w.done <- err
}

// Record queues one completed query. It never blocks.
func (w *Writer) Record(e core.Event, hash string) {
	entry := Entry{
		Offset:   e.StartedAt.Sub(w.start).Microseconds(),
		Duration: e.Duration.Microseconds(),
		Conn:     e.ConnID,
		Rows:     e.Rows,
		Error:    e.Error,
		Hash:     hash,
		TraceID:  e.TraceID,
		Route:    e.Route,
		Replay:   e.Replay,
	}
	select {
	case w.ch <- entry:
	default:
		w.dropped.Add(1)
	}
}

// Close finishes the file. Entries queued before Close are written.
func (w *Writer) Close() error {
	var err error
	w.once.Do(func() {
		close(w.ch)
		err = <-w.done
	})
	return err
}

// Stats describes a session in progress.
type Stats struct {
	Path    string    `json:"path"`
	Started time.Time `json:"started"`
	Entries int64     `json:"entries"`
	Dropped int64     `json:"dropped"`
	Bytes   int64     `json:"bytes"` // uncompressed
	Full    bool      `json:"full"`  // reached its size limit
}

// Stats reports progress so far.
func (w *Writer) Stats() Stats {
	return Stats{Path: w.path, Started: w.start, Entries: w.entries.Load(), Dropped: w.dropped.Load(), Bytes: w.written.Load(), Full: w.full.Load()}
}

type countingWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// Reader reads a session file.
type Reader struct {
	Header Header
	f      *os.File
	gz     *gzip.Reader
	dec    *json.Decoder
}

// Open opens a session file and reads its header.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(bufio.NewReaderSize(f, 256<<10))
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s is not a Tracelet session: %w", path, err)
	}
	r := &Reader{f: f, gz: gz, dec: json.NewDecoder(gz)}
	if err := r.dec.Decode(&r.Header); err != nil || r.Header.Format == 0 {
		r.Close()
		return nil, fmt.Errorf("%s is not a Tracelet session", path)
	}
	if r.Header.Format > FormatVersion {
		r.Close()
		return nil, fmt.Errorf("%s was recorded by a newer Tracelet (format %d)", path, r.Header.Format)
	}
	return r, nil
}

// Next returns the next entry, or io.EOF at the end. A session cut short
// (for example by a crash) ends cleanly at its last complete entry.
func (r *Reader) Next() (Entry, error) {
	var e Entry
	err := r.dec.Decode(&e)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = io.EOF
	}
	return e, err
}

// Close closes the file.
func (r *Reader) Close() error {
	r.gz.Close()
	return r.f.Close()
}
