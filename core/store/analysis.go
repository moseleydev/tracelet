package store

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/moseleydev/tracelet/core"
	"github.com/moseleydev/tracelet/detectors"
)

// PlanRecord is the latest EXPLAIN result for a fingerprint.
type PlanRecord struct {
	Hash        string          `json:"hash"`
	ExplainedAt time.Time       `json:"explained_at"`
	Plan        *detectors.Plan `json:"plan,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// SavePlan records the latest EXPLAIN result (or failure) for hash.
func (s *Store) SavePlan(rec PlanRecord) error {
	var plan []byte
	if rec.Plan != nil {
		plan, _ = json.Marshal(rec.Plan)
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO plans (hash, explained_at, plan, error) VALUES (?, ?, ?, ?)`,
		rec.Hash, rec.ExplainedAt.UnixNano(), string(plan), rec.Error,
	)
	return err
}

// Plans returns the latest plan record for every explained fingerprint.
func (s *Store) Plans() (map[string]PlanRecord, error) {
	rows, err := s.db.Query(`SELECT hash, explained_at, plan, error FROM plans`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PlanRecord{}
	for rows.Next() {
		var rec PlanRecord
		var at int64
		var plan string
		if err := rows.Scan(&rec.Hash, &at, &plan, &rec.Error); err != nil {
			return nil, err
		}
		rec.ExplainedAt = time.Unix(0, at)
		if plan != "" {
			rec.Plan = &detectors.Plan{}
			if err := json.Unmarshal([]byte(plan), rec.Plan); err != nil {
				rec.Plan, rec.Error = nil, "stored plan is unreadable: "+err.Error()
			}
		}
		out[rec.Hash] = rec
	}
	return out, rows.Err()
}

// StoredFinding is a detector finding attached to a fingerprint.
type StoredFinding struct {
	Hash string `json:"hash"`
	detectors.Finding
	UpdatedAt time.Time `json:"updated_at"`
}

// ReplaceFindings sets the current findings for hash, removing any that no
// longer apply (for example after an index was added).
func (s *Store) ReplaceFindings(hash string, findings []detectors.Finding) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM findings WHERE hash = ?`, hash); err != nil {
		return err
	}
	now := time.Now().UnixNano()
	for _, f := range findings {
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO findings (hash, detector, severity, title, explanation, suggestion, fix, updated_at, idx, simulation)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			hash, f.Detector, int(f.Severity), f.Title, f.Explanation, f.Suggestion, f.Fix, now,
			jsonOrEmpty(f.Index), jsonOrEmpty(f.Simulation),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Findings returns every current finding, most severe first.
func (s *Store) Findings() ([]StoredFinding, error) {
	rows, err := s.db.Query(
		`SELECT hash, detector, severity, title, explanation, suggestion, fix, updated_at, idx, simulation FROM findings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredFinding
	for rows.Next() {
		var f StoredFinding
		var sev int
		var at int64
		var idx, sim string
		if err := rows.Scan(&f.Hash, &f.Detector, &sev, &f.Title, &f.Explanation, &f.Suggestion, &f.Fix, &at, &idx, &sim); err != nil {
			return nil, err
		}
		if idx != "" {
			f.Index = &detectors.IndexSuggestion{}
			if json.Unmarshal([]byte(idx), f.Index) != nil {
				f.Index = nil
			}
		}
		if sim != "" {
			f.Simulation = &detectors.Simulation{}
			if json.Unmarshal([]byte(sim), f.Simulation) != nil {
				f.Simulation = nil
			}
		}
		f.Severity = detectors.Severity(sev)
		f.UpdatedAt = time.Unix(0, at)
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity > out[j].Severity
		}
		return out[i].Hash < out[j].Hash
	})
	return out, rows.Err()
}

// jsonOrEmpty encodes v, or returns "" for a nil pointer.
func jsonOrEmpty[T any](v *T) string {
	if v == nil {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// AddBursts adds newly observed bursts to the running totals.
func (s *Store) AddBursts(deltas []core.Bursts) error {
	if len(deltas) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, b := range deltas {
		if _, err := tx.Exec(
			`INSERT INTO patterns (hash, parent_hash, runs, calls, max_run, last_seen, route) VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (hash) DO UPDATE SET
				parent_hash = excluded.parent_hash,
				runs = runs + excluded.runs,
				calls = calls + excluded.calls,
				max_run = max(max_run, excluded.max_run),
				last_seen = max(last_seen, excluded.last_seen),
				route = CASE WHEN excluded.route != '' THEN excluded.route ELSE route END`,
			b.Hash, b.ParentHash, b.Runs, b.Calls, b.MaxRun, b.LastSeen.UnixNano(), b.Route,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Bursts returns N+1 burst totals for fingerprints seen since the given time.
func (s *Store) Bursts(since time.Time) (map[string]core.Bursts, error) {
	rows, err := s.db.Query(
		`SELECT hash, parent_hash, runs, calls, max_run, last_seen, route FROM patterns WHERE last_seen >= ?`,
		since.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]core.Bursts{}
	for rows.Next() {
		var b core.Bursts
		var at int64
		if err := rows.Scan(&b.Hash, &b.ParentHash, &b.Runs, &b.Calls, &b.MaxRun, &at, &b.Route); err != nil {
			return nil, err
		}
		b.LastSeen = time.Unix(0, at)
		out[b.Hash] = b
	}
	return out, rows.Err()
}
