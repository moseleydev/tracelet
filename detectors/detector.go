// Package detectors contains the rules that turn query stats into
// recommendations.
//
// Each detector lives in its own file and registers itself in init(). Adding a
// detector should never require touching any other part of Tracelet; see
// CONTRIBUTING.md for a walkthrough.
package detectors

import (
	"sort"
	"sync"

	"github.com/moseleydev/tracelet/core"
)

// Severity ranks how urgently a finding should be addressed.
type Severity int

const (
	Info Severity = iota
	Warning
	Critical
)

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
	// Events from a single trace (same TraceID) that contained this
	// fingerprint. Empty when the app has no SDK context.
	Trace []core.Event
	// Plan is the EXPLAIN output for this fingerprint, if one was collected.
	Plan *Plan
}

// Plan is an engine-neutral query plan tree.
type Plan struct {
	Node      string  `json:"node"` // e.g. "Seq Scan", "Index Scan", "COLLSCAN"
	Relation  string  `json:"relation,omitempty"`
	Filter    string  `json:"filter,omitempty"`
	EstRows   float64 `json:"est_rows"`
	TotalCost float64 `json:"total_cost"`
	Children  []*Plan `json:"children,omitempty"`
}

// Finding is a single recommendation shown in the studio.
type Finding struct {
	Detector    string   `json:"detector"`
	Severity    Severity `json:"severity"`
	Title       string   `json:"title"`         // "Full table scan on users"
	Explanation string   `json:"explanation"`   // why it is slow, in plain words
	Suggestion  string   `json:"suggestion"`    // what to do
	Fix         string   `json:"fix,omitempty"` // ready-to-run SQL, when possible
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
