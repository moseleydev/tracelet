// Package store persists fingerprints and rollups in a local SQLite file and
// answers the questions the CLI and studio ask: which queries cost the most
// time, how a query's latency changed, and which tables are busiest.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver: no cgo needed

	"github.com/moseleydev/tracelet/core"
)

// schema creates version 1; migrations take it from there. Each entry in
// migrations moves the file from version i+1 to i+2.
const schema = `
CREATE TABLE IF NOT EXISTS fingerprints (
	hash       TEXT PRIMARY KEY,
	query      TEXT NOT NULL,
	tables     TEXT NOT NULL,   -- JSON array
	first_seen INTEGER NOT NULL -- unix nanoseconds
);

CREATE TABLE IF NOT EXISTS rollups (
	hash          TEXT    NOT NULL,
	bucket        INTEGER NOT NULL, -- bucket start, unix seconds
	count         INTEGER NOT NULL,
	errors        INTEGER NOT NULL,
	rows          INTEGER NOT NULL,
	total_ns      INTEGER NOT NULL,
	hist          BLOB    NOT NULL,
	slow_ns       INTEGER NOT NULL,
	slow_query    TEXT    NOT NULL,
	slow_params   TEXT    NOT NULL, -- JSON array
	last_error    TEXT    NOT NULL,
	PRIMARY KEY (hash, bucket)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS rollups_by_bucket ON rollups (bucket);
`

var migrations = []string{
	// v2: query templates, EXPLAIN plans, detector findings, N+1 patterns.
	`
	ALTER TABLE fingerprints ADD COLUMN template TEXT NOT NULL DEFAULT '';

	CREATE TABLE plans (
		hash        TEXT PRIMARY KEY,
		explained_at INTEGER NOT NULL, -- unix nanoseconds
		plan        TEXT NOT NULL,     -- JSON plan tree, empty on error
		error       TEXT NOT NULL
	);

	CREATE TABLE findings (
		hash        TEXT    NOT NULL,
		detector    TEXT    NOT NULL,
		severity    INTEGER NOT NULL,
		title       TEXT    NOT NULL,
		explanation TEXT    NOT NULL,
		suggestion  TEXT    NOT NULL,
		fix         TEXT    NOT NULL,
		updated_at  INTEGER NOT NULL,
		PRIMARY KEY (hash, detector)
	);

	CREATE TABLE patterns (
		hash        TEXT PRIMARY KEY,
		parent_hash TEXT    NOT NULL,
		runs        INTEGER NOT NULL,
		calls       INTEGER NOT NULL,
		max_run     INTEGER NOT NULL,
		last_seen   INTEGER NOT NULL
	);
	`,
	// v3: suggested indexes and their what-if simulations.
	`
	ALTER TABLE findings ADD COLUMN idx TEXT NOT NULL DEFAULT '';        -- JSON IndexSuggestion
	ALTER TABLE findings ADD COLUMN simulation TEXT NOT NULL DEFAULT ''; -- JSON Simulation
	`,
	// v4: request traces from sqlcommenter context, and the route of N+1 bursts.
	`
	CREATE TABLE traces (
		id          TEXT PRIMARY KEY,
		route       TEXT    NOT NULL,
		started_at  INTEGER NOT NULL, -- unix nanoseconds
		duration_ns INTEGER NOT NULL,
		queries     INTEGER NOT NULL,
		db_ns       INTEGER NOT NULL,
		errors      INTEGER NOT NULL,
		truncated   INTEGER NOT NULL,
		spans       TEXT    NOT NULL  -- JSON []Span
	);
	CREATE INDEX traces_by_time ON traces (started_at);
	CREATE INDEX traces_by_route ON traces (route, started_at);
	ALTER TABLE patterns ADD COLUMN route TEXT NOT NULL DEFAULT '';
	`,
}

var schemaVersion = 1 + len(migrations)

// Store is a Tracelet data file.
type Store struct {
	db *sql.DB
}

// Open opens or creates the data file at path.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite allows one writer; serialize in-process

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if version > schemaVersion {
		db.Close()
		return nil, fmt.Errorf("open %s: data file is from a newer Tracelet (schema %d)", path, version)
	}
	if version == 0 {
		if _, err := db.Exec(schema); err != nil {
			db.Close()
			return nil, fmt.Errorf("create schema: %w", err)
		}
		version = 1
	}
	for ; version < schemaVersion; version++ {
		if err := migrate(db, migrations[version-1], version+1); err != nil {
			db.Close()
			return nil, fmt.Errorf("upgrade %s to schema %d: %w", path, version+1, err)
		}
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB, stmts string, to int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(stmts); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", to)); err != nil {
		return err
	}
	return tx.Commit()
}

// Close closes the data file.
func (s *Store) Close() error { return s.db.Close() }

