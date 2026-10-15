// Command tracelet is the Tracelet CLI: it runs the capture proxy and serves
// Tracelet Studio.
package main

import (
	"flag"
	"fmt"
	"os"
)

// version is overwritten at release time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `Tracelet: see every query your app sends to its database.

Usage:
  tracelet <command> [flags]

Commands:
  up        Start the capture proxy and Tracelet Studio
  studio    Open Tracelet Studio for an existing session
  record    Record traffic to a .session file
  replay    Replay a .session file against a target database
  version   Print the Tracelet version

Run "tracelet <command> -h" for command flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "up":
		err = runUp(args)
	case "studio", "record", "replay":
		err = fmt.Errorf("%q is not implemented yet, see docs/ROADMAP.md", cmd)
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

func runUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	db := fs.String("db", "", "upstream database URL, e.g. postgres://user:pass@localhost:5432/app")
	listen := fs.String("listen", "127.0.0.1:6433", "address the proxy listens on; point your app here")
	studio := fs.String("studio", "127.0.0.1:4000", "address Tracelet Studio is served on")
	fs.Parse(args)

	if *db == "" {
		return fmt.Errorf("up: --db is required")
	}
	_ = listen
	_ = studio
	return fmt.Errorf("up: the capture proxy lands in Phase 1, see docs/ROADMAP.md")
}
