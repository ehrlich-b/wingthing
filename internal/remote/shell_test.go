package remote

import (
	"testing"
)

func TestShellQuote(t *testing.T) {
	if got, want := ShellQuote("/opt/wing thing/wt"), "'/opt/wing thing/wt'"; got != want {
		t.Fatalf("shellQuote path = %q, want %q", got, want)
	}
	if got, want := ShellQuote("it's"), "'it'\"'\"'s'"; got != want {
		t.Fatalf("shellQuote apostrophe = %q, want %q", got, want)
	}
}
