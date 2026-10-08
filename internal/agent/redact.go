package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"
)

var secretName = regexp.MustCompile(`(?i)(?:^|_)(?:key|token|secret|password|passwd|passphrase|credentials?|authorization|cookies?|auth|pass)(?:_|$)`)

const redactionMarker = "[redacted]"

// Redactor is a run-local snapshot of credentials. Use it before persisting,
// returning, or truncating any provider text, including successful messages.
type Redactor struct {
	secrets   []string
	initials  [256]bool
	byInitial [256][]string
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
				if password, ok := u.User.Password(); ok && plausibleSecret(password) {
					secrets = append(secrets, password, value)
				}
			}
			for key, values := range u.Query() {
				if secretName.MatchString(key) {
					for _, credential := range values {
						if plausibleSecret(credential) {
							secrets = append(secrets, credential, value)
						}
					}
				}
			}
		}
	}
	unique := make(map[string]bool)
	for _, value := range secrets {
		if !plausibleSecret(value) {
			continue
		}
		for _, encoded := range secretEncodings(value) {
			unique[encoded] = true
		}
	}
	r := &Redactor{}
	for value := range unique {
		r.secrets = append(r.secrets, value)
		r.initials[value[0]] = true
	}
	// Overlapping credentials must never leave the suffix of the longer one.
	sort.Slice(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
	for _, secret := range r.secrets {
		r.byInitial[secret[0]] = append(r.byInitial[secret[0]], secret)
	}
	return r
}

// Environment values must come from a secret-named component, not incidental
// names like KEYCHAIN_ENABLED. Explicit credentials and URL password/query
// fields are also known sources. All require at least eight runes, four distinct
// runes, and two bits of Shannon entropy per rune; flags and short/low-diversity
// values would otherwise corrupt ordinary output. See docs/security.md.
func plausibleSecret(value string) bool {
	counts := make(map[rune]int)
	length := 0
	for _, r := range value {
		counts[r]++
		length++
	}
	if length < 8 || len(counts) < 4 {
		return false
	}
	entropy := 0.0
	for _, count := range counts {
		p := float64(count) / float64(length)
		entropy -= p * math.Log2(p)
	}
	return entropy >= 2
}

// Generate a fixed set of common representations, never recursively encode
// variants. Storage and matching cost stay linear in credential size.
func secretEncodings(value string) []string {
	quoted, _ := json.Marshal(value)
	jsonText := string(quoted[1 : len(quoted)-1])
	var minimal strings.Builder
	encoder := json.NewEncoder(&minimal)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	minimalText := minimal.String()[1 : minimal.Len()-2]
	var percent strings.Builder
	for i := 0; i < len(value); i++ {
		fmt.Fprintf(&percent, "%%%02X", value[i])
	}
	variants := []string{value, url.QueryEscape(value), url.PathEscape(value), percent.String(), jsonText, minimalText,
		jsonUnicode(jsonText, false), jsonUnicode(minimalText, false), jsonUnicode(value, true),
		strings.ReplaceAll(minimalText, "/", `\/`),
		base64.StdEncoding.EncodeToString([]byte(value)), base64.RawStdEncoding.EncodeToString([]byte(value)),
		base64.URLEncoding.EncodeToString([]byte(value)), base64.RawURLEncoding.EncodeToString([]byte(value))}
	for _, encoded := range variants[:9] {
		variants = append(variants, escapeHexCase(encoded, false), escapeHexCase(encoded, true))
	}
	return variants
}

func jsonUnicode(value string, all bool) string {
	var output strings.Builder
	for _, r := range value {
		if !all && r < 128 {
			output.WriteRune(r)
			continue
		}
		for _, unit := range utf16.Encode([]rune{r}) {
			fmt.Fprintf(&output, `\u%04x`, unit)
		}
	}
	return output.String()
}

func escapeHexCase(value string, upper bool) string {
	data := []byte(value)
	for i := 0; i < len(data); i++ {
		start, count := i+1, 0
		if data[i] == '%' && i+2 < len(data) {
			count = 2
		} else if data[i] == '\\' && i+5 < len(data) && data[i+1] == 'u' {
			start, count = i+2, 4
		}
		for j := start; j < start+count; j++ {
			if upper && data[j] >= 'a' && data[j] <= 'f' {
				data[j] -= 'a' - 'A'
			} else if !upper && data[j] >= 'A' && data[j] <= 'F' {
				data[j] += 'a' - 'A'
			}
		}
		if count > 0 {
			i = start + count - 1
		}
	}
	return string(data)
}

func (r *Redactor) Text(text string) string {
	known, _ := r.knownText(text, true)
	var shapes shapeRedaction
	return shapes.write(known) + shapes.finish()
}

// streamText exposes one bounded held window for diagnostic probes. Live
// streams also retain shapeRedaction's continuation state between writes.
func (r *Redactor) streamText(text string) (ready, pending string) {
	known, pending := r.knownText(text, false)
	var shapes shapeRedaction
	ready = shapes.write(known)
	return ready, shapes.pending + pending
}

func (r *Redactor) knownText(text string, final bool) (ready, pending string) {
	if r == nil {
		r = &Redactor{}
	}
	var output strings.Builder
	for text != "" {
		next := 0
		for next < len(text) && !r.initials[text[next]] && text[next] != '[' {
			next++
		}
		output.WriteString(text[:next])
		text = text[next:]
		if text == "" {
			break
		}
		if strings.HasPrefix(text, redactionMarker) {
			output.WriteString(redactionMarker)
			text = text[len(redactionMarker):]
			continue
		}
		if !final && len(text) < len(redactionMarker) && strings.HasPrefix(redactionMarker, text) {
			return output.String(), strings.Clone(text)
		}
		matched := 0
		for _, secret := range r.byInitial[text[0]] {
			if !final && len(text) < len(secret) && strings.HasPrefix(secret, text) {
				return output.String(), strings.Clone(text)
			}
			if matched == 0 && strings.HasPrefix(text, secret) {
				matched = len(secret)
			}
		}
		if matched > 0 {
			output.WriteString(redactionMarker)
			text = text[matched:]
		} else {
			output.WriteByte(text[0])
			text = text[1:]
		}
	}
	return output.String(), ""
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
