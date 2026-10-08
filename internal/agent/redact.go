package agent

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

var secretName = regexp.MustCompile(`(?i)(key|token|secret|password|passwd|passphrase|credential|authorization|cookie|auth(?:_|$)|(?:^|_)pass(?:_|$))`)
var bearerToken = regexp.MustCompile(`(?i)(bearer[ \t]+)[A-Za-z0-9._~+/=-]+`)
var commonToken = regexp.MustCompile(`\b(sk-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9_]{8,}|github_pat_[A-Za-z0-9_]{8,}|xox[baprs]-[A-Za-z0-9-]{8,}|AIza[A-Za-z0-9_-]{20,}|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b`)

// Redactor is a run-local snapshot of credentials. Use it before persisting,
// returning, or truncating any provider text, including successful messages.
type Redactor struct {
	secrets  []string
	initials [256]bool
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
					secrets = append(secrets, password, value)
				}
			}
			for key, values := range u.Query() {
				if secretName.MatchString(key) {
					secrets = append(secrets, values...)
					secrets = append(secrets, value)
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
		r.initials[value[0]] = true
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

// streamText withholds only a trailing prefix of a known credential. The
// withheld text is shorter than the longest credential, regardless of provider
// output size. This also handles overlapping credentials without emitting the
// suffix of a longer one after redacting its shorter prefix.
func (r *Redactor) streamText(text string) (ready, pending string) {
	if r == nil || len(r.secrets) == 0 {
		return r.Text(text), ""
	}
	var output strings.Builder
	for text != "" {
		next := 0
		for next < len(text) && !r.initials[text[next]] {
			next++
		}
		output.WriteString(text[:next])
		text = text[next:]
		if text == "" {
			break
		}
		matched := 0
		for _, secret := range r.secrets {
			if len(text) < len(secret) && strings.HasPrefix(secret, text) {
				return r.Text(output.String()), text
			}
			if matched == 0 && strings.HasPrefix(text, secret) {
				matched = len(secret)
			}
		}
		if matched > 0 {
			output.WriteString("[redacted]")
			text = text[matched:]
		} else {
			output.WriteByte(text[0])
			text = text[1:]
		}
	}
	return r.Text(output.String()), ""
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
