package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

func NewDownloadClient() *http.Client {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 30 * time.Second,
		}).DialContext,
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

var defaultSleep = time.Sleep

func DownloadWithRetry(client *http.Client, url string, sleepFn func(time.Duration)) (*http.Response, error) {
	if sleepFn == nil {
		sleepFn = defaultSleep
	}

	backoffs := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}
	maxAttempts := 3

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err := client.Get(url)
		if err != nil {
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
