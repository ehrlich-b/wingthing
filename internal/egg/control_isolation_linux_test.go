//go:build linux

package egg

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func init() {
	if len(os.Args) > 1 && os.Args[1] == "_deny_init" {
		sandbox.DenyInit(os.Args[2:])
	}
}

func TestLinuxControlPolicyPreservesBridgesAndBlocksFutureSiblings(t *testing.T) {
	if ok, reason := sandbox.CheckCapability(); !ok {
		t.Skipf("namespaces unavailable: %s", reason)
	}
	root, control, bridges, capability := controlIsolationFixture(t)
	createIsolationSibling(t, root, "sibling")
	own := filepath.Join(root, "state", "eggs", "own")
	ownControl, err := net.Listen("unix", filepath.Join(own, "egg.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ownControl.Close() })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	mounts := []sandbox.Mount{{Source: root, Target: root, ReadOnly: true}, {Source: filepath.Join(root, ".claude"), Target: filepath.Join(root, ".claude")}, {Source: filepath.Dir(exe), Target: filepath.Dir(exe), ReadOnly: true}}
	for _, path := range []string{"/usr", "/bin", "/lib", "/lib64"} {
		if _, err := os.Stat(path); err == nil {
			mounts = append(mounts, sandbox.Mount{Source: path, Target: path, ReadOnly: true})
		}
	}
	mounts, err = isolateLinuxEggControl(mounts, control, bridges)
	if err != nil {
		t.Fatal(err)
	}
	// This launch must exclude a sibling that did not exist when the
	// allowlist was compiled, without needing any refreshed deny list.
	createIsolationSibling(t, root, "future")
	home := filepath.Join(own, ".sandbox-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := sandbox.Config{Mounts: mounts, Deny: []string{"/", filepath.Join(root, "state", "wing_key"), filepath.Join(root, "state", "device_token.yaml")}, UserHome: home, NetworkNeed: sandbox.NetworkNone}
	sb, err := sandbox.New(policy)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Destroy() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd, err := sb.Exec(ctx, exe, []string{"-test.run=^TestEggControlIsolationProcess$", "-test.v"})
	if err != nil {
		t.Fatal(err)
	}
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "WT_TEST_CONTROL_ROOT="+root, ToolCapabilityEnv+"="+capability)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("control isolation: %v\n%s", err, output)
	}
}
