package main

import (
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

func TestHostMailboxChildLaunchCommandFlags(t *testing.T) {
	cfg := &config.Config{Dir: wingpolicy.CanonicalPolicyPath(t.TempDir())}
	args, err := eggclient.ProtectedWriteTargetArgs([]string{cfg.Dir, wingpolicy.CanonicalPolicyPath("/opt/wt/bin/wt")})
	if err != nil {
		t.Fatal(err)
	}
	cmd := eggRunCmd()
	if err := cmd.ParseFlags(append([]string{"--session-id", "s1", "--" + eggclient.OmitBrowserBridgeArg}, args...)); err != nil {
		t.Fatal(err)
	}
	if omitted, err := cmd.Flags().GetBool(eggclient.OmitBrowserBridgeArg); err != nil || !omitted {
		t.Fatalf("omit-browser-bridge did not round trip: %v", err)
	}
	if flag := cmd.Flags().Lookup(eggclient.OmitBrowserBridgeArg); flag == nil || !flag.Hidden {
		t.Fatal("omit-browser-bridge must be a hidden internal flag")
	}
	plain := eggRunCmd()
	if err := plain.ParseFlags([]string{"--session-id", "s1"}); err != nil {
		t.Fatal(err)
	}
	if omitted, _ := plain.Flags().GetBool(eggclient.OmitBrowserBridgeArg); omitted {
		t.Fatal("ordinary egg runs must keep the browser bridge")
	}
}
