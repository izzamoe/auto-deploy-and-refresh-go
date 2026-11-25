package auth

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestRateLimit_WindowBlocksAndFrees(t *testing.T) {
	clk := newTestClock()
	w := NewWindow(3, time.Minute, clk.Now)
	for i := 0; i < 3; i++ {
		if blocked, _ := w.Blocked("k"); blocked {
			t.Fatalf("blocked after only %d hits", i)
		}
		w.Add("k")
		clk.Advance(5 * time.Second)
	}
	blocked, retry := w.Blocked("k")
	if !blocked {
		t.Fatal("expected blocked after 3 hits")
	}
	// Oldest hit was 15s ago, so the window frees up in 45s.
	if retry < 44*time.Second || retry > 46*time.Second {
		t.Errorf("retry = %v, want about 45s", retry)
	}
	if blocked, _ := w.Blocked("other"); blocked {
		t.Error("keys must be independent")
	}
	clk.Advance(46 * time.Second)
	if blocked, _ := w.Blocked("k"); blocked {
		t.Error("window must free up once the oldest hit expires")
	}
}

func TestRateLimit_WindowBoundedUnderFlood(t *testing.T) {
	clk := newTestClock()
	w := NewWindow(5, time.Minute, clk.Now)
	for i := 0; i < 10000; i++ {
		w.Add("flood")
	}
	w.mu.Lock()
	n := len(w.hits["flood"])
	w.mu.Unlock()
	if n != 5 {
		t.Errorf("stored %d timestamps for one key, want at most max=5", n)
	}
	// Hammering a blocked key must not extend the block (no lock-out amplification).
	clk.Advance(61 * time.Second)
	if blocked, _ := w.Blocked("flood"); blocked {
		t.Error("block must end one window after the first hits")
	}
}

func TestRateLimit_WindowSweepsIdleKeys(t *testing.T) {
	clk := newTestClock()
	w := NewWindow(5, time.Minute, clk.Now)
	for i := 0; i < 300; i++ {
		w.Add(strings.Repeat("x", 1) + string(rune('a'+i%26)) + string(rune('A'+i/26)))
	}
	clk.Advance(2 * time.Minute)
	for i := 0; i < sweepEveryNOps; i++ { // enough operations to trigger a sweep
		w.Add("fresh")
	}
	w.mu.Lock()
	n := len(w.hits)
	w.mu.Unlock()
	if n > 2 {
		t.Errorf("idle keys were not swept: %d keys remain", n)
	}
}

func TestRateLimit_LoginFailuresPerIPAndUser(t *testing.T) {
	clk := newTestClock()
	l := NewLoginLimiter(clk.Now)
	ip := "198.51.100.4"
	for i := 0; i < 5; i++ {
		if ok, _ := l.Check(ip, "admin"); !ok {
			t.Fatalf("blocked after %d failures, limit is 5", i)
		}
		l.RecordAttempt(ip)
		l.RecordFailure(ip, "admin")
	}
	ok, retry := l.Check(ip, "admin")
	if ok {
		t.Fatal("6th attempt must be blocked (5 failures / 15 min)")
	}
	if retry < 14*time.Minute || retry > 15*time.Minute {
		t.Errorf("retry = %v, want about 15m", retry)
	}
	if ok, _ := l.Check(ip, "ADMIN "); ok {
		t.Error("username matching is case-insensitive and trimmed")
	}
	if ok, _ := l.Check(ip, "someone-else"); !ok {
		t.Error("another username from the same IP is a separate bucket")
	}
	if ok, _ := l.Check("198.51.100.5", "admin"); !ok {
		t.Error("the same username from another IP is a separate bucket")
	}
	clk.Advance(16 * time.Minute)
	if ok, _ := l.Check(ip, "admin"); !ok {
		t.Error("block must expire after 15 minutes")
	}
}

func TestRateLimit_LoginSuccessClearsFailures(t *testing.T) {
	l := NewLoginLimiter(newTestClock().Now)
	for i := 0; i < 4; i++ {
		l.RecordAttempt("1.1.1.1")
		l.RecordFailure("1.1.1.1", "admin")
	}
	l.RecordSuccess("1.1.1.1", "admin")
	for i := 0; i < 4; i++ {
		l.RecordFailure("1.1.1.1", "admin")
	}
	if ok, _ := l.Check("1.1.1.1", "admin"); !ok {
		t.Error("a success must reset the failure counter")
	}
}

