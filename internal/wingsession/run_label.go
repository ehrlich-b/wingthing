package wingsession

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxRunLabelRunes = 200

// NormalizeRunLabel accepts display text, independently of session names.
// Collapse whitespace and remove terminal controls and invisible formatting
// characters before persisting or returning the label.
func NormalizeRunLabel(label string) (string, error) {
	if !utf8.ValidString(label) || utf8.RuneCountInString(label) > MaxRunLabelRunes {
		return "", errors.New("label must be UTF-8 text of at most 200 characters")
	}
	label = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, label)
	return strings.Join(strings.Fields(label), " "), nil
}
