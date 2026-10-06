package main

import (
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func TestSessionLifecycleCommandExposure(t *testing.T) {
	cmd := sessionCmd()
	for _, name := range []string{"status", "transcript", "await"} {
		found, _, err := cmd.Find([]string{name})
		if err != nil || found.Name() != name || found.Flags().Lookup("json") == nil {
			t.Fatalf("%s missing typed CLI: %v", name, err)
		}
	}
	if err := eggclient.ValidateLifecycleWait("silent"); err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatal("accepted terminal silence as lifecycle state")
	}
}