func TestRateLimit_LoginAttemptsPerIP(t *testing.T) {
	clk := newTestClock()
	l := NewLoginLimiter(clk.Now)
	for i := 0; i < 20; i++ {
		if ok, _ := l.Check("203.0.113.1", "user"+string(rune('a'+i))); !ok {
			t.Fatalf("blocked after %d attempts, limit is 20/min", i)
		}
		l.RecordAttempt("203.0.113.1")
	}
	ok, retry := l.Check("203.0.113.1", "brand-new-user")
	if ok {
		t.Fatal("21st attempt within a minute must be blocked")
	}
	if retry < time.Second || retry > time.Minute {
		t.Errorf("retry = %v, want between 1s and 60s", retry)
	}
	clk.Advance(61 * time.Second)
	if ok, _ := l.Check("203.0.113.1", "brand-new-user"); !ok {
		t.Error("per-IP limit must reset after a minute")
	}
}

func mustCIDRs(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	var out []*net.IPNet
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func tcp(ip string) net.Addr { return &net.TCPAddr{IP: net.ParseIP(ip), Port: 40000} }

func hdr(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestClientIP_HeadersIgnoredWhenRemoteNotTrusted(t *testing.T) {
	r := NewClientIPResolver(mustCIDRs(t, "10.0.0.0/8"))
	got := r.Resolve(tcp("203.0.113.50"), hdr(map[string]string{
		"CF-Connecting-IP": "1.2.3.4", "X-Forwarded-For": "5.6.7.8",
	}))
	if got != "203.0.113.50" {
		t.Errorf("untrusted remote can spoof its IP: got %q, want the socket address", got)
	}
}

func TestClientIP_TrustedRemoteUsesCloudflareHeader(t *testing.T) {
	r := NewClientIPResolver(mustCIDRs(t, "10.0.0.0/8"))
	got := r.Resolve(tcp("10.1.2.3"), hdr(map[string]string{
		"CF-Connecting-IP": "198.51.100.77", "X-Forwarded-For": "9.9.9.9",
	}))
	if got != "198.51.100.77" {
		t.Errorf("CF-Connecting-IP from a trusted proxy: got %q", got)
	}
}

func TestClientIP_XForwardedForRightmostUntrusted(t *testing.T) {
	r := NewClientIPResolver(mustCIDRs(t, "10.0.0.0/8", "192.0.2.0/24"))
	// client-controlled prefix, then the real client, then two of our own proxies
	xff := "6.6.6.6, 198.51.100.9, 192.0.2.10, 10.9.9.9"
	if got := r.Resolve(tcp("10.0.0.1"), hdr(map[string]string{"X-Forwarded-For": xff})); got != "198.51.100.9" {
		t.Errorf("rightmost untrusted XFF hop: got %q, want 198.51.100.9", got)
	}
	if got := r.Resolve(tcp("10.0.0.1"), hdr(map[string]string{"X-Forwarded-For": "not-an-ip"})); got != "10.0.0.1" {
		t.Errorf("garbage XFF must fall back to the socket address, got %q", got)
	}
	if got := r.Resolve(tcp("10.0.0.1"), hdr(nil)); got != "10.0.0.1" {
		t.Errorf("no headers: got %q, want the socket address", got)
	}
}

func TestClientIP_EmptyTrustListIgnoresHeadersAndWarnsOnce(t *testing.T) {
	r := NewClientIPResolver(nil)
	var warnings []string
	r.warn = func(msg string, args ...any) { warnings = append(warnings, msg) }

	got := r.Resolve(tcp("203.0.113.8"), hdr(map[string]string{"CF-Connecting-IP": "1.2.3.4"}))
	if got != "203.0.113.8" {
		t.Errorf("default (no trusted proxies) must use the socket address, got %q", got)
	}
	r.Resolve(tcp("203.0.113.8"), hdr(map[string]string{"X-Forwarded-For": "1.2.3.4"}))
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want exactly 1 (RV2-16)", len(warnings))
	}
	if !strings.Contains(warnings[0], "TRUSTED_PROXY_CIDRS") {
		t.Errorf("warning must name the variable: %q", warnings[0])
	}

	quiet := NewClientIPResolver(nil)
	quiet.warn = func(string, ...any) { t.Error("must not warn when no forwarding header is present") }
	quiet.Resolve(tcp("203.0.113.8"), hdr(nil))
}

func TestClientIP_TrustsRemoteAndIPv6(t *testing.T) {
	r := NewClientIPResolver(mustCIDRs(t, "10.0.0.0/8", "fd00::/8"))
	if !r.TrustsRemote(tcp("10.4.4.4")) || !r.TrustsRemote(tcp("fd00::1")) {
		t.Error("addresses inside the trusted CIDRs must be trusted")
	}
	if r.TrustsRemote(tcp("11.0.0.1")) || r.TrustsRemote(nil) {
		t.Error("outside addresses and nil must not be trusted")
	}
	if got := r.Resolve(tcp("fd00::1"), hdr(map[string]string{"CF-Connecting-IP": "2001:db8::5"})); got != "2001:db8::5" {
		t.Errorf("IPv6 client behind a trusted IPv6 proxy: got %q", got)
	}
}
