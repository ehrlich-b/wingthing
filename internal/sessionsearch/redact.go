package sessionsearch

import (
	"regexp"
	"strings"
	"unicode"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(-----END [A-Z ]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`(?i)\b(?:authorization\s*[:=]\s*(?:bearer\s+|basic\s+)?|bearer\s+|(?:[a-z_]*api[_-]?key|[a-z_]*secret|[a-z_]*token|password|passwd|credential)\s*["']?\s*[:=]\s*["']?)[^\s"'<>]+`),
	regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{12,}|gh[pousr]_[A-Za-z0-9_]{12,}|github_pat_[A-Za-z0-9_]{12,}|AKIA[A-Z0-9]{16})\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`),
	regexp.MustCompile(`[A-Za-z0-9_+/=-]{32,}`),
	regexp.MustCompile(`(?i)https?://[^\s/@]+:[^\s/@]+@[^\s]+`),
}
var escapePattern = regexp.MustCompile("\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\][^\x07]*(?:\x07|$))")

// Redact removes recognized credentials before indexing or matching metadata.
// This is best-effort recognition, not a guarantee for arbitrary secret values.
func Redact(text string) string {
	text = escapePattern.ReplaceAllString(text, "")
	for _, pattern := range secretPatterns {
		text = pattern.ReplaceAllString(text, " [redacted] ")
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
}
