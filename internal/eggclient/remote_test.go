package eggclient

import (
	"testing"
)

func TestRemoteExactAgentArgsRemainNULSafe(t *testing.T) {
	if err := validateExactAgentArgs([]string{"", " \t ", "--tools"}); err != nil {
		t.Fatalf("exact remote argv rejected: %v", err)
	}
	if err := validateExactAgentArgs([]string{"bad\x00arg"}); err == nil {
		t.Fatal("exact remote argv accepted NUL")
	}
}
