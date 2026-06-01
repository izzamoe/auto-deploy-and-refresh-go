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

		req := &protocol.Request{}
		req.SetRequestURI(url)
		req.SetMethod(http.MethodGet)
		resp := &protocol.Response{}

		err := client.Do(ctx, req, resp)
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

		httpResp := &http.Response{
			StatusCode:    resp.StatusCode(),
			ContentLength: parseContentLength(resp.Header.Peek("Content-Length")),
		}

		body := resp.BodyStream()
		if rc, ok := body.(io.ReadCloser); ok {
			httpResp.Body = rc
		} else {
			httpResp.Body = io.NopCloser(body)
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
