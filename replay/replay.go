// Package replay sends recorded traffic to a database again, to measure how
// it performs there, or under more load than was recorded.
//
// Replay is built so it cannot damage data by accident:
//
//   - There is no default target. It never connects to the recorded
//     database for writes, and only for reads when explicitly allowed.
//   - Read mode (the default) opens every connection with
//     default_transaction_read_only=on: PostgreSQL itself rejects any
//     write, including writes hidden in functions.
//   - Rollback mode replays writes inside a transaction per connection that
//     is always rolled back. The application's own BEGIN / COMMIT /
//     ROLLBACK become savepoints, and anything that could end the outer
//     transaction (multi-statement simple queries, commands that can't run
//     in a transaction) is skipped.
package replay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/moseleydev/tracelet/core"
	"github.com/moseleydev/tracelet/core/fingerprint"
	"github.com/moseleydev/tracelet/core/session"
)

// Mode decides what is replayed.
type Mode string

const (
	// Read replays only reads, on read-only connections.
	Read Mode = "read"
	// Rollback replays everything inside transactions that are rolled back.
	Rollback Mode = "rollback"
)

// Options configures a replay.
type Options struct {
	Target string // PostgreSQL URL to replay against. Required.
	Mode   Mode
	// Speed multiplies the recorded pace: 2 replays twice as fast; 0 sends
	// every query as soon as the previous one on its connection finishes.
	Speed float64
	// Scale runs this many copies of each recorded connection at once,
	// simulating that many times the recorded traffic.
	Scale int
	// MaxConns caps concurrent connections to the target. Streams beyond it
	// wait their turn (and run later than recorded).
	MaxConns int
	// AllowSource permits a read-only replay against the database the
	// session was recorded from.
	AllowSource bool
	// Progress, if set, is called about every second.
	Progress func(Progress)
}

// Progress is reported while a replay runs.
type Progress struct {
	Done, Total, Errors int64
}

// Timing summarizes the latency of one query shape.
type Timing struct {
	Count int64         `json:"count"`
	P50   time.Duration `json:"p50"`
	P95   time.Duration `json:"p95"`
	Total time.Duration `json:"total"`
}

// QueryResult compares one query shape as recorded and as replayed.
type QueryResult struct {
	Hash      string         `json:"hash"`
	Query     string         `json:"query"` // normalized: no values
	Recorded  Timing         `json:"recorded"`
	Replayed  Timing         `json:"replayed"`
	Errors    int64          `json:"errors"`
	LastError string         `json:"last_error,omitempty"`
	Skipped   map[string]int `json:"skipped,omitempty"` // reason -> count
}

// Result is the outcome of a replay.
type Result struct {
	Session  string         `json:"session"`
	Mode     Mode           `json:"mode"`
	Speed    float64        `json:"speed"`
	Scale    int            `json:"scale"`
	Started  time.Time      `json:"started"`
	Elapsed  time.Duration  `json:"elapsed"`
	Recorded time.Duration  `json:"recorded"` // length of the recorded session
	Sent     int64          `json:"sent"`
	Errors   int64          `json:"errors"`
	Skipped  map[string]int `json:"skipped"`
	Queries  []*QueryResult `json:"queries"` // highest recorded total time first
}

