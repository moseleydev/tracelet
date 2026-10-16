package postgres

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/moseleydev/tracelet/core"
	"github.com/moseleydev/tracelet/proxy"
)

// fakePostgres is a minimal PostgreSQL server that speaks just enough of the
// protocol to exercise the proxy. Queries containing "boom" fail.
type fakePostgres struct {
	addr     string
	cancels  chan *pgproto3.CancelRequest
	received chan string // every simple query text, as the server got it
}

func startFakePostgres(t *testing.T) *fakePostgres {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	f := &fakePostgres{addr: ln.Addr().String(), cancels: make(chan *pgproto3.CancelRequest, 1), received: make(chan string, 100)}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakePostgres) serve(c net.Conn) {
	defer c.Close()
	be := pgproto3.NewBackend(c, c)
	startup, err := be.ReceiveStartupMessage()
	if err != nil {
		return
	}
	if cr, ok := startup.(*pgproto3.CancelRequest); ok {
		f.cancels <- cr
		return
	}
	be.Send(&pgproto3.AuthenticationOk{})
	be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	be.Flush()

	failed := false // extended protocol: skip everything until Sync after an error
	fail := func() {
		be.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "42P01", Message: `relation "boom" does not exist`})
	}
	for {
		msg, err := be.Receive()
		if err != nil {
			return
		}
		switch m := msg.(type) {
		case *pgproto3.Query:
			select {
			case f.received <- m.String:
			default:
			}
			if strings.Contains(m.String, "boom") {
				fail()
			} else {
				be.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 3")})
			}
			be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			be.Flush()
		case *pgproto3.Parse:
			if failed {
				continue
			}
			if strings.Contains(m.Query, "boom") {
				fail()
				failed = true
				continue
			}
			be.Send(&pgproto3.ParseComplete{})
		case *pgproto3.Bind:
			if !failed {
				be.Send(&pgproto3.BindComplete{})
			}
		case *pgproto3.Describe:
			if !failed {
				be.Send(&pgproto3.NoData{})
			}
		case *pgproto3.Execute:
			if !failed {
				be.Send(&pgproto3.CommandComplete{CommandTag: []byte("UPDATE 2")})
			}
		case *pgproto3.Sync:
			failed = false
			be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			be.Flush()
		case *pgproto3.Terminate:
			return
		}
	}
}

func startProxy(t *testing.T, upstream string, captureParams bool) (string, *proxy.Sink) {
	t.Helper()
	sink := proxy.NewSink(100)
	srv, err := New(Config{Upstream: upstream, SSLMode: "disable", CaptureParams: captureParams, Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		srv.Serve(ctx, ln)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return ln.Addr().String(), sink
}

// connect opens a client connection through the proxy and completes startup.
func connect(t *testing.T, addr string) *pgproto3.Frontend {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(5 * time.Second))

	fe := pgproto3.NewFrontend(c, c)
	fe.Send(&pgproto3.StartupMessage{
		ProtocolVersion: pgproto3.ProtocolVersionNumber,
		Parameters:      map[string]string{"user": "test", "database": "test"},
	})
	flush(t, fe)
	waitReady(t, fe)
	return fe
}

func flush(t *testing.T, fe *pgproto3.Frontend) {
	t.Helper()
	if err := fe.Flush(); err != nil {
		t.Fatal(err)
	}
}

func waitReady(t *testing.T, fe *pgproto3.Frontend) {
	t.Helper()
	for {
		msg, err := fe.Receive()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := msg.(*pgproto3.ReadyForQuery); ok {
			return
		}
	}
}

// queued holds events collected from a sink but not yet consumed by a test.
var queued = map[*proxy.Sink][]core.Event{}

func nextEvent(t *testing.T, sink *proxy.Sink) core.Event {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(queued[sink]) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for event")
		}
		batch, _ := sink.Batch(nil)
		queued[sink] = append(queued[sink], batch...)
	}
	e := queued[sink][0]
	queued[sink] = queued[sink][1:]
	return e
}

func expectNoEvent(t *testing.T, sink *proxy.Sink) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
	batch, _ := sink.Batch(nil)
	if pending := append(queued[sink], batch...); len(pending) > 0 {
		t.Fatalf("unexpected event: %+v", pending[0])
	}
}

func TestSimpleQuery(t *testing.T) {
	pg := startFakePostgres(t)
	addr, sink := startProxy(t, pg.addr, false)
	fe := connect(t, addr)

	fe.Send(&pgproto3.Query{String: "SELECT * FROM users"})
	flush(t, fe)
	waitReady(t, fe)

	e := nextEvent(t, sink)
	if e.Query != "SELECT * FROM users" || e.Rows != 3 || e.Error != "" || e.Engine != core.Postgres {
		t.Fatalf("unexpected event: %+v", e)
	}
	if e.Duration <= 0 || e.StartedAt.IsZero() || e.ConnID == 0 {
		t.Fatalf("event missing timing or connection: %+v", e)
	}
}

