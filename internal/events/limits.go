package events

import "time"

// Abuse limits of events-ws.v3.md section 6 and subscription.md section 3 (FR-016, NFR-008).
const (
	// MaxReadBytes is the transport read limit of a client frame: a frame up to this size is
	// read and refused with error{frame_too_large} when it exceeds MaxFrameBytes (so the
	// violation can be counted); a larger one makes the WebSocket library close 1009.
	MaxReadBytes = 16 * MaxFrameBytes
	// OpsWindow is the window of the op token bucket (MaxOpsPer10s ops per window).
	OpsWindow = 10 * time.Second
	// MaxInvalidJSON invalid frames count as one violation.
	MaxInvalidJSON = 5
	// ViolationLimit violations within ViolationWindow close the socket with 4429.
	ViolationLimit  = 3
	ViolationWindow = 60 * time.Second
)

// Limiter is the per-connection op policy: a token bucket of MaxOpsPer10s ops per
// OpsWindow and a sliding record of violations. Used by the connection's single op loop,
// so it is not safe for concurrent use.
type Limiter struct {
	tokens      float64
	last        time.Time
	invalidJSON int
	violations  []time.Time
}

// NewLimiter starts with a full bucket.
func NewLimiter(now time.Time) *Limiter {
	return &Limiter{tokens: MaxOpsPer10s, last: now}
}

// Allow takes one op token; false means the op is refused with error{rate_limited}.
func (l *Limiter) Allow(now time.Time) bool {
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.tokens += elapsed.Seconds() * MaxOpsPer10s / OpsWindow.Seconds()
		if l.tokens > MaxOpsPer10s {
			l.tokens = MaxOpsPer10s
		}
		l.last = now
	}
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

// InvalidJSON counts a frame that was not a JSON object; every MaxInvalidJSON-th one is a
// violation (true).
func (l *Limiter) InvalidJSON() bool {
	l.invalidJSON++
	if l.invalidJSON >= MaxInvalidJSON {
		l.invalidJSON = 0
		return true
	}
	return false
}

// Violation records one violation; true means it is the ViolationLimit-th within
// ViolationWindow and the socket must close with 4429.
func (l *Limiter) Violation(now time.Time) bool {
	l.violations = append(l.violations, now)
	if len(l.violations) > ViolationLimit {
		l.violations = l.violations[len(l.violations)-ViolationLimit:]
	}
	return len(l.violations) == ViolationLimit && now.Sub(l.violations[0]) < ViolationWindow
}
