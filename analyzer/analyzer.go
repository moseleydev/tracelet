package analyzer

import (
	"context"
	"sort"
	"time"

	"github.com/moseleydev/tracelet/core"
	"github.com/moseleydev/tracelet/core/store"
	"github.com/moseleydev/tracelet/detectors"
)

// Analyzer periodically explains the costliest queries and refreshes the
// findings shown in the CLI and studio.
type Analyzer struct {
	Store *store.Store
	// DatabaseURL is the real database, not the proxy.
	DatabaseURL string
	// Interval between analysis passes. Defaults to 15s.
	Interval time.Duration
	// Window is how far back stats are considered. Defaults to 15 minutes.
	Window time.Duration
	Logf   func(format string, args ...any)

	explainer *Explainer
	lastErr   string
}

const (
	maxCandidates     = 50              // only the top queries by total time
	explainsPerPass   = 5               // keep the load on the database tiny
	replanAfter       = 2 * time.Minute // notice new indexes soon after they are added
	retryFailedAfter  = 30 * time.Minute
	minCallsToExplain = 3
)

// Run analyzes every Interval until ctx is cancelled.
func (a *Analyzer) Run(ctx context.Context) {
	if a.Interval == 0 {
		a.Interval = 15 * time.Second
	}
	if a.Window == 0 {
		a.Window = 15 * time.Minute
	}
	if a.Logf == nil {
		a.Logf = func(string, ...any) {}
	}
	defer func() {
		if a.explainer != nil {
			a.explainer.Close(context.Background())
		}
	}()

	timer := time.NewTimer(3 * time.Second) // let some traffic arrive first
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := a.Pass(ctx); err != nil && ctx.Err() == nil {
				a.report(err)
			}
			timer.Reset(a.Interval)
		}
	}
}

// report logs an error once, not on every pass.
func (a *Analyzer) report(err error) {
	if msg := err.Error(); msg != a.lastErr {
		a.lastErr = msg
		a.Logf("analyzer: %v", err)
	}
}

// Pass runs one round: explain the queries that need it, then run the
// detectors on every candidate and store their findings.
func (a *Analyzer) Pass(ctx context.Context) error {
	now := time.Now()
	since := now.Add(-a.Window)
	all, err := a.Store.Top(since, 0)
	if err != nil {
		return err
	}
	var grand time.Duration
	for _, s := range all {
		grand += s.Total
	}
	candidates := all
	if len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}

	fps, err := a.Store.Fingerprints()
	if err != nil {
		return err
	}
	plans, err := a.Store.Plans()
	if err != nil {
		return err
	}
	bursts, err := a.Store.Bursts(since)
	if err != nil {
		return err
	}

	// Explain the costliest queries whose plan is missing or stale.
	budget := explainsPerPass
	var connErr error
	for _, s := range candidates {
		if budget == 0 || connErr != nil {
			break
		}
		fp := fps[s.Fingerprint.Hash]
		if s.Count < minCallsToExplain || !Explainable(fp.Template) {
			continue
		}
		if rec, ok := plans[fp.Hash]; ok {
			age := now.Sub(rec.ExplainedAt)
			if (rec.Error == "" && age < replanAfter) || (rec.Error != "" && age < retryFailedAfter) {
				continue
			}
		}
		x, err := a.connect(ctx)
		if err != nil {
			connErr = err
			break
		}
		budget--
		rec := store.PlanRecord{Hash: fp.Hash, ExplainedAt: now}
		rec.Plan, err = x.Explain(ctx, fp.Template)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			rec.Error = err.Error()
			if x.conn.IsClosed() {
				a.explainer = nil // reconnect next time
			}
		}
		if err := a.Store.SavePlan(rec); err != nil {
			return err
		}
		plans[fp.Hash] = rec
	}

	// Run the detectors and replace each candidate's findings.
	evaluated := map[string]bool{}
	for _, s := range candidates {
		hash := s.Fingerprint.Hash
		evaluated[hash] = true
		s.Fingerprint = fps[hash] // includes the template
		in := detectors.Input{Stats: s, Plan: plans[hash].Plan}
		if grand > 0 {
			in.ShareOfTime = float64(s.Total) / float64(grand)
		}
		if b, ok := bursts[hash]; ok {
			in.Bursts = &b
			if p, ok := fps[b.ParentHash]; ok {
				in.Parent = &p
			}
		}
		if err := a.Store.ReplaceFindings(hash, detectors.Run(in)); err != nil {
			return err
		}
	}

	// Drop findings for queries that left the window.
	existing, err := a.Store.Findings()
	if err != nil {
		return err
	}
	for _, f := range existing {
		if !evaluated[f.Hash] {
			if err := a.Store.ReplaceFindings(f.Hash, nil); err != nil {
				return err
			}
		}
	}
	if connErr != nil {
		return connErr
	}
	a.lastErr = ""
	return nil
}

func (a *Analyzer) connect(ctx context.Context) (*Explainer, error) {
	if a.explainer != nil {
		return a.explainer, nil
	}
	x, err := Connect(ctx, a.DatabaseURL)
	if err != nil {
		return nil, err
	}
	a.explainer = x
	return x, nil
}

// Summary is a finding with the stats of the query it is about, for display.
type Summary struct {
	store.StoredFinding
	Stats       core.Stats `json:"stats"`
	ShareOfTime float64    `json:"share_of_time"`
}

// Summaries joins current findings with query stats over the window, most
// severe and most costly first.
func Summaries(st *store.Store, since time.Time) ([]Summary, error) {
	findings, err := st.Findings()
	if err != nil {
		return nil, err
	}
	top, err := st.Top(since, 0)
	if err != nil {
		return nil, err
	}
	byHash := map[string]core.Stats{}
	var grand time.Duration
	for _, s := range top {
		byHash[s.Fingerprint.Hash] = s
		grand += s.Total
	}
	out := make([]Summary, 0, len(findings))
	for _, f := range findings {
		s, ok := byHash[f.Hash]
		if !ok {
			continue
		}
		sum := Summary{StoredFinding: f, Stats: s}
		if grand > 0 {
			sum.ShareOfTime = float64(s.Total) / float64(grand)
		}
		out = append(out, sum)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity > out[j].Severity
		}
		return out[i].Stats.Total > out[j].Stats.Total
	})
	return out, nil
}