func TestSimpleQueryError(t *testing.T) {
	pg := startFakePostgres(t)
	addr, sink := startProxy(t, pg.addr, false)
	fe := connect(t, addr)

	fe.Send(&pgproto3.Query{String: "SELECT * FROM boom"})
	flush(t, fe)
	waitReady(t, fe)

	e := nextEvent(t, sink)
	want := `relation "boom" does not exist (SQLSTATE 42P01)`
	if e.Error != want {
		t.Fatalf("error = %q, want %q", e.Error, want)
	}
}

func sendExtended(fe *pgproto3.Frontend, stmt, query string, params ...[]byte) {
	fe.Send(&pgproto3.Parse{Name: stmt, Query: query})
	fe.Send(&pgproto3.Bind{PreparedStatement: stmt, Parameters: params})
	fe.Send(&pgproto3.Describe{ObjectType: 'P'})
	fe.Send(&pgproto3.Execute{})
}

func TestExtendedQueryRedactsParams(t *testing.T) {
	pg := startFakePostgres(t)
	addr, sink := startProxy(t, pg.addr, false)
	fe := connect(t, addr)

	sendExtended(fe, "s1", "UPDATE users SET name = $1 WHERE id = $2", []byte("ada"), []byte("7"))
	fe.Send(&pgproto3.Sync{})
	flush(t, fe)
	waitReady(t, fe)

	e := nextEvent(t, sink)
	if e.Query != "UPDATE users SET name = $1 WHERE id = $2" || e.Rows != 2 {
		t.Fatalf("unexpected event: %+v", e)
	}
	if e.Params != nil {
		t.Fatalf("params must be redacted by default, got %v", e.Params)
	}
}

func TestExtendedQueryCapturesParams(t *testing.T) {
	pg := startFakePostgres(t)
	addr, sink := startProxy(t, pg.addr, true)
	fe := connect(t, addr)

	sendExtended(fe, "", "UPDATE users SET name = $1 WHERE id = $2", []byte("ada"), nil)
	fe.Send(&pgproto3.Sync{})
	flush(t, fe)
	waitReady(t, fe)

	if got := nextEvent(t, sink).Params; !reflect.DeepEqual(got, []string{"ada", "NULL"}) {
		t.Fatalf("params = %v", got)
	}
}

func TestPreparedStatementReuse(t *testing.T) {
	pg := startFakePostgres(t)
	addr, sink := startProxy(t, pg.addr, false)
	fe := connect(t, addr)

	// Prepare once, then execute twice by name without re-sending Parse.
	fe.Send(&pgproto3.Parse{Name: "cached", Query: "UPDATE t SET x = 1"})
	fe.Send(&pgproto3.Sync{})
	flush(t, fe)
	waitReady(t, fe)
	expectNoEvent(t, sink)

	for i := 0; i < 2; i++ {
		fe.Send(&pgproto3.Bind{PreparedStatement: "cached"})
		fe.Send(&pgproto3.Execute{})
		fe.Send(&pgproto3.Sync{})
		flush(t, fe)
		waitReady(t, fe)
		if e := nextEvent(t, sink); e.Query != "UPDATE t SET x = 1" {
			t.Fatalf("execution %d: query = %q", i, e.Query)
		}
	}
}

func TestExtendedErrorSkipsRestOfBatch(t *testing.T) {
	pg := startFakePostgres(t)
	addr, sink := startProxy(t, pg.addr, false)
	fe := connect(t, addr)

	// One good statement, then a failing one followed by an Execute the
	// server will skip, all in one batch.
	sendExtended(fe, "", "UPDATE ok SET x = 1")
	sendExtended(fe, "", "SELECT boom")
	fe.Send(&pgproto3.Execute{})
	fe.Send(&pgproto3.Sync{})
	flush(t, fe)
	waitReady(t, fe)

	if e := nextEvent(t, sink); e.Query != "UPDATE ok SET x = 1" || e.Error != "" || e.Rows != 2 {
		t.Fatalf("first event: %+v", e)
	}
	if e := nextEvent(t, sink); e.Query != "SELECT boom" || e.Error == "" {
		t.Fatalf("second event should be the failed query: %+v", e)
	}
	expectNoEvent(t, sink) // the skipped Execute never ran

	// The connection state must still line up for the next query.
	fe.Send(&pgproto3.Query{String: "SELECT 1"})
	flush(t, fe)
	waitReady(t, fe)
	if e := nextEvent(t, sink); e.Query != "SELECT 1" || e.Error != "" {
		t.Fatalf("query after failed batch: %+v", e)
	}
}

func TestFailedPrepareIsReported(t *testing.T) {
	pg := startFakePostgres(t)
	addr, sink := startProxy(t, pg.addr, false)
	fe := connect(t, addr)

	// Drivers such as pgx prepare first (Parse, Describe, Sync) and only
	// execute if that succeeds, so a missing table fails with no Execute.
	fe.Send(&pgproto3.Parse{Name: "s1", Query: "SELECT percent FROM boom WHERE code = $1"})
	fe.Send(&pgproto3.Describe{ObjectType: 'S', Name: "s1"})
	fe.Send(&pgproto3.Sync{})
	flush(t, fe)
	waitReady(t, fe)

	e := nextEvent(t, sink)
	if e.Query != "SELECT percent FROM boom WHERE code = $1" || !strings.Contains(e.Error, "42P01") {
		t.Fatalf("failed prepare event: %+v", e)
	}
	expectNoEvent(t, sink)

	// A successful prepare emits nothing by itself.
	fe.Send(&pgproto3.Parse{Name: "s2", Query: "UPDATE t SET x = 1"})
	fe.Send(&pgproto3.Sync{})
	flush(t, fe)
	waitReady(t, fe)
	expectNoEvent(t, sink)
}

