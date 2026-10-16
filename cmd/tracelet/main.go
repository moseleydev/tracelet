// Command tracelet is the Tracelet CLI: it runs the capture proxy and serves
// Tracelet Studio.
package main

import (
	"fmt"
	"net/http"
	_ "net/http/pprof" // registered on the default mux, served only with TRACELET_PPROF
	"os"
	"runtime/debug"
)

// version is overwritten at release time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `Tracelet: see every query your app sends to its database.

Usage:
  tracelet <command> [flags]

Commands:
  up        Start the capture proxy and Tracelet Studio
  top       Show the queries that cost the most time
  studio    Open Tracelet Studio on saved data, without the proxy
  replay    Replay a recorded session against a database (record with "up --record" or from Studio)
  version   Print the Tracelet version

Run "tracelet <command> -h" for command flags.
`

func main() {
	// Tracelet keeps little live data (about 10 MB), so the default GC
	// target makes the collector run many times a second under load, and
	// each cycle borrows time from the goroutines relaying queries. Letting
	// the heap grow 4x between cycles cuts that by about 4x for a few tens of
	// MB of memory, with a hard ceiling. GOGC / GOMEMLIMIT still win if set.
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(400)
	}
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(512 << 20)
	}

	// TRACELET_PPROF=127.0.0.1:6060 serves Go's profiler, for finding
	// performance problems in Tracelet itself. Off unless set.
	if addr := os.Getenv("TRACELET_PPROF"); addr != "" {
		go func() { fmt.Fprintln(os.Stderr, "tracelet: pprof:", http.ListenAndServe(addr, nil)) }()
	}

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "up":
		err = runUp(args)
	case "top":
		err = runTop(args)
	case "studio":
		err = runStudio(args)
	case "replay":
		err = runReplay(args)
	case "version", "-v", "--version":
		fmt.Println("tracelet", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "tracelet: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tracelet:", err)
		os.Exit(1)
	}
}
