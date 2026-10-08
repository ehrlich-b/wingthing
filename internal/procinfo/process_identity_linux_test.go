//go:build linux

package procinfo

import (
	"strings"
	"testing"
)

func TestLinuxProcessStartIgnoresCommandDelimiters(t *testing.T) {
	stat := "42 (command ) with spaces) S " + strings.Repeat("0 ", 18) + "1234 0"
	if start, err := linuxProcessStart(stat); err != nil || start != "1234" {
		t.Fatalf("stat start time: %q %v", start, err)
	}
	for _, stat := range []string{"invalid", "42 (command) S 0", "42 (zombie) Z " + strings.Repeat("0 ", 20)} {
		if _, err := linuxProcessStart(stat); err == nil {
			t.Fatal("invalid or zombie process stat accepted")
		}
	}
}
