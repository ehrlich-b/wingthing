package agent

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

var secretName = regexp.MustCompile(`(?i)(key|token|secret|password|passwd|credential|authorization|cookie)`)
var bearerToken = regexp.MustCompile(`(?i)(bearer[ \t]+)[A-Za-z0-9._~+/=-]+`)
var commonToken = regexp.MustCompile(`\b(sk-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9_]{8,}|github_pat_[A-Za-z0-9_]{8,}|xox[baprs]-[A-Za-z0-9-]{8,}|AIza[A-Za-z0-9_-]{20,}|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b`)

// Redactor is a run-local snapshot of credentials. Use it before persisting,
// returning, or truncating any provider text, including successful messages.
type Redactor struct {
	secrets []string
}

func NewRedactor(environment []string, credentials ...string) *Redactor {
	secrets := append([]string(nil), credentials...)
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" {
			continue
		}
		if secretName.MatchString(name) {
			secrets = append(secrets, value)
		}
		// Connection strings can carry passwords under otherwise ordinary names.
		if u, err := url.Parse(value); err == nil {
			if u.User != nil {
				if password, ok := u.User.Password(); ok {
					secrets = append(secrets, password)
				}
			}
			for key, values := range u.Query() {
				if secretName.MatchString(key) {
					secrets = append(secrets, values...)
				}
			}
		}
	}
	unique := make(map[string]bool)
	for _, value := range secrets {
		if value == "" {
			continue
		}
		unique[value] = true
		// Stderr may quote credentials as JSON rather than decoded event text.
		encoded, _ := json.Marshal(value)
		unique[string(encoded[1:len(encoded)-1])] = true
	}
	r := &Redactor{}
	for value := range unique {
		r.secrets = append(r.secrets, value)
	}
	// Overlapping credentials must never leave the suffix of the longer one.
	sort.Slice(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
	return r
}

func (r *Redactor) Text(text string) string {
	if r != nil {
		for _, secret := range r.secrets {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	text = bearerToken.ReplaceAllString(text, "${1}[redacted]")
	return commonToken.ReplaceAllString(text, "[redacted]")
}

func (r *Redactor) Error(err error) error {
	if err == nil {
		return nil
	}
	text := r.Text(err.Error())
	if text == err.Error() {
		return err
	}
	return &redactedError{text: text, cause: err}
}

type redactedError struct {
	text  string
	cause error
}

func (e *redactedError) Error() string { return e.text }
func (e *redactedError) Unwrap() error { return e.cause }
