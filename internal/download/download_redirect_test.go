package download

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// T040 (B1/B2/FR-030/FR-031/SC-003): redirect handling of the download client.

func newRedirectServer(t *testing.T, relative bool, newConns *atomic.Int64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/r", func(w http.ResponseWriter, r *http.Request) {
		if relative {
			http.Redirect(w, r, "/f", http.StatusFound)
			return
		}
		w.Header().Set("Location", base+"/f") // absolute, like GitHub's signed-URL redirect
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/f", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew && newConns != nil {
			newConns.Add(1)
		}
	}
	srv.Start()
	base = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

// 50 sequential downloads through an absolute-Location redirect must reuse
// connections (<= 10 new TCP connections, pool size) and all succeed.
func TestDownload_50RedirectsNoPoolExhaustion(t *testing.T) {
	var newConns atomic.Int64
	srv := newRedirectServer(t, false, &newConns)
	c := NewDownloadClient("")
	failures := 0
	for i := range 50 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		resp, err := DownloadWithRetryContext(ctx, c, srv.URL+"/r", nil, noSleep)
		if err != nil {
			cancel()
			failures++
			t.Logf("iter %d: %v", i, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if string(body) != "ok" {
			t.Fatalf("iter %d: body = %q, want ok", i, body)
		}
	}
	if failures > 0 {
		t.Errorf("%d of 50 redirected downloads failed", failures)
	}
	if n := newConns.Load(); n > 10 {
		t.Errorf("opened %d TCP connections for 50 redirected downloads, want <= 10", n)
	}
}

// A relative Location header ("/f") must be resolved against the request URL.
func TestDownload_RelativeLocationResolved(t *testing.T) {
	srv := newRedirectServer(t, true, nil)
	c := NewDownloadClient("")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := DownloadWithRetryContext(ctx, c, srv.URL+"/r", nil, noSleep)
	if err != nil {
		t.Fatalf("relative redirect not followed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
}
