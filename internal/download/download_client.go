package download

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app/client"
	herrs "github.com/cloudwego/hertz/pkg/common/errors"
	"github.com/cloudwego/hertz/pkg/network"
	"github.com/cloudwego/hertz/pkg/network/standard"
	"github.com/cloudwego/hertz/pkg/protocol"
)

type dnsDialer struct {
	base      network.Dialer
	netDialer *net.Dialer
}

func (d *dnsDialer) DialConnection(network, address string, timeout time.Duration, tlsConfig *tls.Config) (network.Conn, error) {
	resolvedAddr, err := d.resolve(address)
	if err != nil {
		return nil, err
	}
	return d.base.DialConnection(network, resolvedAddr, timeout, tlsConfig)
}

func (d *dnsDialer) DialTimeout(network, address string, timeout time.Duration, tlsConfig *tls.Config) (net.Conn, error) {
	resolvedAddr, err := d.resolve(address)
	if err != nil {
		return nil, err
	}
	return d.base.DialTimeout(network, resolvedAddr, timeout, tlsConfig)
}

func (d *dnsDialer) AddTLS(conn network.Conn, tlsConfig *tls.Config) (network.Conn, error) {
	return d.base.AddTLS(conn, tlsConfig)
}

func (d *dnsDialer) resolve(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address, nil
	}
	if d.netDialer.Resolver == nil {
		return address, nil
	}
	ctx := context.Background()
	if d.netDialer.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.netDialer.Timeout)
		defer cancel()
	}
	ips, err := d.netDialer.Resolver.LookupHost(ctx, host)
	if err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("no IPs found for %s", host)
	}
	return net.JoinHostPort(ips[0], port), nil
}

func NewDownloadClient(dnsServer string) *client.Client {
	netDialer := newDownloadDialer(dnsServer)
	c, err := client.NewClient(
		client.WithDialer(&dnsDialer{
			base:      standard.NewDialer(),
			netDialer: netDialer,
		}),
		client.WithDialTimeout(10*time.Second),
		client.WithClientReadTimeout(10*time.Minute),
		client.WithMaxConnsPerHost(10),
		client.WithMaxIdleConnDuration(10*time.Second),
		client.WithResponseBodyStream(true),
	)
	if err != nil {
		c, _ = client.NewClient()
	}
	return c
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

var defaultSleep = time.Sleep

func DownloadWithRetry(client *client.Client, url string, sleepFn func(time.Duration)) (*http.Response, error) {
	return DownloadWithRetryContext(context.Background(), client, url, sleepFn)
}

const maxRedirects = 10

func DownloadWithRetryContext(ctx context.Context, client *client.Client, url string, sleepFn func(time.Duration)) (*http.Response, error) {
	if sleepFn == nil {
		sleepFn = defaultSleep
	}

	backoffs := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}
	maxAttempts := 3

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		httpResp, err := doWithRedirects(ctx, client, url)
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
			httpResp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", httpResp.StatusCode)
			if attempt < maxAttempts-1 {
				sleepFn(backoffs[attempt])
			}
			continue
		}

		if httpResp.StatusCode == http.StatusOK {
			return httpResp, nil
		}

		httpResp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", httpResp.StatusCode)
	}

	return nil, fmt.Errorf("download failed after %d attempts: %w", maxAttempts, lastErr)
}

// doWithRedirects performs a single GET following up to maxRedirects redirects.
// Authorization header is stripped on cross-host redirects (security).
func doWithRedirects(ctx context.Context, c *client.Client, url string) (*http.Response, error) {
	currentURL := url
	var originalHost string

	for i := 0; i <= maxRedirects; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		req := &protocol.Request{}
		req.SetRequestURI(currentURL)
		req.SetMethod(http.MethodGet)

		if i > 0 && originalHost != "" {
			parsedHost := extractHost(currentURL)
			if parsedHost != originalHost {
				req.Header.Del("Authorization")
			}
		}
		if i == 0 {
			originalHost = extractHost(currentURL)
		}

		resp := &protocol.Response{}
		if err := c.Do(ctx, req, resp); err != nil {
			return nil, err
		}

		statusCode := resp.StatusCode()

		if !isRedirectStatus(statusCode) {
			httpResp := &http.Response{
				StatusCode:    statusCode,
				ContentLength: parseContentLength(resp.Header.Peek("Content-Length")),
			}
			body := resp.BodyStream()
			if rc, ok := body.(io.ReadCloser); ok {
				httpResp.Body = rc
			} else {
				httpResp.Body = io.NopCloser(body)
			}
			return httpResp, nil
		}

		location := string(resp.Header.Peek("Location"))
		if location == "" {
			return nil, fmt.Errorf("redirect with empty Location header (status %d)", statusCode)
		}
		currentURL = location
	}

	return nil, fmt.Errorf("too many redirects (max %d)", maxRedirects)
}

func isRedirectStatus(code int) bool {
	switch code {
	case http.StatusMovedPermanently,  // 301
		http.StatusFound,              // 302
		http.StatusSeeOther,           // 303
		http.StatusTemporaryRedirect,  // 307
		http.StatusPermanentRedirect:  // 308
		return true
	}
	return false
}

func extractHost(rawURL string) string {
	// Fast path: find "://" then extract up to next "/"
	idx := strings.Index(rawURL, "://")
	if idx < 0 {
		return rawURL
	}
	rest := rawURL[idx+3:]
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		return rest[:slash]
	}
	return rest
}

func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	if errors.Is(err, herrs.ErrTimeout) {
		return true
	}
	if errors.Is(err, herrs.ErrConnectionClosed) {
		return true
	}
	return false
}

func isRetriableStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || (statusCode >= 500 && statusCode < 600)
}

func parseContentLength(b []byte) int64 {
	if len(b) == 0 {
		return -1
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return -1
	}
	return n
}
