package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/moseleydev/tracelet/replay"
)

const replayUsage = `Replay a recorded session against a database, to see how it performs there
or under more load than was recorded.

Usage:
  tracelet replay <session.trc.gz> --target <postgres-url> [flags]

Safety:
  --mode read      (default) replays reads only, on read-only connections.
                   PostgreSQL itself rejects any write.
  --mode rollback  replays writes too, inside transactions that are always
                   rolled back. Use a staging copy: long transactions hold locks.
  Replay never writes to the database the session was recorded from, and
  only reads from it with --allow-source.

Flags:
`

func runReplay(args []string) error {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), replayUsage)
		fs.PrintDefaults()
	}
	target := fs.String("target", "", "database to replay against (or set TRACELET_REPLAY_TARGET)")
	mode := fs.String("mode", "read", "read or rollback")
	speed := fs.Float64("speed", 1, "pace relative to the recording: 2 is twice as fast, 0 as fast as possible")
	scale := fs.Int("scale", 1, "run this many copies of every recorded connection, to simulate more traffic")
	maxConns := fs.Int("max-conns", 100, "most connections to open on the target at once")
	allowSource := fs.Bool("allow-source", false, "allow a read-only replay against the database the session was recorded from")
	save := fs.String("save", "", "write the results to this JSON file")
	compare := fs.String("compare", "", "compare with results saved earlier by --save")
	limit := fs.Int("limit", 15, "query shapes to show")

	// Accept the session file before or after the flags.
	var path string
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		path, args = args[0], args[1:]
	}
	fs.Parse(args)
	if path == "" && fs.NArg() > 0 {
		path = fs.Arg(0)
	}
	if path == "" {
		fs.Usage()
		return errors.New("replay: which session? Recorded sessions are in .tracelet/sessions")
	}
	if *target == "" {
		*target = os.Getenv("TRACELET_REPLAY_TARGET")
	}

	var previous *replay.Result
	if *compare != "" {
		raw, err := os.ReadFile(*compare)
		if err != nil {
			return fmt.Errorf("replay: %w", err)
		}
		previous = &replay.Result{}
		if err := json.Unmarshal(raw, previous); err != nil {
			return fmt.Errorf("replay: %s: %w", *compare, err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("replaying %s  mode %s  speed %s  scale %dx\n", path, *mode, speedLabel(*speed), *scale)
	res, err := replay.Run(ctx, path, replay.Options{
		Target: *target, Mode: replay.Mode(*mode), Speed: *speed, Scale: *scale,
		MaxConns: *maxConns, AllowSource: *allowSource,
		Progress: func(p replay.Progress) {
			fmt.Fprintf(os.Stderr, "\r  %d / %d queries, %d errors   ", p.Done, p.Total, p.Errors)
		},
	})
	fmt.Fprint(os.Stderr, "\r                                                  \r")
	if res == nil {
		return fmt.Errorf("replay: %w", err)
	}
	if err != nil {
		fmt.Println("stopped early:", err)
	}

	printReplay(res, previous, *limit, isTerminal(os.Stdout))
	if *save != "" {
		raw, _ := json.MarshalIndent(res, "", "  ")
		if err := os.WriteFile(*save, raw, 0o600); err != nil {
			return fmt.Errorf("replay: save: %w", err)
		}
		fmt.Printf("\nResults saved to %s. Compare a later run with --compare %s\n", *save, *save)
	}
	return nil
}

func speedLabel(s float64) string {
	if s <= 0 {
		return "max"
	}
	return fmt.Sprintf("%gx", s)
}

func printReplay(res *replay.Result, previous *replay.Result, limit int, color bool) {
	paint := func(code, s string) string {
		if !color {
			return s
		}
		return code + s + ansiReset
	}
	prev := map[string]replay.Timing{}
	if previous != nil {
		for _, q := range previous.Queries {
			prev[q.Hash] = q.Replayed
		}
	}

	fmt.Printf("\nReplayed %d queries in %s (recorded over %s)", res.Sent, fmtDur(res.Elapsed), fmtDur(res.Recorded))
	if res.Errors > 0 {
		fmt.Print(", ", paint(ansiRed, fmt.Sprintf("%d errors", res.Errors)))
	}
	fmt.Println()
	for reason, n := range res.Skipped {
		fmt.Printf("  skipped %d: %s\n", n, reason)
	}
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	header := "CALLS\tRECORDED p95\tREPLAY p95\tCHANGE\t"
	if previous != nil {
		header = "CALLS\tRECORDED p95\tPREVIOUS p95\tREPLAY p95\tVS PREVIOUS\t"
	}
	fmt.Fprintln(w, header)
	shown := 0
	for _, q := range res.Queries {
		if q.Replayed.Count == 0 {
			continue
		}
		if shown == limit {
			break
		}
		shown++
		change := ratio(q.Recorded.P95, q.Replayed.P95, paint)
		row := fmt.Sprintf("%d\t%s\t%s\t%s\t", q.Replayed.Count, fmtDur(q.Recorded.P95), fmtDur(q.Replayed.P95), change)
		if previous != nil {
			p := prev[q.Hash]
			row = fmt.Sprintf("%d\t%s\t%s\t%s\t%s\t", q.Replayed.Count, fmtDur(q.Recorded.P95), fmtDur(p.P95), fmtDur(q.Replayed.P95), ratio(p.P95, q.Replayed.P95, paint))
		}
		fmt.Fprintf(w, "%s  %s\n", row, oneLine(q.Query, 80))
		if q.Errors > 0 {
			fmt.Fprintf(w, "\t\t\t\t  %s\n", paint(ansiRed, fmt.Sprintf("%d failed: %s", q.Errors, oneLine(q.LastError, 90))))
		}
	}
	w.Flush()
}

// ratio describes how latency changed: "2.4x slower", "3.1x faster".
func ratio(before, after time.Duration, paint func(string, string) string) string {
	if before <= 0 || after <= 0 {
		return "-"
	}
	r := float64(after) / float64(before)
	switch {
	case r >= 1.2:
		return paint(ansiRed, fmt.Sprintf("%.1fx slower", r))
	case r <= 1/1.2:
		return paint(ansiGreen, fmt.Sprintf("%.1fx faster", 1/r))
	}
	return "same"
}
