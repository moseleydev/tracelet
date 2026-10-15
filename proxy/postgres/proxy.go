// Package postgres is a transparent proxy for the PostgreSQL wire protocol
// (v3). It forwards every byte between the application and the database
// unchanged and emits a core.Event for each query it observes.
package postgres

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moseleydev/tracelet/proxy"
)

// Config configures a Server.
type Config struct {
	// Upstream is the real database address, host:port.
	Upstream string
	// SSLMode follows libpq semantics for the proxy-to-database leg:
	// disable, prefer (default), require, verify-ca or verify-full.
	// The app-to-proxy leg is always plain TCP on a local address.
	SSLMode string
	// ServerName is the hostname checked by verify-full. Defaults to the
	// host part of Upstream.
	ServerName string
	// CaptureParams records bound parameter values. Off by default because
	// parameters often contain personal data.
	CaptureParams bool
	// Sink receives the captured events. Required.
	Sink *proxy.Sink
	// Logf reports connection-level problems. Optional.
	Logf func(format string, args ...any)
}

// Server accepts application connections and proxies them to Upstream.
type Server struct {
	cfg      Config
	tlsCfg   *tls.Config
	connSeq  atomic.Uint64
	eventSeq atomic.Uint64
}

const (
	maxStartupLen = 10_000
	maxMessageLen = 1 << 30 // PostgreSQL's own limit on a single message

	sslRequestCode    = 80877103
	gssEncRequestCode = 80877104
	cancelRequestCode = 80877102

	dialTimeout    = 10 * time.Second
	startupTimeout = 30 * time.Second
	bufSize        = 32 << 10
)

// New validates cfg and returns a Server.
func New(cfg Config) (*Server, error) {
	if cfg.Sink == nil {
		return nil, errors.New("postgres: Config.Sink is required")
	}
	if cfg.Upstream == "" {
		return nil, errors.New("postgres: Config.Upstream is required")
	}
	host, _, err := net.SplitHostPort(cfg.Upstream)
	if err != nil {
		return nil, fmt.Errorf("postgres: upstream %q: %w", cfg.Upstream, err)
	}
	if cfg.ServerName == "" {
		cfg.ServerName = host
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}

	s := &Server{cfg: cfg}
	switch cfg.SSLMode {
	case "disable":
	case "", "prefer", "require":
		s.tlsCfg = &tls.Config{InsecureSkipVerify: true} // libpq: encrypt, don't verify
	case "verify-ca":
		s.tlsCfg = &tls.Config{InsecureSkipVerify: true, VerifyConnection: verifyChainOnly}
	case "verify-full":
		s.tlsCfg = &tls.Config{ServerName: cfg.ServerName}
	default:
		return nil, fmt.Errorf("postgres: unsupported sslmode %q", cfg.SSLMode)
	}
	return s, nil
}

// verifyChainOnly checks the certificate chain without checking the hostname,
// which is what libpq's verify-ca does.
func verifyChainOnly(cs tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("database presented no certificate")
	}
	opts := x509.VerifyOptions{Intermediates: x509.NewCertPool()}
	for _, c := range cs.PeerCertificates[1:] {
		opts.Intermediates.AddCert(c)
	}
	_, err := cs.PeerCertificates[0].Verify(opts)
	return err
}

// Serve accepts connections on ln until ctx is cancelled, then closes every
// open connection and returns once all of them have finished.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		conns = map[net.Conn]struct{}{}
	)
	stop := context.AfterFunc(ctx, func() {
		ln.Close()
		mu.Lock()
		for c := range conns {
			c.Close()
		}
		mu.Unlock()
	})
	defer stop()

	for {
		c, err := ln.Accept()
		if err != nil {
			wg.Wait()
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		mu.Lock()
		conns[c] = struct{}{}
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handle(c)
			mu.Lock()
			delete(conns, c)
			mu.Unlock()
		}()
	}
}

func (s *Server) handle(client net.Conn) {
	defer client.Close()
	id := s.connSeq.Add(1)

	upstream, err := s.startup(client)
	if err != nil {
		s.cfg.Logf("conn %d: %v", id, err)
		return
	}
	if upstream == nil {
		return // cancel request, already forwarded
	}
	defer upstream.Close()

	t := newTracker(s, id)
	errc := make(chan error, 2)
	go func() { errc <- pump(client, upstream, frontendInspected, t.frontend) }()
	go func() { errc <- pump(upstream, client, backendInspected, t.backend) }()
	err = <-errc
	client.Close()
	upstream.Close()
	<-errc
	if err != nil && !isClosed(err) {
		s.cfg.Logf("conn %d: %v", id, err)
	}
}

