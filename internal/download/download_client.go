package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// maxRedirects is the number of redirect hops a single download may follow.
	maxRedirects = 10
	// readIdleTimeout bounds how long a download may make no progress at all
	// (waiting for response headers or for the next chunk of the body).
	readIdleTimeout = 10 * time.Minute
	// responseHeaderTimeout bounds the wait for response headers after the
	// request was written.
	responseHeaderTimeout = 60 * time.Second
	// maxConnsPerHost is the size of the connection pool per host.
	maxConnsPerHost = 10
)

var errReadIdleTimeout = errors.New("download stalled: no data received before the idle timeout")

// NewDownloadClient returns the shared *http.Client used for artifact downloads
// (B1/B2/FR-030, research D10): a standard net/http client whose Transport pools
// at most maxConnsPerHost connections per host, resolves names through dnsServer
// when it is set and never decompresses (so Content-Length matches the artifact
// bytes). The redirect policy (checkRedirect) is enforced by
// DownloadWithRetryContext on every call, whichever client it receives.
func NewDownloadClient(dnsServer string) *http.Client {
	resolver := newDownloadDialer(dnsServer).Resolver
	dialer := &net.Dialer{Timeout: 10 * time.Second, Resolver: resolver}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
		MaxConnsPerHost:       maxConnsPerHost,
		MaxIdleConns:          maxConnsPerHost,
		MaxIdleConnsPerHost:   maxConnsPerHost,
		IdleConnTimeout:       10 * time.Second,
		DisableCompression:    true,
	}
	return &http.Client{Transport: transport, CheckRedirect: checkRedirect}
}

func newDownloadDialer(dnsServer string) *net.Dialer {
	dialer := &net.Dialer{Timeout: 30 * time.Second}

	if normalizedDNS := normalizeDNSServer(dnsServer); normalizedDNS != "" {
		dialer.Resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				resolverDialer := &net.Dialer{Timeout: 30 * time.Second}
				return resolverDialer.DialContext(ctx, network, normalizedDNS)
			},
		}
	}

	return dialer
}

func normalizeDNSServer(dnsServer string) string {
	server := strings.TrimSpace(dnsServer)
	if server == "" {
		return ""
	}

	if _, _, err := net.SplitHostPort(server); err == nil {
		return server
	}

	return net.JoinHostPort(server, "53")
}

// checkRedirect is the redirect policy of every download (FR-031):
//   - at most maxRedirects hops;
//   - never follow an https -> http downgrade;
//   - Authorization (and other credentials) are sent only to the host AND scheme
//     the request started on, so a token for the GitHub API never reaches the
//     signed-storage host GitHub redirects asset downloads to.
//
// Relative Location headers are resolved by net/http before this runs.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("too many redirects (max %d)", maxRedirects)
	}
	prev := via[len(via)-1]
	if prev.URL.Scheme == "https" && req.URL.Scheme == "http" {
		return fmt.Errorf("refusing redirect from https to http (downgrade): %s", redactedTarget(req))
	}
	origin := via[0]
	if req.URL.Host != origin.URL.Host || req.URL.Scheme != origin.URL.Scheme {
		req.Header.Del("Authorization")
		req.Header.Del("Proxy-Authorization")
		req.Header.Del("Cookie")
	}
	return nil
}

// redactedTarget names a redirect target without its query string (signed
// storage URLs carry credentials there).
func redactedTarget(req *http.Request) string {
	return req.URL.Scheme + "://" + req.URL.Host + req.URL.Path
}

var defaultSleep = time.Sleep

func DownloadWithRetry(c *http.Client, url string, sleepFn func(time.Duration)) (*http.Response, error) {
	return DownloadWithRetryContext(context.Background(), c, url, nil, sleepFn)
}

// DownloadWithRetryContext GETs url with retries. headers, if non-nil, are set
// on the request; Authorization is dropped on any redirect that changes host or
// scheme (checkRedirect). The returned response is the final one (status 200)
// and its Body must be closed by the caller; reading it is bounded by
// readIdleTimeout.
func DownloadWithRetryContext(ctx context.Context, c *http.Client, url string, headers map[string]string, sleepFn func(time.Duration)) (*http.Response, error) {
	if sleepFn == nil {
		sleepFn = defaultSleep
	}
	if c == nil {
		c = NewDownloadClient("")
	}
	hc := *c // shallow copy: the Transport (and its pool) is shared, the policy is ours
	hc.CheckRedirect = checkRedirect

	backoffs := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}
	maxAttempts := 3

	var lastErr error
	for attempt := range maxAttempts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		httpResp, err := getOnce(ctx, &hc, url, headers)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if isTransientError(err) {
				lastErr = err
				if attempt < maxAttempts-1 {
					sleepFn(backoffs[attempt])
				}
				continue
			}
			return nil, err
		}

		if isRetriableStatus(httpResp.StatusCode) {
			drainAndClose(httpResp.Body)
			lastErr = fmt.Errorf("HTTP %d", httpResp.StatusCode)
			if attempt < maxAttempts-1 {
				sleepFn(backoffs[attempt])
			}
			continue
		}

		if httpResp.StatusCode == http.StatusOK {
			return httpResp, nil
		}

		drainAndClose(httpResp.Body)
		return nil, fmt.Errorf("HTTP %d", httpResp.StatusCode)
	}

	return nil, fmt.Errorf("download failed after %d attempts: %w", maxAttempts, lastErr)
}

// getOnce performs one GET (following redirects) whose context is cancelled if
// the transfer makes no progress for readIdleTimeout. Intermediate redirect
// responses are closed by net/http, so their connections return to the pool.
func getOnce(ctx context.Context, c *http.Client, url string, headers map[string]string) (*http.Response, error) {
	reqCtx, cancel := context.WithCancel(ctx)
	var idled atomic.Bool
	timer := time.AfterFunc(readIdleTimeout, func() {
		idled.Store(true)
		cancel()
	})

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		timer.Stop()
		cancel()
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.Do(req)
	if err != nil {
		timer.Stop()
		cancel()
		if idled.Load() {
			return nil, errReadIdleTimeout
		}
		return nil, err
	}
	resp.Body = &idleBody{ReadCloser: resp.Body, timer: timer, cancel: cancel, idled: &idled}
	return resp, nil
}

// idleBody resets the idle timer on every Read and releases the per-request
// context on Close.
type idleBody struct {
	io.ReadCloser
	timer  *time.Timer
	cancel context.CancelFunc
	idled  *atomic.Bool
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		if b.idled.Load() {
			return n, errReadIdleTimeout
		}
		return n, err
	}
	b.timer.Reset(readIdleTimeout)
	return n, nil
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// drainAndClose reads a little of an unwanted body so the connection can be
// reused, then closes it.
func drainAndClose(body io.ReadCloser) {
	_, _ = io.CopyN(io.Discard, body, 4096)
	body.Close()
}

func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errReadIdleTimeout) {
		return true
	}
	if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
		return true
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return false
}

func isRetriableStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || (statusCode >= 500 && statusCode < 600)
}
