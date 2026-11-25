// Command devproxy is the development front door (D17, FR-096): one origin that sends
// API, event-socket, webhook and llms.txt requests to the Go backend and everything
// else under /admin (pages, `next dev` assets, HMR) to the Next dev server.
//
// Next `rewrites` are not supported together with `output: 'export'`, so the split
// happens here. It is NOT part of the production binary: nothing imports this package.
//
//	go run ./cmd/devproxy -listen 127.0.0.1:8080 -backend http://127.0.0.1:9000 -frontend http://127.0.0.1:3000
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

// backendPrefixes are the path prefixes served by the Go backend. A prefix ending in
// "/" matches that subtree; any other entry matches the exact path only.
var backendPrefixes = []string{
	"/admin/api/",
	"/admin/events/",
	"/webhook",
	"/llms.txt",
	"/llms-full.txt",
}

// isBackendPath reports whether a request path belongs to the Go backend.
func isBackendPath(p string) bool {
	for _, prefix := range backendPrefixes {
		if strings.HasSuffix(prefix, "/") {
			if strings.HasPrefix(p, prefix) || p == strings.TrimSuffix(prefix, "/") {
				return true
			}
			continue
		}
		if p == prefix {
			return true
		}
	}
	return false
}

// newProxy returns a handler that routes between backend and frontend. Both are
// httputil.ReverseProxy instances, which forward `Upgrade: websocket` handshakes (the
// admin event socket and Next's HMR socket) as raw bidirectional streams.
func newProxy(backend, frontend *url.URL) http.Handler {
	be := httputil.NewSingleHostReverseProxy(backend)
	fe := httputil.NewSingleHostReverseProxy(frontend)
	for name, p := range map[string]*httputil.ReverseProxy{"backend": be, "frontend": fe} {
		upstream := name
		p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("devproxy: %s unreachable for %s %s: %v", upstream, r.Method, r.URL.Path, err)
			http.Error(w, fmt.Sprintf("devproxy: %s unreachable: %v", upstream, err), http.StatusBadGateway)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isBackendPath(r.URL.Path):
			be.ServeHTTP(w, r)
		case r.URL.Path == "/":
			// Convenience: the dev origin's root opens the admin UI.
			http.Redirect(w, r, "/admin/", http.StatusTemporaryRedirect)
		default:
			fe.ServeHTTP(w, r)
		}
	})
}

// loopbackOnly reports whether addr binds only to a loopback interface; the proxy has
// no authentication of its own and must not be exposed.
func loopbackOnly(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parseTarget(name, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("-%s %q is not an absolute URL", name, raw)
	}
	return u, nil
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "listen address (loopback only)")
	backendURL := flag.String("backend", "http://127.0.0.1:9000", "Go backend base URL (LISTEN_ADDR of cmd/server)")
	frontendURL := flag.String("frontend", "http://127.0.0.1:3000", "`next dev` base URL")
	flag.Parse()

	if !loopbackOnly(*listen) {
		fmt.Fprintf(os.Stderr, "devproxy: refusing to listen on %q: use a loopback address (dev only)\n", *listen)
		os.Exit(2)
	}
	be, err := parseTarget("backend", *backendURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devproxy:", err)
		os.Exit(2)
	}
	fe, err := parseTarget("frontend", *frontendURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devproxy:", err)
		os.Exit(2)
	}
	log.Printf("devproxy: %s -> API/events/webhook/llms %s, rest %s", *listen, be, fe)
	log.Fatal(http.ListenAndServe(*listen, newProxy(be, fe)))
}