// Run replays the session at path.
func Run(ctx context.Context, path string, opt Options) (*Result, error) {
	if opt.Target == "" {
		return nil, errors.New("replay needs a --target database; it never defaults to the recorded one")
	}
	if opt.Mode == "" {
		opt.Mode = Read
	}
	if opt.Mode != Read && opt.Mode != Rollback {
		return nil, fmt.Errorf("unknown mode %q: use read or rollback", opt.Mode)
	}
	if opt.Scale < 1 {
		opt.Scale = 1
	}
	if opt.MaxConns < 1 {
		opt.MaxConns = 100
	}

	r, err := session.Open(path)
	if err != nil {
		return nil, err
	}
	header := r.Header
	streams, recorded, length, err := load(r)
	r.Close()
	if err != nil {
		return nil, err
	}

	cfg, err := pgconn.ParseConfig(opt.Target)
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	if sameDatabase(header.Upstream, cfg) {
		if opt.Mode != Read {
			return nil, fmt.Errorf("refusing to replay writes into %s: that is the database this session was recorded from. "+
				"Replay writes only against a staging copy", header.Upstream)
		}
		if !opt.AllowSource {
			return nil, fmt.Errorf("the target is the database this session was recorded from (%s). "+
				"A read-only replay there adds load to it; pass --allow-source if that is what you want", header.Upstream)
		}
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["application_name"] = "tracelet-replay"
	if opt.Mode == Read {
		cfg.RuntimeParams["default_transaction_read_only"] = "on"
	}

	// Fail fast on a bad target before starting hundreds of streams.
	probe, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to target: %w", err)
	}
	probe.Close(ctx)

	res := &Result{Session: path, Mode: opt.Mode, Speed: opt.Speed, Scale: opt.Scale, Started: time.Now(), Recorded: length, Skipped: map[string]int{}}
	acc := newAccumulator(recorded)

	var total int64
	for _, s := range streams {
		total += int64(len(s.entries))
	}
	total *= int64(opt.Scale)

	var done, errs atomic.Int64
	stopProgress := make(chan struct{})
	if opt.Progress != nil {
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-stopProgress:
					return
				case <-t.C:
					opt.Progress(Progress{Done: done.Load(), Total: total, Errors: errs.Load()})
				}
			}
		}()
	}

	sem := make(chan struct{}, opt.MaxConns)
	var wg sync.WaitGroup
	start := time.Now()
	for copyN := 0; copyN < opt.Scale; copyN++ {
		for _, s := range streams {
			wg.Add(1)
			go func(s *stream) {
				defer wg.Done()
				// Join when the recorded connection first spoke.
				if !wait(ctx, start, s.entries[0].Offset, opt.Speed) {
					return
				}
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-sem }()
				runStream(ctx, cfg, opt, s, start, acc, &done, &errs)
			}(s)
		}
	}
	wg.Wait()
	close(stopProgress)

	res.Elapsed = time.Since(start)
	res.Queries, res.Sent, res.Errors, res.Skipped = acc.results()
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	return res, nil
}

type stream struct {
	conn    uint64
	entries []session.Entry
}

// load reads a session into per-connection streams, ordered by start time.
func load(r *session.Reader) ([]*stream, map[string][]session.Entry, time.Duration, error) {
	byConn := map[uint64]*stream{}
	recorded := map[string][]session.Entry{}
	var last int64
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, 0, err
		}
		recorded[e.Hash] = append(recorded[e.Hash], e)
		last = max(last, e.Offset+e.Duration)
		s := byConn[e.Conn]
		if s == nil {
			s = &stream{conn: e.Conn}
			byConn[e.Conn] = s
		}
		s.entries = append(s.entries, e)
	}
	if len(byConn) == 0 {
		return nil, nil, 0, errors.New("the session has no queries")
	}
	var out []*stream
	for _, s := range byConn {
		sort.SliceStable(s.entries, func(i, j int) bool { return s.entries[i].Offset < s.entries[j].Offset })
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].entries[0].Offset < out[j].entries[0].Offset })
	return out, recorded, time.Duration(last) * time.Microsecond, nil
}

// wait sleeps until the entry's scheduled time. It reports false if ctx ended.
func wait(ctx context.Context, start time.Time, offsetMicros int64, speed float64) bool {
	if speed <= 0 {
		return ctx.Err() == nil
	}
	due := start.Add(time.Duration(float64(offsetMicros)/speed) * time.Microsecond)
	d := time.Until(due)
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// runStream replays one recorded connection on its own connection.
func runStream(ctx context.Context, cfg *pgconn.Config, opt Options, s *stream, start time.Time,
	acc *accumulator, done, errs *atomic.Int64) {
	conn, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		for _, e := range s.entries {
			acc.skip(e, "could not connect")
		}
		return
	}
	defer conn.Close(context.Background())

	st := &streamState{conn: conn, mode: opt.Mode}
	if opt.Mode == Rollback {
		if err := st.control(ctx, "BEGIN"); err != nil {
			for _, e := range s.entries {
				acc.skip(e, "could not start a transaction")
			}
			return
		}
		// Whatever happens, nothing this stream did is ever committed.
		defer st.control(context.Background(), "ROLLBACK")
	}

	for _, e := range s.entries {
		if !wait(ctx, start, e.Offset, opt.Speed) {
			return
		}
		d, err, reason := st.exec(ctx, e)
		switch {
		case reason != "":
			acc.skip(e, reason)
		case err != nil:
			errs.Add(1)
			acc.add(e, d, err)
		default:
			acc.add(e, d, nil)
		}
		done.Add(1)
	}
}

type streamState struct {
	conn      *pgconn.PgConn
	mode      Mode
	inApp     bool // inside a transaction the application opened
	appFailed bool // a statement in that transaction failed
}

