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
	public := fs.Bool("studio-public", false, "allow serving Studio on a non-local address (it has no login)")
	fs.Parse(args)

	if err := checkStudioAddr(*addr, *public); err != nil {
		return fmt.Errorf("studio: %w", err)
	}

	if !store.Exists(*dataPath) {
		return fmt.Errorf("studio: no data at %s; run \"tracelet up\" first", *dataPath)
	}
	st, err := store.Open(*dataPath)
	if err != nil {
		return fmt.Errorf("studio: %w", err)
	}
	defer st.Close()

	ui, err := startStudio(*addr, studio.New(st, nil, nil, studio.Info{
		Version:   version,
		Data:      *dataPath,
		StartedAt: time.Now(),
		Public:    *public,
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

// checkStudioAddr refuses to expose Studio beyond this machine unless the
// user asked for it, and warns when they did.
func checkStudioAddr(addr string, public bool) error {
	if addr == "" || studio.IsLoopback(addr) {
		return nil
	}
	if !public {
		return fmt.Errorf("refusing to serve Studio on %s: it has no login, so anyone who can reach it would see your queries. "+
			"Use a local address such as 127.0.0.1:4000, or add --studio-public if you really mean it", addr)
	}
	fmt.Fprintf(os.Stderr, "tracelet: warning: Studio is served on %s without a login. Anyone who can reach it can see your query data.\n", addr)
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
