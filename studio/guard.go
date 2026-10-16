package studio

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// IsLoopback reports whether a listen address only accepts connections from
// this machine ("127.0.0.1:4000", "localhost:4000", "[::1]:4000").
// ":4000" and "0.0.0.0:4000" listen on every interface, so they are not.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isLocalHostname(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// guard protects Studio, which has no login, when it is served on localhost.
//
//   - Host check: a malicious web page can point its own domain at
//     127.0.0.1 (DNS rebinding) and then read Studio's API as a same-origin
//     page. Requests must name a local host, so that trick fails.
//   - Origin check: any request that changes something must come from a
//     Studio page, not from another site the browser has open.
//
// With public set (--studio-public), the Host check is skipped; the
// Origin check still applies.
func guard(next http.Handler, public bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !public && !isLocalHostname(r.Host) {
			http.Error(w, "Tracelet Studio only answers to localhost. "+
				"To serve it on a network address, start it with --studio-public.", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) {
					http.Error(w, "cross-origin request refused", http.StatusForbidden)
					return
				}
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
