package main

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
)

func noSleep(_ time.Duration) {}

func TestNewDownloadClientHasBoundedTimeouts(t *testing.T) {
	client := NewDownloadClient("1.1.1.1")
	if client.Timeout == 0 {
		t.Fatal("client.Timeout must be non-zero")
	}
	if client.Timeout != 10*time.Minute {
		t.Errorf("expected 10m timeout, got %v", client.Timeout)
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport must be *http.Transport")
	}
	if tr.TLSHandshakeTimeout == 0 {
		t.Error("TLSHandshakeTimeout must be non-zero")
	}
	if tr.ResponseHeaderTimeout == 0 {
		t.Error("ResponseHeaderTimeout must be non-zero")
	}
	if tr.MaxIdleConnsPerHost == 0 {
		t.Error("MaxIdleConnsPerHost must be non-zero")
	}
}

func TestNormalizeDNSServerAddsDefaultPort(t *testing.T) {
	if got := normalizeDNSServer("1.1.1.1"); got != "1.1.1.1:53" {
		t.Fatalf("expected default port to be added, got %q", got)
	}
}

func TestNormalizeDNSServerKeepsExplicitPort(t *testing.T) {
	if got := normalizeDNSServer("1.1.1.1:5353"); got != "1.1.1.1:5353" {
		t.Fatalf("expected explicit port to be preserved, got %q", got)
	}
}

func TestNewDownloadClientUsesCustomResolver(t *testing.T) {
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
	dialer := newDownloadDialer("")
	if dialer.Resolver != nil {
		t.Fatal("expected resolver to be unset when DNS is blank")
	}
}

func TestDownloadWithRetry200ReturnsImmediately(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "binary-data")
	}))
	defer srv.Close()

	client := &http.Client{}
	resp, err := DownloadWithRetry(client, srv.URL, noSleep)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestDownloadWithRetry500RetriesAndSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{}
	resp, err := DownloadWithRetry(client, srv.URL, noSleep)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestDownloadWithRetry429RetriesAndSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{}
	resp, err := DownloadWithRetry(client, srv.URL, noSleep)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestDownloadWithRetry404FailsImmediately(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{}
	_, err := DownloadWithRetry(client, srv.URL, noSleep)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("expected exactly 1 call for 404, got %d", calls)
	}
}

func TestDownloadWithRetry403FailsImmediately(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := &http.Client{}
	_, err := DownloadWithRetry(client, srv.URL, noSleep)
	if err == nil {
		t.Fatal("expected error for 403, got nil")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("expected exactly 1 call for 403, got %d", calls)
	}
}

func TestDownloadWithRetryAllAttemptsExhaustedReturnsError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := &http.Client{}
	_, err := DownloadWithRetry(client, srv.URL, noSleep)
	if err == nil {
		t.Fatal("expected error after exhausted retries, got nil")
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestDownloadWithRetryNetworkErrorRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	addr := srv.URL
	srv.Close()

	var sleepCalls int
	countSleep := func(_ time.Duration) { sleepCalls++ }

	client := &http.Client{}
	_, err := DownloadWithRetry(client, addr, countSleep)
	if err == nil {
		t.Fatal("expected error for closed server, got nil")
	}
	if sleepCalls == 0 {
		t.Error("expected backoff sleeps for network errors, got 0")
	}
}

func TestDownloadWithRetryBackoffDurationsAreCorrect(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
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

	client := &http.Client{}
	resp, err := DownloadWithRetry(client, srv.URL, recordSleep)
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
	maxBytes := int64(32)
	body := bytes.Repeat([]byte{'a'}, int(maxBytes))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(maxBytes+1))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	tmpPath := filepath.Join(t.TempDir(), "artifact.tmp")
	_, err := downloadBinaryWithMaxBytes(srv.URL, tmpPath, NewProgressTracker(), "app1", &http.Client{}, maxBytes)
	if err == nil {
		t.Fatal("expected oversized Content-Length to be rejected")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected error to mention too large, got %q", err.Error())
	}
	assertNoSuccessfulDownloadArtifact(t, tmpPath)
}

func TestDownloadBinaryExceedsMaxBytesWhileStreamingIsRejected(t *testing.T) {
	maxBytes := int64(32)
	body := bytes.Repeat([]byte{'b'}, int(maxBytes+1))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	tmpPath := filepath.Join(t.TempDir(), "artifact.tmp")
	_, err := downloadBinaryWithMaxBytes(srv.URL, tmpPath, NewProgressTracker(), "app1", &http.Client{}, maxBytes)
	if err == nil {
		t.Fatal("expected streamed body above maxBytes to be rejected")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected error to mention too large, got %q", err.Error())
	}
	assertNoSuccessfulDownloadArtifact(t, tmpPath)
}

func TestDownloadBinaryIncompleteContentLengthIsRejected(t *testing.T) {
	maxBytes := int64(32)
	body := bytes.Repeat([]byte{'c'}, int(maxBytes/2))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(maxBytes))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	tmpPath := filepath.Join(t.TempDir(), "artifact.tmp")
	_, err := downloadBinaryWithMaxBytes(srv.URL, tmpPath, NewProgressTracker(), "app1", &http.Client{}, maxBytes)
	if err == nil {
		t.Fatal("expected incomplete response body to be rejected")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("expected error to mention incomplete, got %q", err.Error())
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
	for _, code := range []int{500, 502, 503, 504} {
		if !isRetriableStatus(code) {
			t.Errorf("expected %d to be retriable", code)
		}
	}
}

func TestIsRetriableStatusReturnsTrueFor429(t *testing.T) {
	if !isRetriableStatus(429) {
		t.Error("expected 429 to be retriable")
	}
}

func TestIsRetriableStatusReturnsFalseFor4xx(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404} {
		if isRetriableStatus(code) {
			t.Errorf("expected %d to NOT be retriable", code)
		}
	}
}