// control runs a statement Tracelet itself needs (not timed).
func (s *streamState) control(ctx context.Context, sql string) error {
	_, err := s.conn.Exec(ctx, sql).ReadAll()
	return err
}

// exec replays one entry. It returns the time the query took, its error,
// or a reason it was skipped.
func (s *streamState) exec(ctx context.Context, e session.Entry) (d time.Duration, err error, skipped string) {
	defer func() {
		if r := recover(); r != nil {
			d, err, skipped = 0, nil, "malformed recording"
		}
	}()
	x := e.Replay
	if x == nil {
		return 0, nil, "not replayable"
	}
	kind := classify(x.Query)

	if s.mode == Read && kind.writes {
		return 0, nil, "write (read mode)"
	}
	if s.mode == Rollback {
		if x.Simple && fingerprint.MultiStatement(x.Query) {
			// One of them could be a COMMIT that ends the outer transaction.
			return 0, nil, "several statements in one query"
		}
		if kind.noTxn {
			return 0, nil, "can't run inside a transaction"
		}
		if kind.txControl {
			return 0, s.translate(ctx, kind), ""
		}
		if !s.inApp {
			// One statement on its own: contain its effects and errors in a
			// savepoint so the outer transaction always survives.
			if err := s.control(ctx, "SAVEPOINT tracelet_stmt"); err != nil {
				// The outer transaction was aborted. Start a fresh one.
				if s.restart(ctx) != nil || s.control(ctx, "SAVEPOINT tracelet_stmt") != nil {
					return 0, nil, "transaction lost"
				}
			}
			d, err = s.run(ctx, x)
			if err != nil {
				s.control(ctx, "ROLLBACK TO SAVEPOINT tracelet_stmt")
			}
			s.control(ctx, "RELEASE SAVEPOINT tracelet_stmt")
			return d, err, ""
		}
		d, err = s.run(ctx, x)
		if err != nil {
			s.appFailed = true
		}
		return d, err, ""
	}
	d, err = s.run(ctx, x)
	return d, err, ""
}

// restart replaces an aborted outer transaction with a new one. Nothing is
// committed: ROLLBACK discards everything the stream did so far.
func (s *streamState) restart(ctx context.Context) error {
	s.control(ctx, "ROLLBACK")
	s.inApp, s.appFailed = false, false
	return s.control(ctx, "BEGIN")
}

// run sends the recorded query exactly as it was sent originally.
func (s *streamState) run(ctx context.Context, x *core.Replay) (time.Duration, error) {
	t0 := time.Now()
	if x.Simple {
		results, err := s.conn.Exec(ctx, x.Query).ReadAll()
		d := time.Since(t0)
		if err == nil {
			for _, r := range results {
				if r.Err != nil {
					err = r.Err
					break
				}
			}
		}
		return d, err
	}
	oids := x.ParamOIDs
	if len(oids) > 1 && len(oids) != len(x.Params) {
		// Parse may declare types for only some parameters; the rest are
		// inferred, which is what 0 means.
		fixed := make([]uint32, len(x.Params))
		copy(fixed, oids)
		oids = fixed
	}
	if n := len(x.ParamFormats); n > 1 && n != len(x.Params) {
		return 0, errors.New("malformed recording: parameter formats don't match parameters")
	}
	res := s.conn.ExecParams(ctx, x.Query, x.Params, oids, x.ParamFormats, x.ResultFormats).Read()
	return time.Since(t0), res.Err
}

// translate turns the application's transaction control into savepoints
// inside Tracelet's outer transaction, which is never committed.
func (s *streamState) translate(ctx context.Context, k kind) error {
	switch k.verb {
	case "BEGIN", "START":
		if s.inApp {
			return nil
		}
		s.inApp, s.appFailed = true, false
		return s.control(ctx, "SAVEPOINT tracelet_app")
	case "COMMIT", "END":
		if !s.inApp {
			return nil
		}
		s.inApp = false
		if s.appFailed {
			// PostgreSQL would turn this COMMIT into a rollback.
			s.control(ctx, "ROLLBACK TO SAVEPOINT tracelet_app")
		}
		return s.control(ctx, "RELEASE SAVEPOINT tracelet_app")
	case "ROLLBACK", "ABORT":
		if k.toSavepoint {
			return s.control(ctx, k.raw) // ROLLBACK TO <app savepoint>, inside the app's transaction
		}
		if !s.inApp {
			return nil
		}
		s.inApp = false
		s.control(ctx, "ROLLBACK TO SAVEPOINT tracelet_app")
		return s.control(ctx, "RELEASE SAVEPOINT tracelet_app")
	case "SAVEPOINT", "RELEASE":
		return s.control(ctx, k.raw)
	}
	return nil
}

