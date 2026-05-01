package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

func NewDownloadClient(dnsServer string) *http.Client {
	dialer := newDownloadDialer(dnsServer)
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConnsPerHost:   10,
		DisableCompression:    false,
	}
	return &http.Client{
		Timeout:   10 * time.Minute,
		Transport: transport,
	}
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

func DownloadWithRetry(client *http.Client, url string, sleepFn func(time.Duration)) (*http.Response, error) {
	return DownloadWithRetryContext(context.Background(), client, url, sleepFn)
}

func DownloadWithRetryContext(ctx context.Context, client *http.Client, url string, sleepFn func(time.Duration)) (*http.Response, error) {
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
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
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

		if isRetriableStatus(resp.StatusCode) {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			if attempt < maxAttempts-1 {
				sleepFn(backoffs[attempt])
			}
			continue
		}

		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
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
	return false
}

func isRetriableStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || (statusCode >= 500 && statusCode < 600)
}
