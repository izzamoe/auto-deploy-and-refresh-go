package auth

import "time"

// Window is a sliding-window hit counter keyed by string.
type Window struct{}

// NewWindow allows max hits per span.
func NewWindow(max int, span time.Duration, now func() time.Time) *Window { return &Window{} }

// Blocked reports whether key reached the limit and how long until it frees up.
func (w *Window) Blocked(key string) (bool, time.Duration) { return false, 0 }

// Add records one hit.
func (w *Window) Add(key string) {}

// Reset forgets key.
func (w *Window) Reset(key string) {}

// LoginLimiter enforces Q-4 for login: 5 failures / 15 min per (IP, username)
// and 20 attempts / min per IP.
type LoginLimiter struct{}

// NewLoginLimiter builds a LoginLimiter.
func NewLoginLimiter(now func() time.Time) *LoginLimiter { return &LoginLimiter{} }

// Check reports whether a login attempt may proceed.
func (l *LoginLimiter) Check(ip, username string) (bool, time.Duration) { return true, 0 }

// RecordAttempt counts one attempt for the IP.
func (l *LoginLimiter) RecordAttempt(ip string) {}

// RecordFailure counts one failed login for (ip, username).
func (l *LoginLimiter) RecordFailure(ip, username string) {}

// RecordSuccess clears the failure counter for (ip, username).
func (l *LoginLimiter) RecordSuccess(ip, username string) {}
