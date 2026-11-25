package auth

import (
	"strings"
	"sync"
	"time"
)

// Rate limits (Q-4).
const (
	loginFailureMax    = 5
	loginFailureWindow = 15 * time.Minute
	loginAttemptMax    = 20
	loginAttemptWindow = time.Minute

	// TokenFailureMax/TokenFailureWindow bound failed API-token authentication per IP.
	TokenFailureMax    = 10
	TokenFailureWindow = time.Minute

	maxLimiterKey  = 128
	sweepEveryNOps = 256
)

// Window is an in-memory sliding-window hit counter keyed by string (D9). It stores
// at most max timestamps per key, so memory stays bounded under a flood, and it
// sweeps idle keys periodically.
type Window struct {
	mu   sync.Mutex
	max  int
	span time.Duration
	now  func() time.Time
	hits map[string][]time.Time
	ops  int
}

// NewWindow allows max hits per span. A nil clock means time.Now.
func NewWindow(max int, span time.Duration, now func() time.Time) *Window {
	if now == nil {
		now = time.Now
	}
	return &Window{max: max, span: span, now: now, hits: map[string][]time.Time{}}
}

// Blocked reports whether key has reached the limit and, if so, how long until the
// oldest hit leaves the window (at least one second).
func (w *Window) Blocked(key string) (bool, time.Duration) {
	key = clampKey(key)
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	hs := w.prune(key, now)
	if len(hs) < w.max {
		return false, 0
	}
	retry := hs[0].Add(w.span).Sub(now)
	if retry < time.Second {
		retry = time.Second
	}
	return true, retry
}

// Add records one hit.
func (w *Window) Add(key string) {
	key = clampKey(key)
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	if w.ops++; w.ops%sweepEveryNOps == 0 {
		w.sweep(now)
	}
	hs := w.prune(key, now)
	if len(hs) >= w.max {
		return // already blocked; the block ends when the oldest hit expires
	}
	w.hits[key] = append(hs, now)
}

// Reset forgets key.
func (w *Window) Reset(key string) {
	w.mu.Lock()
	delete(w.hits, clampKey(key))
	w.mu.Unlock()
}

// prune drops hits older than the window and returns what is left. Caller holds mu.
func (w *Window) prune(key string, now time.Time) []time.Time {
	hs := w.hits[key]
	cut := now.Add(-w.span)
	i := 0
	for i < len(hs) && !hs[i].After(cut) {
		i++
	}
	if i > 0 {
		hs = hs[i:]
		if len(hs) == 0 {
			delete(w.hits, key)
			return nil
		}
		w.hits[key] = hs
	}
	return hs
}

func (w *Window) sweep(now time.Time) {
	for k := range w.hits {
		w.prune(k, now)
	}
}

func clampKey(k string) string {
	if len(k) > maxLimiterKey {
		return k[:maxLimiterKey]
	}
	return k
}

// LoginLimiter enforces Q-4 for login: 5 failures / 15 min per (IP, username)
// and 20 attempts / min per IP.
type LoginLimiter struct {
	failures *Window
	attempts *Window
}

// NewLoginLimiter builds a LoginLimiter. A nil clock means time.Now.
func NewLoginLimiter(now func() time.Time) *LoginLimiter {
	return &LoginLimiter{
		failures: NewWindow(loginFailureMax, loginFailureWindow, now),
		attempts: NewWindow(loginAttemptMax, loginAttemptWindow, now),
	}
}

// Check reports whether a login attempt may proceed; when it may not, retry says
// for how long. Check does not count anything: call RecordAttempt/RecordFailure.
func (l *LoginLimiter) Check(ip, username string) (bool, time.Duration) {
	blockedA, retryA := l.attempts.Blocked(ip)
	blockedF, retryF := l.failures.Blocked(failureKey(ip, username))
	if !blockedA && !blockedF {
		return true, 0
	}
	if retryF > retryA {
		return false, retryF
	}
	return false, retryA
}

// RecordAttempt counts one attempt for the IP.
func (l *LoginLimiter) RecordAttempt(ip string) { l.attempts.Add(ip) }

// RecordFailure counts one failed login for (ip, username).
func (l *LoginLimiter) RecordFailure(ip, username string) { l.failures.Add(failureKey(ip, username)) }

// RecordSuccess clears the failure counter for (ip, username).
func (l *LoginLimiter) RecordSuccess(ip, username string) { l.failures.Reset(failureKey(ip, username)) }

func failureKey(ip, username string) string {
	return ip + "\x00" + strings.ToLower(strings.TrimSpace(username))
}
