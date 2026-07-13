package download

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/client"
)

func noSleep(_ time.Duration) {}

func TestNewDownloadClientHasBoundedTimeouts(t *testing.T) {
	t.Parallel()
	c := NewDownloadClient("1.1.1.1")
	if c == nil {
		t.Fatal("client must not be nil")
	}
	opts := c.GetOptions()
	if opts == nil {
		t.Fatal("client options must not be nil")
	}
	if opts.DialTimeout != 10*time.Second {
		t.Errorf("expected 10s dial timeout, got %v", opts.DialTimeout)
	}
	if opts.ReadTimeout != 10*time.Minute {
		t.Errorf("expected 10m read timeout, got %v", opts.ReadTimeout)
	}
	if opts.MaxConnsPerHost != 10 {
		t.Errorf("expected 10 max conns per host, got %d", opts.MaxConnsPerHost)
	}
	if !opts.ResponseBodyStream {
		t.Error("expected ResponseBodyStream to be enabled")
	}
}

func TestNormalizeDNSServerAddsDefaultPort(t *testing.T) {
	t.Parallel()
	if got := normalizeDNSServer("1.1.1.1"); got != "1.1.1.1:53" {
		t.Fatalf("expected default port to be added, got %q", got)
	}
}

func TestNormalizeDNSServerKeepsExplicitPort(t *testing.T) {
	t.Parallel()
	if got := normalizeDNSServer("1.1.1.1:5353"); got != "1.1.1.1:5353" {
		t.Fatalf("expected explicit port to be preserved, got %q", got)
	}
}

func TestNewDownloadClientUsesCustomResolver(t *testing.T) {
	t.Parallel()
	dialer := newDownloadDialer("1.1.1.1")
	if dialer.Resolver == nil {
		t.Fatal("expected custom resolver to be configured")
	}
	if dialer.Timeout != 30*time.Second {
		t.Fatalf("expected dialer timeout to remain 30s, got %v", dialer.Timeout)
	}
	resolver := dialer.Resolver
	if resolver == nil || !resolver.PreferGo {
		t.Fatal("expected resolver to prefer Go DNS")
	}
	if _, ok := any(dialer.Resolver).(*net.Resolver); !ok {
		t.Fatal("expected resolver to be *net.Resolver")
	}
	if resolver.Dial == nil {
		t.Fatal("expected resolver Dial override to be configured")
	}
	if normalized := normalizeDNSServer("1.1.1.1"); normalized != "1.1.1.1:53" {
		t.Fatalf("expected normalized DNS server, got %q", normalized)
	}
}

func TestNewDownloadClientLeavesResolverUnsetWithoutDNS(t *testing.T) {
	t.Parallel()
	dialer := newDownloadDialer("")
	if dialer.Resolver != nil {
		t.Fatal("expected resolver to be unset when DNS is blank")
	}
}

func TestDownloadWithRetry200ReturnsImmediately(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "binary-data")
	}))
	defer srv.Close()

	c, _ := client.NewClient(client.WithResponseBodyStream(true))
	resp, err := DownloadWithRetry(c, srv.URL, noSleep)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestDownloadWithRetry500RetriesAndSucceeds(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := client.NewClient(client.WithResponseBodyStream(true))
	resp, err := DownloadWithRetry(c, srv.URL, noSleep)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if calls.Load() != 3 {
		t.Errorf("expected 3 calls, got %d", calls.Load())
	}
}

