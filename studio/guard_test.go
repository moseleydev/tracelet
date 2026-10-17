package studio

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:4000": true, "localhost:4000": true, "[::1]:4000": true, "127.0.0.2:1": true,
		":4000": false, "0.0.0.0:4000": false, "192.168.1.5:4000": false, "example.com:4000": false,
	} {
		if got := IsLoopback(addr); got != want {
			t.Errorf("IsLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestGuardBlocksDNSRebinding(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	cases := []struct {
		name, method, host, origin string
		public                     bool
		want                       int
	}{
		{"local GET", "GET", "127.0.0.1:4000", "", false, 200},
		{"localhost GET", "GET", "localhost:4000", "", false, 200},
		{"ipv6 GET", "GET", "[::1]:4000", "", false, 200},
		{"rebound domain", "GET", "evil.example:4000", "", false, 403},
		{"rebound domain, public", "GET", "evil.example:4000", "", true, 200},
		{"cross-site POST", "POST", "127.0.0.1:4000", "https://evil.example", false, 403},
		{"same-origin POST", "POST", "127.0.0.1:4000", "http://127.0.0.1:4000", false, 200},
		{"cross-site POST, public", "POST", "studio.lan:4000", "https://evil.example", true, 403},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "http://x/api/info", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()
		guard(ok, c.public).ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, w.Code, c.want)
		}
	}
}