// startup handles the untyped messages that open a connection: SSL and GSS
// negotiation, cancel requests and the startup message itself. It returns
// the upstream connection, or nil when the client only sent a cancel request.
func (s *Server) startup(client net.Conn) (net.Conn, error) {
	client.SetDeadline(time.Now().Add(startupTimeout))
	defer client.SetDeadline(time.Time{})

	for {
		pkt, err := readStartupPacket(client)
		if err != nil {
			return nil, fmt.Errorf("startup: %w", err)
		}
		code := binary.BigEndian.Uint32(pkt[4:8])
		switch {
		case code == sslRequestCode, code == gssEncRequestCode:
			// The app-to-proxy leg is local and plain; tell the client to
			// carry on without encryption.
			if _, err := client.Write([]byte{'N'}); err != nil {
				return nil, err
			}
		case code == cancelRequestCode:
			up, err := s.dialUpstream()
			if err != nil {
				return nil, fmt.Errorf("cancel request: %w", err)
			}
			_, err = up.Write(pkt)
			up.Close()
			return nil, err
		case code>>16 == 3:
			up, err := s.dialUpstream()
			if err != nil {
				return nil, err
			}
			if _, err := up.Write(pkt); err != nil {
				up.Close()
				return nil, err
			}
			return up, nil
		default:
			return nil, fmt.Errorf("unsupported protocol version %d.%d", code>>16, code&0xffff)
		}
	}
}

func readStartupPacket(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n < 8 || n > maxStartupLen {
		return nil, fmt.Errorf("invalid startup packet length %d", n)
	}
	pkt := make([]byte, n)
	copy(pkt, hdr[:])
	if _, err := io.ReadFull(r, pkt[4:]); err != nil {
		return nil, err
	}
	return pkt, nil
}

func (s *Server) dialUpstream() (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", s.cfg.Upstream, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	if s.tlsCfg == nil {
		return conn, nil
	}

	conn.SetDeadline(time.Now().Add(dialTimeout))
	defer conn.SetDeadline(time.Time{})

	req := []byte{0, 0, 0, 8, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(req[4:], sslRequestCode)
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}
	// Read exactly one byte, unbuffered, so nothing injected before the TLS
	// handshake can be mistaken for encrypted traffic.
	var resp [1]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		conn.Close()
		return nil, err
	}
	switch resp[0] {
	case 'S':
		tc := tls.Client(conn, s.tlsCfg)
		if err := tc.Handshake(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("tls handshake with database: %w", err)
		}
		return tc, nil
	case 'N':
		if s.cfg.SSLMode == "" || s.cfg.SSLMode == "prefer" {
			return conn, nil
		}
		conn.Close()
		return nil, fmt.Errorf("database does not support SSL but sslmode=%s", s.cfg.SSLMode)
	default:
		conn.Close()
		return nil, fmt.Errorf("unexpected response %q to SSL request", resp[0])
	}
}

// Message types whose bodies the tracker reads. Everything else, including
// DataRow (the bulk of the traffic), is streamed through without copying.
var frontendInspected = typeSet('Q', 'P', 'B', 'E', 'S', 'C')
var backendInspected = typeSet('1', '2', 'C', 'E', 'Z', 'I', 's')

func typeSet(types ...byte) *[256]bool {
	var set [256]bool
	for _, t := range types {
		set[t] = true
	}
	return &set
}

// pump copies typed protocol messages from src to dst, passing the bodies of
// inspected message types to inspect. Output is flushed whenever src has
// nothing more buffered, so batching never adds latency.
func pump(src, dst net.Conn, inspected *[256]bool, inspect func(typ byte, body []byte)) error {
	r := bufio.NewReaderSize(src, bufSize)
	w := bufio.NewWriterSize(dst, bufSize)
	var hdr [5]byte
	var buf []byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return err
		}
		typ := hdr[0]
		n := int64(binary.BigEndian.Uint32(hdr[1:])) - 4
		if n < 0 || n > maxMessageLen {
			return fmt.Errorf("invalid length %d for message %q", n+4, typ)
		}
		if _, err := w.Write(hdr[:]); err != nil {
			return err
		}
		if inspected[typ] {
			if int64(cap(buf)) < n {
				buf = make([]byte, n)
			}
			body := buf[:n]
			if _, err := io.ReadFull(r, body); err != nil {
				return err
			}
			inspect(typ, body)
			if _, err := w.Write(body); err != nil {
				return err
			}
		} else if _, err := io.CopyN(w, r, n); err != nil {
			return err
		}
		if r.Buffered() == 0 {
			if err := w.Flush(); err != nil {
				return err
			}
		}
	}
}

func isClosed(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrUnexpectedEOF)
}
