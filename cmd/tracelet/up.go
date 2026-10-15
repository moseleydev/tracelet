package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/moseleydev/tracelet/core"
	"github.com/moseleydev/tracelet/proxy"
	"github.com/moseleydev/tracelet/proxy/postgres"
)

func runUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	db := fs.String("db", "", "upstream database URL, e.g. postgres://user:pass@localhost:5432/app")
	listen := fs.String("listen", "127.0.0.1:6433", "address the proxy listens on; point your app here")
	captureParams := fs.Bool("capture-params", false, "record bound parameter values (may contain personal data)")
	quiet := fs.Bool("quiet", false, "do not print each query")
	fs.Parse(args)

	if *db == "" {
		return fmt.Errorf("up: --db is required")
	}
	u, err := url.Parse(*db)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return fmt.Errorf("up: --db must be a postgres:// URL")
	}
	host, port := u.Hostname(), u.Port()
	if host == "" {
		host = "localhost"
	}
	if port == "" {
		port = "5432"
	}

	sink := proxy.NewSink(8192)
	srv, err := postgres.New(postgres.Config{
		Upstream:      net.JoinHostPort(host, port),
		SSLMode:       u.Query().Get("sslmode"),
		CaptureParams: *captureParams,
		Sink:          sink,
		Logf:          log.New(os.Stderr, "tracelet: ", 0).Printf,
	})
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("up: %w", err)
	}

	appURL := *u
	appURL.Host = ln.Addr().String()
	fmt.Printf("tracelet proxy  %s → %s\n", ln.Addr(), net.JoinHostPort(host, port))
	fmt.Printf("point your app at  %s\n\n", appURL.Redacted())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	served := make(chan error, 1)
	go func() {
		served <- srv.Serve(ctx, ln)
		sink.Close()
	}()

	color := isTerminal(os.Stdout)
	var count int64
	for e := range sink.Events() {
		count++
		if !*quiet {
			printEvent(e, color)
		}
	}

	fmt.Printf("\n%d queries captured", count)
	if d := sink.Dropped(); d > 0 {
		fmt.Printf(", %d dropped (consumer too slow)", d)
	}
	fmt.Println()
	return <-served
}

const (
	ansiReset  = "\033[0m"
	ansiDim    = "\033[2m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
)

func printEvent(e core.Event, color bool) {
	paint := func(code, s string) string {
		if !color {
			return s
		}
		return code + s + ansiReset
	}

	ms := float64(e.Duration) / float64(time.Millisecond)
	latency := fmt.Sprintf("%8.2fms", ms)
	switch {
	case ms >= 100:
		latency = paint(ansiRed, latency)
	case ms >= 10:
		latency = paint(ansiYellow, latency)
	default:
		latency = paint(ansiGreen, latency)
	}

	detail := fmt.Sprintf("%6d rows", e.Rows)
	if e.Error != "" {
		detail = paint(ansiRed, " ERROR    ")
	}

	fmt.Printf("%s %s %s  %s\n",
		paint(ansiDim, e.StartedAt.Format("15:04:05.000")), latency, detail, oneLine(e.Query, 120))
	if e.Error != "" {
		fmt.Printf("%s %s\n", strings.Repeat(" ", 34), paint(ansiRed, e.Error))
	}
	if len(e.Params) > 0 {
		fmt.Printf("%s %s\n", strings.Repeat(" ", 34), paint(ansiDim, "params: "+strings.Join(e.Params, ", ")))
	}
}

// oneLine collapses whitespace and truncates q to at most max runes.
func oneLine(q string, max int) string {
	q = strings.Join(strings.Fields(q), " ")
	if r := []rune(q); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return q
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