func TestDownloadWithRetry429RetriesAndSucceeds(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := client.NewClient(client.WithResponseBodyStream(true))
	resp, err := DownloadWithRetry(c, srv.URL, noSleep)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestDownloadWithRetry404FailsImmediately(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c, _ := client.NewClient(client.WithResponseBodyStream(true))
	_, err := DownloadWithRetry(c, srv.URL, noSleep)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if calls.Load() != 1 {
		t.Errorf("expected exactly 1 call for 404, got %d", calls.Load())
	}
}

func TestDownloadWithRetry403FailsImmediately(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c, _ := client.NewClient(client.WithResponseBodyStream(true))
	_, err := DownloadWithRetry(c, srv.URL, noSleep)
	if err == nil {
		t.Fatal("expected error for 403, got nil")
	}
	if calls.Load() != 1 {
		t.Errorf("expected exactly 1 call for 403, got %d", calls.Load())
	}
}

func TestDownloadWithRetryAllAttemptsExhaustedReturnsError(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, _ := client.NewClient(client.WithResponseBodyStream(true))
	_, err := DownloadWithRetry(c, srv.URL, noSleep)
	if err == nil {
		t.Fatal("expected error after exhausted retries, got nil")
	}
	if calls.Load() != 3 {
		t.Errorf("expected 3 calls, got %d", calls.Load())
	}
}

func TestDownloadWithRetryNetworkErrorRetries(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	addr := srv.URL
	srv.Close()

	var sleepCalls int
	countSleep := func(_ time.Duration) { sleepCalls++ }

	c, _ := client.NewClient(client.WithResponseBodyStream(true))
	_, err := DownloadWithRetry(c, addr, countSleep)
	if err == nil {
		t.Fatal("expected error for closed server, got nil")
	}
	if sleepCalls == 0 {
		t.Error("expected backoff sleeps for network errors, got 0")
	}
}

func TestDownloadWithRetryBackoffDurationsAreCorrect(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	expected := []time.Duration{1 * time.Second, 2 * time.Second}
	var got []time.Duration
	recordSleep := func(d time.Duration) { got = append(got, d) }

	c, _ := client.NewClient(client.WithResponseBodyStream(true))
	resp, err := DownloadWithRetry(c, srv.URL, recordSleep)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if len(got) != len(expected) {
		t.Fatalf("expected %d sleeps, got %d: %v", len(expected), len(got), got)
	}
	for i, d := range expected {
		if got[i] != d {
			t.Errorf("sleep[%d]: expected %v, got %v", i, d, got[i])
		}
	}
}

func TestDownloadBinaryTooLargeContentLengthIsRejected(t *testing.T) {
	t.Parallel()
	maxBytes := int64(32)
	body := bytes.Repeat([]byte{'a'}, int(maxBytes))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(maxBytes+1))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	tmpPath := filepath.Join(t.TempDir(), "artifact.tmp")
	dlClient, _ := client.NewClient(client.WithResponseBodyStream(true))
	_, err := downloadBinaryWithMaxBytes(srv.URL, tmpPath, NewProgressTracker(), "app1", dlClient, maxBytes)
	if err == nil {
		t.Fatal("expected oversized Content-Length to be rejected")
	}
	if !strings.Contains(err.Error(), "too large") && !strings.Contains(err.Error(), "unexpected EOF") && !strings.Contains(err.Error(), "download failed") {
		t.Fatalf("expected error to mention too large or unexpected EOF, got %q", err.Error())
	}
	assertNoSuccessfulDownloadArtifact(t, tmpPath)
}

func TestDownloadBinaryExceedsMaxBytesWhileStreamingIsRejected(t *testing.T) {
	t.Parallel()
	maxBytes := int64(32)
	body := bytes.Repeat([]byte{'b'}, int(maxBytes+1))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	tmpPath := filepath.Join(t.TempDir(), "artifact.tmp")
	dlClient, _ := client.NewClient(client.WithResponseBodyStream(true))
	_, err := downloadBinaryWithMaxBytes(srv.URL, tmpPath, NewProgressTracker(), "app1", dlClient, maxBytes)
	if err == nil {
		t.Fatal("expected streamed body above maxBytes to be rejected")
	}
	if !strings.Contains(err.Error(), "too large") && !strings.Contains(err.Error(), "unexpected EOF") && !strings.Contains(err.Error(), "download failed") {
		t.Fatalf("expected error to mention too large or unexpected EOF, got %q", err.Error())
	}
	assertNoSuccessfulDownloadArtifact(t, tmpPath)
}

func TestDownloadBinaryIncompleteContentLengthIsRejected(t *testing.T) {
	t.Parallel()
	maxBytes := int64(32)
	body := bytes.Repeat([]byte{'c'}, int(maxBytes/2))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(maxBytes))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	tmpPath := filepath.Join(t.TempDir(), "artifact.tmp")
	dlClient, _ := client.NewClient(client.WithResponseBodyStream(true))
	_, err := downloadBinaryWithMaxBytes(srv.URL, tmpPath, NewProgressTracker(), "app1", dlClient, maxBytes)
	if err == nil {
		t.Fatal("expected incomplete response body to be rejected")
	}
	if !strings.Contains(err.Error(), "incomplete") && !strings.Contains(err.Error(), "unexpected EOF") && !strings.Contains(err.Error(), "download failed") {
		t.Fatalf("expected error to mention incomplete or unexpected EOF, got %q", err.Error())
	}
	assertNoSuccessfulDownloadArtifact(t, tmpPath)
}

func assertNoSuccessfulDownloadArtifact(t *testing.T, tmpPath string) {
	t.Helper()
	info, err := os.Stat(tmpPath)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("stat tmp artifact: %v", err)
	}
	if info.Mode()&0111 != 0 {
		t.Fatalf("rejected tmp artifact must not be executable, mode=%v", info.Mode())
	}
	if info.Size() > 0 {
		t.Fatalf("rejected tmp artifact should be removed or empty, size=%d", info.Size())
	}
}

func TestIsRetriableStatusReturnsTrueFor5xx(t *testing.T) {
	t.Parallel()
	for _, code := range []int{500, 502, 503, 504} {
		if !isRetriableStatus(code) {
			t.Errorf("expected %d to be retriable", code)
		}
	}
}

func TestIsRetriableStatusReturnsTrueFor429(t *testing.T) {
	t.Parallel()
	if !isRetriableStatus(429) {
		t.Error("expected 429 to be retriable")
	}
}

func TestIsRetriableStatusReturnsFalseFor4xx(t *testing.T) {
	t.Parallel()
	for _, code := range []int{400, 401, 403, 404} {
		if isRetriableStatus(code) {
			t.Errorf("expected %d to NOT be retriable", code)
		}
	}
}
