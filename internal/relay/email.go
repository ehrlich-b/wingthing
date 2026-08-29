package relay

import (
	"fmt"
	"net/mail"
	"strings"
	"unicode"
)

// normalizeBareEmail accepts one mailbox without a display name or comments.
// Keeping the stored form equal to the SMTP header form prevents user input
// from becoming additional message headers.
func normalizeBareEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("invalid email address")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Name != "" || address.Address != value {
		return "", fmt.Errorf("invalid email address")
	}
	return value, nil
}

func smtpHeaderValue(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
}
