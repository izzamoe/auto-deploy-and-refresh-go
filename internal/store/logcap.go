package store

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Log limits (Q-16, FR-050, FR-058).
const (
	// MaxJobLogBytes caps the service log stored with a deploy job.
	MaxJobLogBytes = 256 << 10
	// MaxLiveLogBytes caps a live log response.
	MaxLiveLogBytes = 2 << 20

	truncatedLogPrefix = "[... log truncated: "
)

// CapLogTail keeps the LAST max bytes of s (the end of a deploy or the newest journal
// lines are what matters), never splitting a UTF-8 sequence, and prepends a one-line
// marker when something was cut. truncated reports whether it was.
func CapLogTail(s string, max int) (out string, truncated bool) {
	if len(s) <= max {
		return s, false
	}
	cut := len(s) - max
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return fmt.Sprintf("%s%d earlier bytes omitted ...]\n", truncatedLogPrefix, cut) + s[cut:], true
}

// IsTruncatedJobLog reports whether a stored log was cut by CapLogTail.
func IsTruncatedJobLog(log string) bool { return strings.HasPrefix(log, truncatedLogPrefix) }
