package postgres

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moseleydev/tracelet/core"
)

// tracker follows one connection's protocol state and turns request/response
// pairs into events.
//
// PostgreSQL answers requests strictly in order, so the tracker keeps a FIFO
// of in-flight operations: the frontend goroutine appends to it and the
// backend goroutine completes operations from the front.
type tracker struct {
	s      *Server
	connID uint64

	// Owned by the frontend goroutine.
	stmts   map[string]string // prepared statement name -> query
	portals map[string]portal

	mu      sync.Mutex
	pending []*op
}

type portal struct {
	query  string
	params []string
}

type opKind uint8

const (
	opSimple  opKind = iota // 'Q': completes at ReadyForQuery
	opExecute               // 'E': completes at CommandComplete/Error/...
	opSync                  // 'S': marks the end of an extended-protocol batch
)

type op struct {
	kind   opKind
	query  string
	params []string
	start  time.Time
	rows   int64
	err    string
}

func newTracker(s *Server, connID uint64) *tracker {
	return &tracker{
		s:       s,
		connID:  connID,
		stmts:   map[string]string{},
		portals: map[string]portal{},
	}
}

// frontend inspects a message sent by the application.
func (t *tracker) frontend(typ byte, body []byte) {
	switch typ {
	case 'Q': // Query
		q, _ := cstring(body)
		t.push(&op{kind: opSimple, query: q, start: time.Now()})

	case 'P': // Parse
		name, rest := cstring(body)
		q, _ := cstring(rest)
		t.stmts[name] = q

	case 'B': // Bind
		name, rest := cstring(body)
		stmt, rest := cstring(rest)
		var params []string
		if t.s.cfg.CaptureParams {
			params = bindParams(rest)
		}
		t.portals[name] = portal{query: t.stmts[stmt], params: params}

	case 'E': // Execute
		name, _ := cstring(body)
		p := t.portals[name]
		t.push(&op{kind: opExecute, query: p.query, params: p.params, start: time.Now()})

	case 'S': // Sync
		t.push(&op{kind: opSync})

	case 'C': // Close
		if len(body) < 1 {
			return
		}
		name, _ := cstring(body[1:])
		if body[0] == 'S' {
			delete(t.stmts, name)
		} else {
			delete(t.portals, name)
		}
	}
}

// backend inspects a message sent by the database.
func (t *tracker) backend(typ byte, body []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var head *op
	if len(t.pending) > 0 {
		head = t.pending[0]
	}

	switch typ {
	case 'C': // CommandComplete
		tag, _ := cstring(body)
		rows := rowsFromTag(tag)
		if head == nil {
			return
		}
		switch head.kind {
		case opSimple:
			head.rows += rows // a simple query may hold several statements
		case opExecute:
			head.rows = rows
			t.complete()
		}

	case 'I', 's': // EmptyQueryResponse, PortalSuspended
		if head != nil && head.kind == opExecute {
			t.complete()
		}

	case 'E': // ErrorResponse
		if head == nil {
			return
		}
		msg := errorMessage(body)
		switch head.kind {
		case opSimple:
			if head.err == "" {
				head.err = msg
			}
		case opExecute:
			head.err = msg
			t.complete()
		}

	case 'Z': // ReadyForQuery
		if head != nil && head.kind == opSimple {
			t.complete()
			return
		}
		// End of an extended batch. Executes still queued before the Sync
		// were skipped by the server after an error, so they never ran.
		for len(t.pending) > 0 {
			o := t.pending[0]
			t.pending = t.pending[1:]
			if o.kind == opSync {
				break
			}
		}
	}
}

func (t *tracker) push(o *op) {
	t.mu.Lock()
	t.pending = append(t.pending, o)
	t.mu.Unlock()
}

// complete pops the head operation and emits it. Caller holds t.mu.
func (t *tracker) complete() {
	o := t.pending[0]
	t.pending[0] = nil
	t.pending = t.pending[1:]

	t.s.cfg.Sink.Emit(core.Event{
		ID:        strconv.FormatUint(t.s.eventSeq.Add(1), 10),
		Engine:    core.Postgres,
		Query:     o.query,
		Params:    o.params,
		StartedAt: o.start,
		Duration:  time.Since(o.start),
		Rows:      o.rows,
		Error:     o.err,
		ConnID:    t.connID,
	})
}

// cstring splits a NUL-terminated string off the front of b. The result is
// copied, so it stays valid after b is reused.
func cstring(b []byte) (string, []byte) {
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return string(b), nil
	}
	return string(b[:i]), b[i+1:]
}

// rowsFromTag extracts the row count from a CommandComplete tag such as
// "SELECT 5", "UPDATE 3" or "INSERT 0 1".
func rowsFromTag(tag string) int64 {
	i := strings.LastIndexByte(tag, ' ')
	if i < 0 {
		return 0
	}
	switch tag[:strings.IndexByte(tag, ' ')] {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "MERGE", "FETCH", "MOVE", "COPY":
		n, _ := strconv.ParseInt(tag[i+1:], 10, 64)
		return n
	}
	return 0
}

// errorMessage formats an ErrorResponse body as "message (SQLSTATE code)".
func errorMessage(body []byte) string {
	var msg, code string
	for len(body) > 0 && body[0] != 0 {
		field := body[0]
		var v string
		v, body = cstring(body[1:])
		switch field {
		case 'M':
			msg = v
		case 'C':
			code = v
		}
	}
	if code == "" {
		return msg
	}
	return msg + " (SQLSTATE " + code + ")"
}

// bindParams decodes parameter values from the remainder of a Bind message
// (after the portal and statement names). Binary values are summarized
// rather than decoded.
func bindParams(b []byte) []string {
	u16 := func() (int, bool) {
		if len(b) < 2 {
			return 0, false
		}
		v := int(binary.BigEndian.Uint16(b))
		b = b[2:]
		return v, true
	}

	nFormats, ok := u16()
	if !ok || len(b) < 2*nFormats {
		return nil
	}
	formats := make([]int, nFormats)
	for i := range formats {
		formats[i], _ = u16()
	}

	nParams, ok := u16()
	if !ok {
		return nil
	}
	params := make([]string, 0, nParams)
	for i := 0; i < nParams; i++ {
		if len(b) < 4 {
			return params
		}
		n := int32(binary.BigEndian.Uint32(b))
		b = b[4:]
		if n < 0 {
			params = append(params, "NULL")
			continue
		}
		if int(n) > len(b) {
			return params
		}
		format := 0
		switch {
		case nFormats == 1:
			format = formats[0]
		case i < nFormats:
			format = formats[i]
		}
		if format == 1 {
			params = append(params, "<binary "+strconv.Itoa(int(n))+" bytes>")
		} else {
			params = append(params, string(b[:n]))
		}
		b = b[n:]
	}
	return params
}
