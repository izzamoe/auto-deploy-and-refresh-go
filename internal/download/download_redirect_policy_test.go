package download

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// T040 (B2/FR-031): redirect POLICY of the download client: no https->http
// downgrade, Authorization never leaves the original host/scheme.

func TestDownload_RejectsHTTPSDowngrade(t *testing.T) {
	var plainHits atomic.Int64
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		w.Write([]byte("ok"))
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/f", http.StatusFound)
	}))
	defer secure.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := DownloadWithRetryContext(ctx, secure.Client(), secure.URL+"/r", nil, noSleep)
	if err == nil {
		resp.Body.Close()
		t.Fatal("https->http redirect was followed; want an error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "downgrade") {
		t.Errorf("error should mention the downgrade, got: %v", err)
	}
	if n := plainHits.Load(); n != 0 {
		t.Errorf("plain-http target was contacted %d times; the downgrade must be refused before dialing", n)
	}
}

func TestDownload_StripsAuthorizationOnHostOrSchemeChange(t *testing.T) {
	var gotAuth atomic.Value // header seen by the redirect TARGET
	gotAuth.Store("<unset>")
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/f", http.StatusFound)
	}))
	defer origin.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// origin.Client() trusts both httptest certificates (same test CA).
	resp, err := DownloadWithRetryContext(ctx, origin.Client(), origin.URL+"/r", map[string]string{"Authorization": "Bearer secret"}, noSleep)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	resp.Body.Close()
	if got := gotAuth.Load().(string); got != "" {
		t.Errorf("Authorization leaked to another host: %q", got)
	}
}

func TestCheckRedirectPolicy(t *testing.T) {
	t.Parallel()
	mk := func(rawURL, auth string) *http.Request {
		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		r := &http.Request{Method: http.MethodGet, URL: u, Header: http.Header{}}
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		return r
	}
	cases := []struct {
		name     string
		first    string
		next     string
		wantErr  bool
		wantAuth string // Authorization expected on next after the policy ran
	}{
		{"same host and scheme keeps Authorization", "https://a.example/x", "https://a.example/y", false, "Bearer t"},
		{"other host strips", "https://a.example/x", "https://b.example/y", false, ""},
		{"other port strips", "https://a.example/x", "https://a.example:8443/y", false, ""},
		{"http to https on the same host strips", "http://a.example/x", "https://a.example/y", false, ""},
		{"https to http is refused", "https://a.example/x", "http://a.example/y", true, ""},
		{"https to http on another host is refused", "https://a.example/x", "http://b.example/y", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			first := mk(tc.first, "Bearer t")
			next := mk(tc.next, "Bearer t")
			err := checkRedirect(next, []*http.Request{first})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr {
				if got := next.Header.Get("Authorization"); got != tc.wantAuth {
					t.Errorf("Authorization = %q, want %q", got, tc.wantAuth)
				}
			}
		})
	}

	t.Run("redirect limit", func(t *testing.T) {
		t.Parallel()
		via := make([]*http.Request, maxRedirects)
		for i := range via {
			via[i] = mk("https://a.example/x", "")
		}
		if err := checkRedirect(mk("https://a.example/y", ""), via); err == nil {
			t.Fatalf("expected too-many-redirects error with %d hops", len(via))
		}
		if err := checkRedirect(mk("https://a.example/y", ""), via[:maxRedirects-1]); err != nil {
			t.Fatalf("%d hops must be allowed, got %v", maxRedirects-1, err)
		}
	})
}
