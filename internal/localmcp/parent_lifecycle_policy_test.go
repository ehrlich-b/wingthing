package localmcp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
)

// Exercise the built Claude fixture's physical layout and its actual preflight
// without launching providers, sandbox-exec, a proxy, or any listener.
func TestParentLifecycleScopedFixtureProtection(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the scoped parent's write-deny preflight models macOS Seatbelt")
	}
	previous := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = previous })
	scratch, err := filepath.Abs("../../.scratch")
	if err != nil {
		t.Fatal(err)
	}
	home, localState, remoteState := testprovider.ScopedHomeDirs(t, scratch)
	tmp, binary := filepath.Join(home, "t"), filepath.Join(home, "wt")
	if err := os.Mkdir(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("fixture controller"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", tmp)
	policy, err := testprovider.ScopedHomePolicy(home).YAML()
	if err != nil {
		t.Fatal(err)
	}
	for i, state := range []string{localState, remoteState} {
		workspace := filepath.Join(home, []string{"work", "remote-work"}[i])
		if err := os.Mkdir(workspace, 0700); err != nil {
			t.Fatal(err)
		}
		policyPath := filepath.Join(state, "egg.yaml")
		if err := os.WriteFile(policyPath, []byte(policy), 0600); err != nil {
			t.Fatal(err)
		}
		loaded, err := egg.ResolveEggConfig(policyPath)
		if err != nil {
			t.Fatal(err)
		}
		id := cmdutil.NewRuntimeID()
		endpoint := filepath.Join(state, "eggs", id, "egg.sock")
		if len(endpoint) > 103 || filepath.Dir(state) != home {
			t.Fatalf("state escaped HOME or exceeded Darwin's socket limit: %s (%d bytes)", endpoint, len(endpoint))
		}
		if control, err := controlsocket.Path(state); err != nil || control != filepath.Join(state, controlsocket.SocketName) {
			t.Fatalf("control socket escaped physical state: %s %v", control, err)
		}
		cfg := &config.Config{Dir: state}
		for _, agent := range []string{"claude", "codex"} {
			targets := []string{binary, endpoint, policyPath}
			if err := defaultConversationBrokerProtection(cfg, loaded, agent, workspace, id, eggclient.EggIdentity{}, targets); err != nil {
				t.Fatalf("%s fixture protection: %v", agent, err)
			}
			for _, exposed := range []string{filepath.Join(scratch, "outside-scoped-home"), workspace, tmp} {
				unsafe := &config.Config{Dir: exposed}
				if err := defaultConversationBrokerProtection(unsafe, loaded, agent, workspace, id, eggclient.EggIdentity{}, targets); err == nil || !strings.Contains(err.Error(), "provider-writable") {
					t.Fatalf("%s provider-writable state accepted at %s: %v", agent, exposed, err)
				}
			}
		}
		if err := defaultConversationBrokerProtection(cfg, egg.DefaultEggConfig(), "claude", workspace, id, eggclient.EggIdentity{}, nil); err == nil || !strings.Contains(err.Error(), "ancestor") {
			t.Fatalf("unpinned fixture HOME accepted: %v", err)
		}
		t.Logf("protected fixture socket: %d bytes", len(endpoint))
	}
}
