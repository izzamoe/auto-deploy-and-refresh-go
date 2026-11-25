package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// upstream is a fake server that names itself in every response and echoes over a
// hijacked connection after an `Upgrade: websocket` handshake.
func upstream(t *testing.T, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "no hijack", http.StatusInternalServerError)
				return
			}
			conn, buf, err := hj.Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nX-Upstream: %s\r\n\r\n", name)
			_ = buf.Flush()
			_, _ = io.Copy(conn, buf) // echo what the client sends next
			return
		}
		w.Header().Set("X-Upstream", name)
		fmt.Fprintf(w, "%s %s %s", name, r.Method, r.URL.RequestURI())
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestProxy(t *testing.T) (proxy *httptest.Server) {
	t.Helper()
	be := upstream(t, "backend")
	fe := upstream(t, "frontend")
	beURL, _ := url.Parse(be.URL)
	feURL, _ := url.Parse(fe.URL)
	proxy = httptest.NewServer(newProxy(beURL, feURL))
	t.Cleanup(proxy.Close)
	return proxy
}

func get(t *testing.T, rawURL string) (status int, upstreamName, body string) {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(rawURL)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("X-Upstream"), string(b)
}

func TestProxyRoutesAPIToBackendAndTheRestToFrontend(t *testing.T) {
	proxy := newTestProxy(t)
	cases := []struct {
		path string
		want string
	}{
		{"/admin/api/v1/apps?x=1", "backend"},
		{"/admin/api/v1/session", "backend"},
		{"/admin/events/v1/ws", "backend"},
		{"/webhook", "backend"},
		{"/llms.txt", "backend"},
		{"/llms-full.txt", "backend"},
		{"/admin/", "frontend"},
		{"/admin/apps/", "frontend"},
		{"/admin/apps/edit/?id=q1", "frontend"},
		{"/admin/_next/static/chunks/app.js", "frontend"},
		{"/admin/__next._tree.txt?_rsc=abc", "frontend"},
		// Prefix lookalikes must not leak to the backend.
		{"/admin/apis/x", "frontend"},
		{"/webhooks", "frontend"},
		{"/admin/eventsfoo", "frontend"},
	}
	for _, tc := range cases {
		status, got, body := get(t, proxy.URL+tc.path)
		if status != http.StatusOK || got != tc.want {
			t.Errorf("GET %s: status %d upstream %q, want 200 %q (body %q)", tc.path, status, got, tc.want, body)
		}
		wantURI := tc.path
		if !strings.HasSuffix(body, " "+wantURI) {
			t.Errorf("GET %s: upstream saw %q, want the request URI unchanged", tc.path, body)
		}
	}
}

func TestProxyRootOpensTheAdminUI(t *testing.T) {
	proxy := newTestProxy(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(proxy.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect || resp.Header.Get("Location") != "/admin/" {
		t.Fatalf("GET /: status %d Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestProxyForwardsMethodsAndBodies(t *testing.T) {
	proxy := newTestProxy(t)
	resp, err := http.Post(proxy.URL+"/webhook", "application/json", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("X-Upstream") != "backend" || !strings.HasPrefix(string(b), "backend POST /webhook") {
		t.Fatalf("POST /webhook: upstream %q body %q", resp.Header.Get("X-Upstream"), b)
	}
}

// websocketRoundTrip opens a raw WebSocket-style Upgrade through the proxy, checks the
// 101 comes from the expected upstream and that bytes flow both ways afterwards.
func websocketRoundTrip(t *testing.T, proxyURL, path, wantUpstream string) {
	t.Helper()
	u, _ := url.Parse(proxyURL)
	conn, err := net.DialTimeout("tcp", u.Host, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", path, u.Host)
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		t.Fatalf("%s: read handshake: %v", path, err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("%s: status %d, want 101", path, resp.StatusCode)
	}
	if got := resp.Header.Get("X-Upstream"); got != wantUpstream {
		t.Fatalf("%s: upgraded on %q, want %q", path, got, wantUpstream)
	}
	if _, err := conn.Write([]byte("ping-frame\n")); err != nil {
		t.Fatal(err)
	}
	line, err := r.ReadString('\n')
	if err != nil || line != "ping-frame\n" {
		t.Fatalf("%s: echo = %q, %v", path, line, err)
	}
}

func TestProxyForwardsWebSocketUpgrades(t *testing.T) {
	proxy := newTestProxy(t)
	t.Run("admin events go to the backend", func(t *testing.T) {
		websocketRoundTrip(t, proxy.URL, "/admin/events/v1/ws?protocol=2", "backend")
	})
	t.Run("HMR goes to the frontend", func(t *testing.T) {
		websocketRoundTrip(t, proxy.URL, "/admin/_next/webpack-hmr", "frontend")
	})
}

func TestProxyReportsAnUnreachableUpstreamAs502(t *testing.T) {
	dead, _ := url.Parse("http://127.0.0.1:1")
	proxy := httptest.NewServer(newProxy(dead, dead))
	defer proxy.Close()
	for _, p := range []string{"/admin/api/v1/apps", "/admin/"} {
		status, _, body := get(t, proxy.URL+p)
		if status != http.StatusBadGateway || !strings.Contains(body, "unreachable") {
			t.Errorf("GET %s: status %d body %q, want 502 unreachable", p, status, body)
		}
	}
}

func TestLoopbackOnly(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8080": true,
		"[::1]:8080":     true,
		"localhost:8080": true,
		":8080":          false,
		"0.0.0.0:8080":   false,
		"[::]:8080":      false,
		"10.0.0.5:8080":  false,
		"example.com:80": false,
		"8080":           false,
	} {
		if got := loopbackOnly(addr); got != want {
			t.Errorf("loopbackOnly(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestParseTarget(t *testing.T) {
	if _, err := parseTarget("backend", "http://127.0.0.1:9000"); err != nil {
		t.Errorf("valid URL rejected: %v", err)
	}
	for _, bad := range []string{"", "127.0.0.1:9000", "/just/a/path", "http://"} {
		if _, err := parseTarget("backend", bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