type kind struct {
	verb        string // first keyword, upper case
	raw         string
	writes      bool // changes data or schema
	txControl   bool // BEGIN, COMMIT, ROLLBACK, SAVEPOINT, ...
	toSavepoint bool // ROLLBACK TO ...
	noTxn       bool // can't run inside a transaction block, or must never be replayed
}

// classify looks at the leading keywords of a statement.
func classify(q string) kind {
	words := leadingWords(q, 4)
	k := kind{raw: strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(q), ";"))}
	if len(words) == 0 {
		return k
	}
	k.verb = words[0]
	second := ""
	if len(words) > 1 {
		second = words[1]
	}
	rest := strings.Join(words, " ")
	switch k.verb {
	case "BEGIN", "START", "COMMIT", "END", "ABORT", "SAVEPOINT", "RELEASE":
		k.txControl = true
	case "ROLLBACK":
		k.txControl = true
		k.toSavepoint = second == "TO"
	case "PREPARE":
		k.noTxn = second == "TRANSACTION"
	case "INSERT", "UPDATE", "DELETE", "MERGE", "CREATE", "ALTER", "DROP", "TRUNCATE", "GRANT", "REVOKE",
		"COMMENT", "REFRESH", "LOCK", "REINDEX", "CLUSTER", "SECURITY", "IMPORT", "CALL", "DO", "NOTIFY":
		k.writes = true
	case "SELECT", "WITH":
		u := strings.ToUpper(q)
		k.writes = strings.Contains(u, " FOR UPDATE") || strings.Contains(u, " FOR NO KEY UPDATE") || strings.Contains(u, " FOR SHARE") ||
			strings.Contains(u, " FOR KEY SHARE") || strings.Contains(u, " INTO ") || (k.verb == "WITH" && containsAny(u, "INSERT ", "UPDATE ", "DELETE ", "MERGE "))
	}
	switch {
	case k.verb == "VACUUM", k.verb == "CHECKPOINT", k.verb == "DISCARD", k.verb == "DEALLOCATE",
		k.verb == "COPY", k.verb == "LISTEN", k.verb == "UNLISTEN", k.verb == "LOAD",
		k.verb == "ALTER" && second == "SYSTEM",
		k.verb == "CREATE" && (second == "DATABASE" || second == "TABLESPACE" || strings.Contains(rest, "CONCURRENTLY")),
		k.verb == "DROP" && (second == "DATABASE" || second == "TABLESPACE" || strings.Contains(rest, "CONCURRENTLY")),
		k.verb == "REINDEX" && strings.Contains(strings.ToUpper(q), "CONCURRENTLY"),
		(k.verb == "COMMIT" || k.verb == "ROLLBACK") && second == "PREPARED":
		k.noTxn = true
	}
	if k.noTxn {
		k.writes = true
	}
	return k
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// leadingWords returns up to n upper-cased words, skipping comments.
func leadingWords(q string, n int) []string {
	var out []string
	for len(q) > 0 && len(out) < n {
		q = strings.TrimLeft(q, " \t\r\n(;")
		switch {
		case strings.HasPrefix(q, "--"):
			if i := strings.IndexByte(q, '\n'); i >= 0 {
				q = q[i+1:]
			} else {
				q = ""
			}
			continue
		case strings.HasPrefix(q, "/*"):
			if i := strings.Index(q, "*/"); i >= 0 {
				q = q[i+2:]
			} else {
				q = ""
			}
			continue
		}
		i := strings.IndexAny(q, " \t\r\n(;")
		if i < 0 {
			i = len(q)
		}
		if i == 0 {
			break
		}
		out = append(out, strings.ToUpper(q[:i]))
		q = q[i:]
	}
	return out
}

// sameDatabase reports whether the target is the recorded database.
// recorded is "host:port/dbname" from the session header.
func sameDatabase(recorded string, target *pgconn.Config) bool {
	if recorded == "" {
		return false
	}
	addr, db, _ := strings.Cut(recorded, "/")
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return sameHost(host, target.Host) && port == fmt.Sprint(target.Port) && db == target.Database
}

func sameHost(a, b string) bool {
	local := func(h string) bool {
		if h == "localhost" || strings.HasPrefix(h, "/") { // unix socket
			return true
		}
		ip := net.ParseIP(h)
		return ip != nil && ip.IsLoopback()
	}
	return strings.EqualFold(a, b) || (local(a) && local(b))
}
