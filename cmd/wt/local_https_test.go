package main

import (
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/localrelay"
)

func TestLocalCertHelpStatesPrivateKeyBoundary(t *testing.T) {
	cmd := localCertCmd()
	if !strings.Contains(cmd.Long, "private key") || !strings.Contains(cmd.Long, "never leaves this machine") {
		t.Fatalf("local-cert help omits key boundary: %q", cmd.Long)
	}
}

func TestServeHTTPSFlagsAreOptIn(t *testing.T) {
	cmd := serveCmd()
	https, err := cmd.Flags().GetBool("https")
	if err != nil {
		t.Fatal(err)
	}
	if https {
		t.Fatal("hosted/upstream serve unexpectedly enables a local CA")
	}
	if got, err := cmd.Flags().GetString("https-addr"); err != nil || got != localrelay.DefaultLocalHTTPSAddr {
		t.Fatalf("https-addr = %q, %v", got, err)
	}
}

func TestRoostHTTPSFlagsAreOptIn(t *testing.T) {
	cmd := roostStartCmd()
	https, err := cmd.Flags().GetBool("https")
	if err != nil {
		t.Fatal(err)
	}
	if https {
		t.Fatal("existing roost unexpectedly enables trust-store mutation")
	}
	if got, err := cmd.Flags().GetString("https-addr"); err != nil || got != localrelay.DefaultLocalHTTPSAddr {
		t.Fatalf("https-addr = %q, %v", got, err)
	}
}
