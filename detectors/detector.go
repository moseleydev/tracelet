// Package detectors contains the rules that turn query stats into
// recommendations.
//
// Each detector lives in its own file and registers itself in init(). Adding a
// detector should never require touching any other part of Tracelet; see
// CONTRIBUTING.md for a walkthrough.
package detectors

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/moseleydev/tracelet/core"
)

// Severity ranks how urgently a finding should be addressed.
type Severity int

const (
	Info Severity = iota
	Warning
	Critical
)

// MarshalJSON writes a severity as its name: "info", "warning" or "critical".
func (s Severity) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// UnmarshalJSON reads a severity name.
func (s *Severity) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err != nil {
		return err
	}
	switch name {
	case "info":
		*s = Info
	case "warning":
		*s = Warning
	case "critical":
		*s = Critical
	default:
		return fmt.Errorf("unknown severity %q", name)
	}
	return nil
}

func (s Severity) String() string {
	switch s {
	case Critical:
		return "critical"
	case Warning:
		return "warning"
	default:
		return "info"
	}
}

// Input is everything a detector may look at for one fingerprint.
type Input struct {
	Stats core.Stats
	// ShareOfTime is this fingerprint's fraction (0-1) of all time spent in
	// the database over the same window: how much fixing it would matter.
	ShareOfTime float64
	// Events from a single trace (same TraceID) that contained this
	// fingerprint. Empty when the app has no SDK context.
	Trace []core.Event
	// Plan is the EXPLAIN output for this fingerprint, if one was collected.
	Plan *Plan
	// Bursts describes back-to-back repeats on one connection, if any.
	Bursts *core.Bursts
	// Parent is the fingerprint that usually runs right before a burst.
	Parent *core.Fingerprint
}

// Plan is an engine-neutral query plan tree.
type Plan struct {
	Node      string   `json:"node"` // e.g. "Seq Scan", "Index Scan", "COLLSCAN"
	Relation  string   `json:"relation,omitempty"`
	Schema    string   `json:"schema,omitempty"`
	Index     string   `json:"index,omitempty"`
	Filter    string   `json:"filter,omitempty"`
	IndexCond string   `json:"index_cond,omitempty"`
	SortKey   []string `json:"sort_key,omitempty"`
	EstRows   float64  `json:"est_rows"`
	TotalCost float64  `json:"total_cost"`
	Children  []*Plan  `json:"children,omitempty"`

	// Filled in for nodes that read a table: its estimated size and the
	// indexes it already has.
	TableRows float64 `json:"table_rows,omitempty"`
	Indexes   []Index `json:"indexes,omitempty"`
}

// Index is an existing index on a table.
type Index struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"` // key columns or expressions, in order
	Unique  bool     `json:"unique,omitempty"`
}

// Walk calls fn for p and every node below it, with each node's parent
// (nil for the root).
func (p *Plan) Walk(fn func(node, parent *Plan)) {
	var walk func(n, parent *Plan)
	walk = func(n, parent *Plan) {
		fn(n, parent)
		for _, c := range n.Children {
			walk(c, n)
		}
	}
	if p != nil {
		walk(p, nil)
	}
}

// Table returns the schema-qualified table name of a scan node.
func (p *Plan) Table() string {
	if p.Schema != "" {
		return p.Schema + "." + p.Relation
	}
	return p.Relation
}

// Finding is a single recommendation shown in the studio.
type Finding struct {
	Detector    string   `json:"detector"`
	Severity    Severity `json:"severity"`
	Title       string   `json:"title"`         // "Full table scan on users"
	Explanation string   `json:"explanation"`   // why it is slow, in plain words
	Suggestion  string   `json:"suggestion"`    // what to do
	Fix         string   `json:"fix,omitempty"` // ready-to-run SQL, when possible

	// Index is set when the fix is a new index, so the analyzer can test it
	// with a hypothetical index before anyone creates it.
	Index *IndexSuggestion `json:"index,omitempty"`
	// Simulation is the analyzer's what-if estimate for Index. Detectors
	// leave it empty.
	Simulation *Simulation `json:"simulation,omitempty"`
}

// IndexSuggestion describes an index to create.
type IndexSuggestion struct {
	Table string `json:"table"` // schema-qualified
	// Column definitions in order, exactly as they go in the column list:
	// "customer_id", "created_at DESC", "(lower(email))", "email gin_trgm_ops".
	Columns   []string `json:"columns"`
	Method    string   `json:"method,omitempty"`    // access method; empty means btree
	Extension string   `json:"extension,omitempty"` // extension the index needs, e.g. pg_trgm
}

// SQL renders the CREATE INDEX statement. With an empty name, PostgreSQL
// picks one.
func (s IndexSuggestion) SQL(name string, concurrently bool) string {
	var b strings.Builder
	b.WriteString("CREATE INDEX ")
	if concurrently {
		b.WriteString("CONCURRENTLY ")
	}
	if name != "" {
		b.WriteString(name + " ")
	}
	b.WriteString("ON " + s.Table + " ")
	if s.Method != "" {
		b.WriteString("USING " + s.Method + " ")
	}
	b.WriteString("(" + strings.Join(s.Columns, ", ") + ")")
	return b.String()
}

// Simulatable reports whether HypoPG can model this index. It supports
// btree, hash and brin, but not GIN or GiST, and not indexes that need an
// extension the database may not have yet.
func (s IndexSuggestion) Simulatable() bool {
	switch s.Method {
	case "", "btree", "hash", "brin":
		return s.Extension == ""
	}
	return false
}

// Simulation is a what-if estimate: the planner's cost for the query now,
// and with the suggested index in place (simulated, never created).
type Simulation struct {
	At time.Time `json:"at"`
	// Unavailable explains why no estimate could be made, e.g. the hypopg
	// extension is not installed. The other fields are then zero.
	Unavailable string  `json:"unavailable,omitempty"`
	CostBefore  float64 `json:"cost_before,omitempty"`
	CostAfter   float64 `json:"cost_after,omitempty"`
	// UsesIndex reports whether the planner would actually use the index.
	UsesIndex bool `json:"uses_index"`
	// Speedup is CostBefore / CostAfter: how many times less work the
	// planner expects. Planner cost is not time: measured speedups are
	// usually smaller, since every call also pays fixed costs such as the
	// network round trip.
	Speedup float64 `json:"speedup,omitempty"`
}

// Detector inspects one fingerprint and returns zero or more findings.
type Detector interface {
	// Name is a short, stable, kebab-case id such as "full-scan".
	Name() string
	Detect(in Input) []Finding
}

var (
	mu       sync.RWMutex
	registry = map[string]Detector{}
)

// Register adds a detector. Call it from your detector file's init().
func Register(d Detector) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[d.Name()]; dup {
		panic("detectors: duplicate detector " + d.Name())
	}
	registry[d.Name()] = d
}

// All returns every registered detector, sorted by name.
func All() []Detector {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Detector, 0, len(registry))
	for _, d := range registry {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Run applies every registered detector to in.
func Run(in Input) []Finding {
	var out []Finding
	for _, d := range All() {
		out = append(out, d.Detect(in)...)
	}
	return out
}
