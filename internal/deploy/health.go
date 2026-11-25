package deploy

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Health-check parameters (Q-13, FR-042): the unit must be "active" at three
// consecutive samples two seconds apart, its NRestarts must not rise, and the whole
// check is bounded to 30 s. The sleeps go through deployHealthCheckSleep, and the
// elapsed time is the sum of the sleeps, so tests run on a virtual clock.
const (
	healthRequiredActive = 3
	healthInterval       = 2 * time.Second
	healthMaxWait        = 30 * time.Second
	healthHTTPTimeout    = 5 * time.Second
	maxHealthURLLen      = 2048
)

// healthHTTPGet returns the status code of a GET on url; a seam for tests.
var healthHTTPGet = func(target string) (int, error) {
	client := &http.Client{
		Timeout: healthHTTPTimeout,
		// A redirect is an answer, not a success: do not follow it anywhere.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(target)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// readNRestarts returns the unit's NRestarts counter. ok is false when systemd does
// not report it (older systemd, stub, error); the restart check is then skipped.
func readNRestarts(serviceName string) (n int, ok bool) {
	out, err := systemctlUnit("show", serviceName, "-p", "NRestarts", "--value")
	if err != nil {
		return 0, false
	}
	n, err = strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// runHealthCheck waits for the freshly restarted service to prove itself. checkpoint
// is called before every sample (the cancel hook of the caller); a non-nil abort is
// returned as is and no verdict is made. Otherwise unhealthy is nil for a healthy
// service and the reason for an unhealthy one (the caller rolls back).
func runHealthCheck(serviceName, healthURL string, baseline int, haveBaseline bool, checkpoint func() error) (unhealthy, abort error) {
	var elapsed time.Duration
	consecutive := 0
	lastStatus := ""
	for consecutive < healthRequiredActive {
		if err := checkpoint(); err != nil {
			return nil, err
		}
		if elapsed >= healthMaxWait {
			return fmt.Errorf("service was not stably active within %s (last state %q)", healthMaxWait, lastStatus), nil
		}
		deployHealthCheckSleep(healthInterval)
		elapsed += healthInterval

		out, err := systemctlUnit("is-active", serviceName)
		lastStatus = strings.TrimSpace(string(out))
		slog.Info("health check", "elapsed", elapsed, "status", lastStatus, "consecutive", consecutive)
		if err == nil && lastStatus == "active" {
			consecutive++
		} else {
			consecutive = 0
		}
		if haveBaseline {
			if n, ok := readNRestarts(serviceName); ok && n > baseline {
				return fmt.Errorf("service restarted itself %d time(s) during the health check (restart loop)", n-baseline), nil
			}
		}
	}

	if healthURL == "" {
		return nil, nil
	}
	lastProblem := ""
	for {
		code, err := healthHTTPGet(healthURL)
		switch {
		case err != nil:
			lastProblem = "request failed"
			slog.Info("health url check", "error", err)
		case code >= 200 && code < 300:
			return nil, nil
		default:
			lastProblem = "status " + strconv.Itoa(code)
		}
		if err := checkpoint(); err != nil {
			return nil, err
		}
		if elapsed >= healthMaxWait {
			return fmt.Errorf("health URL did not return 2xx within %s (%s)", healthMaxWait, lastProblem), nil
		}
		deployHealthCheckSleep(healthInterval)
		elapsed += healthInterval
	}
}

// ValidateHealthURL accepts an empty value (no HTTP health check) or an absolute
// http/https URL without credentials (FR-042).
func ValidateHealthURL(raw string) error {
	if raw == "" {
		return nil
	}
	if len(raw) > maxHealthURLLen {
		return fmt.Errorf("must be at most %d characters", maxHealthURLLen)
	}
	for _, r := range raw {
		if r < 0x21 || r == 0x7f {
			return fmt.Errorf("must not contain spaces or control characters")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("must be a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("must start with http:// or https://")
	}
	if u.Hostname() == "" {
		return fmt.Errorf("must include a host")
	}
	if u.User != nil {
		return fmt.Errorf("must not contain credentials")
	}
	return nil
}
