// Package redact masks things that look like credentials in log lines. It is
// the second layer: envfile.Scrubber removes known values first, and these
// patterns catch secrets that never sat in an env file (user JWTs, API keys).
package redact

import "regexp"

const mask = "[redacted]"

type rule struct {
	re   *regexp.Regexp
	repl string
}

var rules = []rule{
	// scheme://user:password@host
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^\s/:@]+:[^\s/@]+@`), "${1}" + mask + "@"},
	// JSON Web Tokens
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`), mask},
	// Authorization: Bearer|Basic|Token xxx
	{regexp.MustCompile(`(?i)\b(bearer|basic|token)(\s+)[A-Za-z0-9._~+/=-]{8,}`), "${1}${2}" + mask},
	// well-known key prefixes: Stripe, GitHub, Resend, Slack, lk_ API keys, AWS
	{regexp.MustCompile(`\b(sk|pk|rk)_(live|test)_[A-Za-z0-9]{8,}|\bwhsec_[A-Za-z0-9]{8,}|\bgh[pousr]_[A-Za-z0-9]{20,}|\bgithub_pat_[A-Za-z0-9_]{20,}|\bre_[A-Za-z0-9_]{16,}|\bxox[abpr]-[A-Za-z0-9-]{10,}|\blk_[A-Za-z0-9_-]{8,}|\bAKIA[0-9A-Z]{16}`), mask},
	// key=value / "key": "value" where the key sounds secret
	{regexp.MustCompile(`(?i)((?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|authorization|cookie|signature|credential|session)[A-Za-z0-9_-]*["']?\s*[:=]\s*["']?)([^\s"',;&}]{4,})`), "${1}" + mask},
	// long opaque runs: hex or base64 of 32+ characters
	{regexp.MustCompile(`\b[A-Za-z0-9+/_-]{40,}={0,2}|\b[a-fA-F0-9]{32,}\b`), mask},
}

// Line masks credential-shaped substrings.
func Line(s string) string {
	for _, r := range rules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}
