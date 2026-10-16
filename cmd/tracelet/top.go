package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/moseleydev/tracelet/analyzer"
	"github.com/moseleydev/tracelet/core"
	"github.com/moseleydev/tracelet/core/store"
	"github.com/moseleydev/tracelet/detectors"
)

func runTop(args []string) error {
	fs := flag.NewFlagSet("top", flag.ExitOnError)
	dataPath := fs.String("data", store.DefaultPath, "data file written by tracelet up")
	since := fs.Duration("since", time.Hour, "how far back to look; 0 for all data")
	limit := fs.Int("limit", 10, "number of queries to show")
	fs.Parse(args)

	if !store.Exists(*dataPath) {
		return fmt.Errorf("top: no data at %s; run \"tracelet up\" first", *dataPath)
	}
	st, err := store.Open(*dataPath)
	if err != nil {
		return fmt.Errorf("top: %w", err)
	}
	defer st.Close()

	from := time.Time{}
	if *since > 0 {
		from = time.Now().Add(-*since)
	}
	top, err := st.Top(from, *limit)
	if err != nil {
		return fmt.Errorf("top: %w", err)
	}
	if len(top) == 0 {
		fmt.Println("No queries recorded in this window. Try --since 0.")
		return nil
	}
	printTop(top, isTerminal(os.Stdout))
	printFindings(st, from, isTerminal(os.Stdout))
	return nil
}

func printTop(stats []core.Stats, color bool) {
	paint := func(code, s string) string {
		if !color {
			return s
		}
		return code + s + ansiReset
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "TOTAL\tCALLS\tMEAN\tP95\tMAX\tROWS\tERRORS\t")
	for _, s := range stats {
		errs := "-"
		if s.Errors > 0 {
			errs = fmt.Sprint(s.Errors)
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%.0f\t%s\t  %s\n",
			fmtDur(s.Total), s.Count, fmtDur(s.Mean), fmtDur(s.P95), fmtDur(s.Max),
			s.MeanRows, errs, oneLine(s.Fingerprint.Query, 90))
	}
	w.Flush()

	// Severity color for the slowest query, so the eye lands on it.
	if len(stats) > 0 && stats[0].P95 >= 100*time.Millisecond {
		fmt.Println(paint(ansiRed, fmt.Sprintf("\n%s is slow at p95 %s, touching %s.",
			"The top query", fmtDur(stats[0].P95), tableList(stats[0].Fingerprint.Tables))))
	}
}

// printFindings lists the analyzer's suggestions, most severe first.
func printFindings(st *store.Store, since time.Time, color bool) {
	sums, err := analyzer.Summaries(st, since)
	if err != nil || len(sums) == 0 {
		return
	}
	paint := func(code, s string) string {
		if !color {
			return s
		}
		return code + s + ansiReset
	}
	fmt.Printf("\nSuggestions (%d):\n", len(sums))
	for _, s := range sums {
		label := map[detectors.Severity]string{
			detectors.Critical: paint(ansiRed, "● critical"),
			detectors.Warning:  paint(ansiYellow, "● warning "),
			detectors.Info:     paint(ansiDim, "● info    "),
		}[s.Severity]
		fmt.Printf("\n%s  %s  %s\n", label, s.Title, paint(ansiDim, fmt.Sprintf("(%.0f%% of time)", s.ShareOfTime*100)))
		fmt.Printf("   %s\n", oneLine(s.Stats.Fingerprint.Query, 100))
		if s.Fix != "" {
			for _, line := range strings.Split(s.Fix, "\n") {
				fmt.Printf("   %s\n", paint(ansiGreen, line))
			}
		}
		if sim := s.Simulation; sim != nil {
			switch {
			case sim.Unavailable != "":
				fmt.Printf("   %s\n", paint(ansiDim, "what-if: "+sim.Unavailable))
			case sim.UsesIndex && sim.Speedup > 0:
				fmt.Printf("   what-if: ~%s less work per call, by planner estimate %s\n", fmtFactor(sim.Speedup),
					paint(ansiDim, fmt.Sprintf("(cost %.0f -> %.1f with a HypoPG hypothetical index; real gains are usually smaller)", sim.CostBefore, sim.CostAfter)))
			default:
				fmt.Printf("   %s\n", paint(ansiYellow, "what-if: PostgreSQL would not use this index for this query"))
			}
		}
	}
}

// fmtFactor prints a speedup: 213 -> "213x", 4.26 -> "4.3x".
func fmtFactor(x float64) string {
	if x >= 10 {
		return fmt.Sprintf("%.0fx", x)
	}
	return fmt.Sprintf("%.1fx", x)
}

func tableList(tables []string) string {
	if len(tables) == 0 {
		return "no tables"
	}
	return strings.Join(tables, ", ")
}

// fmtDur prints a duration with precision that suits its size.
func fmtDur(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
	case d < time.Second:
		return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
	case d < time.Minute:
		return fmt.Sprintf("%.2fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}