// Save records new fingerprints and inserts or replaces rollups. Each rollup
// must hold the complete state of its bucket.
func (s *Store) Save(fps []core.Fingerprint, rollups []core.Rollup) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UnixNano()
	for _, fp := range fps {
		tables, _ := json.Marshal(nonNil(fp.Tables))
		if _, err := tx.Exec(
			`INSERT INTO fingerprints (hash, query, tables, first_seen, template) VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT (hash) DO UPDATE SET template = excluded.template
			 WHERE fingerprints.template = '' AND excluded.template != ''`,
			fp.Hash, fp.Query, string(tables), now, fp.Template,
		); err != nil {
			return err
		}
	}
	for _, r := range rollups {
		hist, _ := r.Hist.MarshalBinary()
		params, _ := json.Marshal(nonNil(r.SlowParams))
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO rollups
			 (hash, bucket, count, errors, rows, total_ns, hist, slow_ns, slow_query, slow_params, last_error)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Hash, r.Bucket.Unix(), r.Count, r.Errors, r.Rows, int64(r.Total), hist,
			int64(r.SlowDuration), r.SlowQuery, string(params), r.LastError,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Top returns per-fingerprint stats for rollups since the given time, ranked
// by total time spent (calls × mean latency), highest first. limit <= 0
// returns everything.
func (s *Store) Top(since time.Time, limit int) ([]core.Stats, error) {
	byHash, err := s.rollups(`WHERE bucket >= ?`, bucketOf(since))
	if err != nil {
		return nil, err
	}
	fps, err := s.fingerprints()
	if err != nil {
		return nil, err
	}
	stats := make([]core.Stats, 0, len(byHash))
	for hash, rs := range byHash {
		fp, ok := fps[hash]
		if !ok {
			fp = core.Fingerprint{Hash: hash}
		}
		stats = append(stats, core.Summarize(fp, rs, core.BucketSize))
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Total != stats[j].Total {
			return stats[i].Total > stats[j].Total
		}
		return stats[i].Fingerprint.Hash < stats[j].Fingerprint.Hash
	})
	if limit > 0 && len(stats) > limit {
		stats = stats[:limit]
	}
	return stats, nil
}

// Series returns one fingerprint's rollups since the given time, oldest
// first: the data behind a latency-over-time chart.
func (s *Store) Series(hash string, since time.Time) ([]core.Rollup, error) {
	byHash, err := s.rollups(`WHERE hash = ? AND bucket >= ?`, hash, bucketOf(since))
	if err != nil {
		return nil, err
	}
	rs := byHash[hash]
	sort.Slice(rs, func(i, j int) bool { return rs[i].Bucket.Before(rs[j].Bucket) })
	return rs, nil
}

// TableStats summarizes the queries that touch one table.
type TableStats struct {
	Table        string        `json:"table"`
	Fingerprints []string      `json:"fingerprints"` // hashes, highest total time first
	Count        int64         `json:"count"`
	Total        time.Duration `json:"total"`
}

// Tables returns every table touched since the given time, busiest first.
// A query that touches several tables counts toward each of them.
func (s *Store) Tables(since time.Time) ([]TableStats, error) {
	top, err := s.Top(since, 0)
	if err != nil {
		return nil, err
	}
	byTable := map[string]*TableStats{}
	for _, st := range top { // already ordered by total time
		for _, t := range st.Fingerprint.Tables {
			ts := byTable[t]
			if ts == nil {
				ts = &TableStats{Table: t}
				byTable[t] = ts
			}
			ts.Fingerprints = append(ts.Fingerprints, st.Fingerprint.Hash)
			ts.Count += st.Count
			ts.Total += st.Total
		}
	}
	out := make([]TableStats, 0, len(byTable))
	for _, ts := range byTable {
		out = append(out, *ts)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Table < out[j].Table
	})
	return out, nil
}

// Fingerprints returns every known fingerprint by hash.
func (s *Store) Fingerprints() (map[string]core.Fingerprint, error) { return s.fingerprints() }

func (s *Store) fingerprints() (map[string]core.Fingerprint, error) {
	rows, err := s.db.Query(`SELECT hash, query, tables, template FROM fingerprints`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]core.Fingerprint{}
	for rows.Next() {
		var fp core.Fingerprint
		var tables string
		if err := rows.Scan(&fp.Hash, &fp.Query, &tables, &fp.Template); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(tables), &fp.Tables)
		out[fp.Hash] = fp
	}
	return out, rows.Err()
}

func (s *Store) rollups(where string, args ...any) (map[string][]core.Rollup, error) {
	rows, err := s.db.Query(
		`SELECT hash, bucket, count, errors, rows, total_ns, hist, slow_ns, slow_query, slow_params, last_error
		 FROM rollups `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]core.Rollup{}
	for rows.Next() {
		var (
			r              core.Rollup
			bucket         int64
			total, slow    int64
			hist           []byte
			params, errStr string
		)
		if err := rows.Scan(&r.Hash, &bucket, &r.Count, &r.Errors, &r.Rows, &total, &hist,
			&slow, &r.SlowQuery, &params, &errStr); err != nil {
			return nil, err
		}
		if err := r.Hist.UnmarshalBinary(hist); err != nil {
			return nil, fmt.Errorf("rollup %s@%d: %w", r.Hash, bucket, err)
		}
		r.Bucket = time.Unix(bucket, 0).UTC()
		r.Total, r.SlowDuration = time.Duration(total), time.Duration(slow)
		r.LastError = errStr
		json.Unmarshal([]byte(params), &r.SlowParams)
		if len(r.SlowParams) == 0 {
			r.SlowParams = nil
		}
		out[r.Hash] = append(out[r.Hash], r)
	}
	return out, rows.Err()
}

func bucketOf(t time.Time) int64 {
	return t.Truncate(core.BucketSize).Unix()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// DefaultPath is where tracelet keeps its data when --data is not given: a
// .tracelet directory in the current project.
var DefaultPath = filepath.Join(".tracelet", "tracelet.db")

// Exists reports whether a data file is present at path.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
