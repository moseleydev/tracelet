package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/moseleydev/tracelet/core/store"
	"github.com/moseleydev/tracelet/studio"
)

// runStudio serves Tracelet Studio over saved data, with no live proxy.
func runStudio(args []string) error {
	fs := flag.NewFlagSet("studio", flag.ExitOnError)
	dataPath := fs.String("data", store.DefaultPath, "data file written by tracelet up")
	addr := fs.String("studio", "127.0.0.1:4000", "address to serve Tracelet Studio on")
	openBrowser := fs.Bool("open", true, "open Tracelet Studio in your browser")
	fs.Parse(args)

	if !store.Exists(*dataPath) {
		return fmt.Errorf("studio: no data at %s; run \"tracelet up\" first", *dataPath)
	}
	st, err := store.Open(*dataPath)
	if err != nil {
		return fmt.Errorf("studio: %w", err)
	}
	defer st.Close()

	ui, err := startStudio(*addr, studio.New(st, nil, studio.Info{
		Version:   version,
		Data:      *dataPath,
		StartedAt: time.Now(),
	}), nil)
	if err != nil {
		return fmt.Errorf("studio: %w", err)
	}
	defer ui.stop()

	fmt.Printf("tracelet studio  %s  (saved data from %s)\n", ui.url, *dataPath)
	fmt.Println("press Ctrl+C to stop")
	if *openBrowser {
		openURL(ui.url)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	return nil
}

type studioServer struct {
	url  string
	stop func()
}

// startStudio serves h on addr. If hub is non-nil it is started too, and
// stopped before the HTTP server so open live streams end promptly.
func startStudio(addr string, h http.Handler, hub *studio.Hub) (*studioServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "tracelet: studio:", err)
		}
	}()

	hubStop := make(chan struct{})
	if hub != nil {
		go hub.Run(hubStop)
	}
	return &studioServer{
		url: "http://" + ln.Addr().String(),
		stop: func() {
			close(hubStop)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			srv.Shutdown(ctx)
		},
	}, nil
}

// openURL opens url in the default browser, best effort.
func openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}
