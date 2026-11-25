package admin

import (
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
)

// Content-Security-Policy of the admin UI (FR-054, spike-report (4)): scripts run only
// from this origin or by hash (the static export has inline bootstrap scripts), never
// 'unsafe-inline'/'unsafe-eval'. style-src keeps 'unsafe-inline' until the real UI is
// proven to work without it (React/Radix set style attributes at runtime).
const (
	cspBase = "default-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'; " +
		"style-src 'self' 'unsafe-inline'"
)

var inlineScriptRE = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
var scriptSrcAttrRE = regexp.MustCompile(`(?i)\bsrc\s*=`)

// InlineScriptHashes returns the CSP source expressions ('sha256-<base64>') of every
// inline <script> in html (scripts without a src attribute), deduplicated, in document
// order. The browser hashes the exact text between the tags.
func InlineScriptHashes(html []byte) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range inlineScriptRE.FindAllSubmatch(html, -1) {
		if scriptSrcAttrRE.Match(m[1]) || len(m[2]) == 0 {
			continue
		}
		sum := sha256.Sum256(m[2])
		h := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// BuildCSP is the policy for a document whose inline scripts hash to scriptHashes.
func BuildCSP(scriptHashes []string) string {
	script := "script-src 'self'"
	if len(scriptHashes) > 0 {
		script += " " + strings.Join(scriptHashes, " ")
	}
	return cspBase + "; " + script
}

// defaultCSP is the policy of every response that is not an HTML document with inline
// scripts (JSON, static assets, errors): scripts only from this origin.
var defaultCSP = BuildCSP(nil)