func TestSSLRequestDeclined(t *testing.T) {
	pg := startFakePostgres(t)
	addr, _ := startProxy(t, pg.addr, false)

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))

	req := make([]byte, 8)
	binary.BigEndian.PutUint32(req[0:], 8)
	binary.BigEndian.PutUint32(req[4:], sslRequestCode)
	c.Write(req)
	resp := make([]byte, 1)
	if _, err := io.ReadFull(c, resp); err != nil || resp[0] != 'N' {
		t.Fatalf("SSL request response = %q, %v; want 'N'", resp, err)
	}

	// Startup continues normally on the same connection.
	fe := pgproto3.NewFrontend(c, c)
	fe.Send(&pgproto3.StartupMessage{ProtocolVersion: pgproto3.ProtocolVersionNumber, Parameters: map[string]string{"user": "test"}})
	flush(t, fe)
	waitReady(t, fe)
}

func TestCancelRequestForwarded(t *testing.T) {
	pg := startFakePostgres(t)
	addr, _ := startProxy(t, pg.addr, false)

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fe := pgproto3.NewFrontend(c, c)
	// A 32-byte key, as PostgreSQL 18 (protocol 3.2) issues; older servers
	// use 4 bytes. Either way the request is forwarded byte for byte.
	key := bytes.Repeat([]byte{0xab}, 32)
	fe.Send(&pgproto3.CancelRequest{ProcessID: 42, SecretKey: key})
	flush(t, fe)

	select {
	case cr := <-pg.cancels:
		if cr.ProcessID != 42 || !bytes.Equal(cr.SecretKey, key) {
			t.Fatalf("cancel request = %+v", cr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel request was not forwarded")
	}
}

func TestRowsFromTag(t *testing.T) {
	for tag, want := range map[string]int64{
		"SELECT 5":      5,
		"INSERT 0 12":   12,
		"UPDATE 3":      3,
		"DELETE 0":      0,
		"COPY 100":      100,
		"CREATE TABLE":  0,
		"BEGIN":         0,
		"SET":           0,
		"ALTER TABLE 1": 0,
	} {
		if got := rowsFromTag(tag); got != want {
			t.Errorf("rowsFromTag(%q) = %d, want %d", tag, got, want)
		}
	}
}

func TestBindParams(t *testing.T) {
	// Formats: [text, binary]; params: "hi", 4 binary bytes, NULL.
	b := []byte{0, 2, 0, 0, 0, 1, 0, 3}
	b = append(b, 0, 0, 0, 2, 'h', 'i')
	b = append(b, 0, 0, 0, 4, 1, 2, 3, 4)
	b = append(b, 0xff, 0xff, 0xff, 0xff)
	b = append(b, 0, 0) // result formats

	want := []string{"hi", "<binary 4 bytes>", "NULL"}
	if got := bindParams(b); !reflect.DeepEqual(got, want) {
		t.Fatalf("bindParams = %v, want %v", got, want)
	}
	if got := bindParams([]byte{0, 9}); got != nil {
		t.Fatalf("truncated message should yield nil, got %v", got)
	}
}

func TestHugeQueryPassesThroughIntact(t *testing.T) {
	pg := startFakePostgres(t)
	addr, sink := startProxy(t, pg.addr, false)
	fe := connect(t, addr)

	// A 3 MB bulk statement: far larger than Tracelet ever buffers.
	huge := "INSERT INTO t VALUES " + strings.Repeat("('xxxxxxxxxx'),", 3<<20/15) + "('end')"
	fe.Send(&pgproto3.Query{String: huge})
	flush(t, fe)
	waitReady(t, fe)

	if got := <-pg.received; got != huge {
		t.Fatalf("server received %d bytes, want the original %d bytes unchanged", len(got), len(huge))
	}
	e := nextEvent(t, sink)
	if len(e.Query) > maxInspect || !strings.HasPrefix(huge, e.Query) {
		t.Fatalf("event query is %d bytes; want a prefix of at most %d", len(e.Query), maxInspect)
	}
}

func TestInspectionPanicLeavesTrafficUntouched(t *testing.T) {
	var logged []string
	srv := &Server{cfg: Config{Logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }}}
	tr := newTracker(srv, 7)
	calls := 0
	g := tr.guard(func(byte, []byte) { calls++; panic("parser bug") })

	g('Q', nil) // must not propagate
	g('Q', nil) // tracking is now off for this connection
	if calls != 1 || !tr.broken.Load() || len(logged) != 1 || !strings.Contains(logged[0], "traffic is unaffected") {
		t.Fatalf("calls %d, broken %v, logged %q", calls, tr.broken.Load(), logged)
	}
}
