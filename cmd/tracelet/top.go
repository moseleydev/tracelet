package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/moseleydev/tracelet/core"
	"github.com/moseleydev/tracelet/core/store"
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
