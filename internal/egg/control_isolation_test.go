package egg

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func TestDefaultPolicyDeniesWingthingControlCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "custom"))
	cfg := DefaultEggConfig().ToSandboxConfig(home)
	for _, state := range []string{".wingthing", ".wingthing-preview", "custom"} {
		for _, name := range []string{"eggs", "device_token.yaml", "local_device_token.yaml", "wing_key", "sync.key"} {
			path := config.CanonicalProviderPath(filepath.Join(home, state, name))
			found := false
			for _, denied := range cfg.Deny {
				if denied == path {
					found = true
				}
			}
			if !found {
				t.Errorf("controller path readable: %s", path)
			}
		}
	}
}

func TestEggRefusesAnotherSessionsToolSocket(t *testing.T) {
	root := filepath.Dir(shortSockPath(t))
	s := &Server{dir: filepath.Join(root, "eggs", "own")}
	err := s.RunSession(context.Background(), RunConfig{ToolSocketPath: filepath.Join(root, "eggs", "sibling", ".tools", "tool.sock")})
	if err == nil || !strings.Contains(err.Error(), "tool socket must belong") {
		t.Fatalf("sibling tool socket admitted: %v", err)
	}
}

func TestLinuxControlCompilationPreservesImplicitReadRoot(t *testing.T) {
	for _, test := range []struct {
		name     string
		mounts   []sandbox.Mount
		deny     []string
		wantRoot bool
	}{
		{name: "no FS rules", wantRoot: true},
		{name: "workspace only", mounts: []sandbox.Mount{{Source: "/workspace"}}, wantRoot: true},
		{name: "explicit jail", mounts: []sandbox.Mount{{Source: "/workspace"}}, deny: []string{"/"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			mounts := linuxEggReadMounts(test.mounts, test.deny)
			found := false
			for _, mount := range mounts {
				found = found || (mount.Source == "/" && mount.ReadOnly)
			}
			if found != test.wantRoot {
				t.Fatalf("implicit read root = %v, want %v: %+v", found, test.wantRoot, mounts)
			}
		})
	}
}

func TestLinuxControlAllowlistExcludesExistingAndFutureEggs(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "state", "eggs")
	own := filepath.Join(tree, "own")
	for _, path := range []string{own, filepath.Join(tree, "sibling"), filepath.Join(root, "workspace"), filepath.Join(root, ".claude")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(root, "egg-alias")
	if err := os.Symlink(tree, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "root-alias")); err != nil {
		t.Fatal(err)
	}
	bridges := []sandbox.Mount{{Source: filepath.Join(own, ".tools"), ReadOnly: true}, {Source: filepath.Join(own, "browser-requests")}}
	mounts, err := isolateLinuxEggControl([]sandbox.Mount{{Source: root, ReadOnly: true}, {Source: alias}, {Source: filepath.Join(root, ".claude")}}, []string{tree}, bridges)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(own, "egg.token"), filepath.Join(own, "egg.sock"), filepath.Join(tree, "sibling", "tool.sock"), filepath.Join(tree, "future", "egg.token"), filepath.Join(alias, "sibling", "egg.token"), filepath.Join(root, "root-alias", "state", "eggs", "sibling", "egg.token")} {
		canonical := canonicalPolicyTestPathIfMissing(path)
		for _, mount := range mounts {
			if controlPathWithin(canonical, canonicalPolicyTestPathIfMissing(mount.Source)) {
				t.Errorf("%s exposed by mount %+v", path, mount)
			}
		}
	}
	for _, required := range append(bridges, sandbox.Mount{Source: filepath.Join(root, ".claude")}) {
		found := false
		for _, mount := range mounts {
			if mount.Source == required.Source && mount.ReadOnly == required.ReadOnly {
				found = true
			}
		}
		if !found {
			t.Errorf("legitimate mount missing: %+v", required)
		}
	}
}

func canonicalPolicyTestPathIfMissing(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path
	}
	return filepath.Join(canonicalPolicyTestPathIfMissing(parent), filepath.Base(path))
}

func TestLinuxControlAllowlistSplitsAncestorAliasesIntoCanonicalMounts(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	tree := filepath.Join(state, "eggs")
	logs := filepath.Join(state, "logs")
	for _, path := range []string{tree, logs} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(state, alias); err != nil {
		t.Fatal(err)
	}
	mounts, err := isolateLinuxEggControl([]sandbox.Mount{{Source: alias, Target: alias, ReadOnly: true}}, []string{tree}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].Source != canonicalPolicyTestPathIfMissing(logs) || mounts[0].Target != mounts[0].Source || !mounts[0].ReadOnly {
		t.Fatalf("split alias emitted an unsafe or missing mount: %+v", mounts)
	}
}

// The child runs inside the OS policy against disposable files and sockets.
func TestEggControlIsolationProcess(t *testing.T) {
	root := os.Getenv("WT_TEST_CONTROL_ROOT")
	if root == "" {
		t.Skip("sandbox subprocess only")
	}
	if os.Getenv("WT_TEST_DOTFILE_ALIAS") != "" {
		path := filepath.Join(root, ".zshrc")
		if data, err := os.ReadFile(path); err != nil || string(data) != "real dotfile symlink" {
			t.Fatalf("dotfile alias: %q %v", data, err)
		}
		if err := os.WriteFile(path, []byte("changed"), 0600); err == nil {
			t.Fatal("dotfile alias writable")
		}
	}
	tree := filepath.Join(root, "state", "eggs")
	own := filepath.Join(tree, "own")
	for _, path := range []string{filepath.Join(own, "egg.token"), filepath.Join(tree, "sibling", "egg.token"), filepath.Join(tree, "future", "egg.token"), filepath.Join(root, "state", "wing_key"), filepath.Join(root, "state", "device_token.yaml")} {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			t.Errorf("read denied controller credential %s", path)
		}
	}
	for _, path := range []string{filepath.Join(own, "egg.sock"), filepath.Join(tree, "sibling", ".tools", "tool.sock"), filepath.Join(tree, "future", "egg.sock")} {
		if conn, err := net.Dial("unix", path); err == nil {
			_ = conn.Close()
			t.Errorf("connected to denied controller socket %s", path)
		}
	}
	if os.Getenv("WT_TEST_BIND_ALIASES") != "" {
		for _, path := range []string{filepath.Join(root, "mnt", "state", "wing_key"), filepath.Join(root, "mnt", "state", "eggs", "future", "egg.token"), filepath.Join(root, "mnt", "sibling", "egg.token")} {
			if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
				t.Errorf("physical control alias readable: %s", path)
			}
		}
	}
	if target, err := os.ReadFile(filepath.Join(root, "process-target")); err == nil {
		output, _ := exec.Command("/bin/ps", "eww", "-p", strings.TrimSpace(string(target))).CombinedOutput()
		if strings.Contains(string(output), "WT_OTHER_EGG_CAPABILITY=fixture-only-secret") {
			t.Error("read another egg's environment capability")
		}
	}
	if _, err := os.ReadFile(filepath.Join(own, "shims", "wt-browser")); err != nil {
		t.Error("browser shim unreadable", err)
	}
	for _, path := range []string{filepath.Join(own, "browser-requests"), filepath.Join(root, ".claude", "wingthing-events", "own", "event.json")} {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Error("legitimate bridge write failed", err)
			continue
		}
		if _, err := f.WriteString("legitimate\n"); err != nil {
			t.Error(err)
		}
		_ = f.Close()
	}
	if _, err := os.ReadFile(filepath.Join(own, "lifecycle-settings.json")); err != nil {
		t.Error("native lifecycle settings unreadable", err)
	}
	response := toolCall(t, filepath.Join(own, ".tools", "tool.sock"), ToolRequest{Tool: "echo", Args: []string{"works"}, Capability: os.Getenv(ToolCapabilityEnv)})
	if response.Error != "" || response.Stdout != "works\n" {
		t.Errorf("own authenticated tool failed: %#v", response)
	}
}

func controlIsolationFixture(t *testing.T) (string, []string, []sandbox.Mount, string) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "wt-ci-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	own := filepath.Join(root, "state", "eggs", "own")
	for _, dir := range []string{filepath.Join(own, "shims"), filepath.Join(own, ".tools"), filepath.Join(root, ".claude", "wingthing-events", "own")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(own, "egg.token"), filepath.Join(own, "browser-requests"), filepath.Join(own, "lifecycle-settings.json"), filepath.Join(own, "shims", "wt-browser"), filepath.Join(root, "state", "wing_key"), filepath.Join(root, "state", "device_token.yaml")} {
		if err := os.WriteFile(path, []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	toolPath := filepath.Join(own, ".tools", "tool.sock")
	listener, err := NewToolListener(toolPath, []*config.ToolConfig{{Name: "echo", Run: `echo "$1"`}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeToolListenerForTest(t, listener) })
	control := []string{filepath.Join(root, "state", "eggs"), filepath.Join(root, "state", "wing_key"), filepath.Join(root, "state", "device_token.yaml")}
	bridges := []sandbox.Mount{
		{Source: filepath.Join(own, ".tools"), Target: filepath.Join(own, ".tools"), ReadOnly: true},
		{Source: filepath.Join(own, "shims"), Target: filepath.Join(own, "shims"), ReadOnly: true},
		{Source: filepath.Join(own, "browser-requests"), Target: filepath.Join(own, "browser-requests")},
		{Source: filepath.Join(own, "lifecycle-settings.json"), Target: filepath.Join(own, "lifecycle-settings.json"), ReadOnly: true},
	}
	return root, control, bridges, listener.capability
}

func createIsolationSibling(t *testing.T, root, session string) {
	t.Helper()
	dir := filepath.Join(root, "state", "eggs", session)
	if err := os.MkdirAll(filepath.Join(dir, ".tools"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.token"), []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "egg.sock"), filepath.Join(dir, ".tools", "tool.sock")} {
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
	}
}

func TestDarwinControlPolicyPreservesBridgesAndBlocksFutureSiblings(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("WT_TEST_SEATBELT_ENFORCEMENT") != "1" {
		t.Skip("set WT_TEST_SEATBELT_ENFORCEMENT=1 on an unsandboxed Mac")
	}
	root, control, bridges, capability := controlIsolationFixture(t)
	victim := exec.Command("/bin/sleep", "60")
	victim.Env = []string{"PATH=/usr/bin:/bin", "WT_OTHER_EGG_CAPABILITY=fixture-only-secret"}
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = victim.Process.Kill(); _ = victim.Wait() })
	if err := os.WriteFile(filepath.Join(root, "process-target"), []byte(fmt.Sprint(victim.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	createIsolationSibling(t, root, "sibling")
	ownControl, err := net.Listen("unix", filepath.Join(root, "state", "eggs", "own", "egg.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ownControl.Close() })
	sb, err := sandbox.New(sandbox.Config{
		Mounts:               append([]sandbox.Mount{{Source: root}}, bridges...),
		NetworkNeed:          sandbox.NetworkNone,
		ControlDenyPaths:     control,
		ControlBridges:       bridges,
		ControlSocket:        filepath.Join(root, "state", "eggs", "own", ".tools", "tool.sock"),
		DenyOtherProcessInfo: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sb.Destroy(); err != nil {
			t.Errorf("destroy sandbox: %v", err)
		}
	})
	createIsolationSibling(t, root, "future")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd, err := sb.Exec(ctx, exe, []string{"-test.run=^TestEggControlIsolationProcess$", "-test.v"})
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(os.Environ(), "WT_TEST_CONTROL_ROOT="+root, ToolCapabilityEnv+"="+capability)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("control isolation: %v\n%s", err, output)
	}
}

func TestLinuxControlAllowlistPreservesReadOnlyDotfileAliases(t *testing.T) {
	home := canonicalPolicyTestPath(t, t.TempDir())
	t.Setenv("HOME", home)
	tree := filepath.Join(home, ".wingthing", "eggs")
	dotfiles := filepath.Join(home, "dotfiles")
	for _, dir := range []string{tree, dotfiles} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(dotfiles, "zshrc")
	if err := os.WriteFile(target, []byte("safe dotfile"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, ".zshrc")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tree, filepath.Join(home, "unsafe")); err != nil {
		t.Fatal(err)
	}
	mounts, err := isolateLinuxEggControl([]sandbox.Mount{{Source: home}}, []string{tree}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mounts {
		if m.Target == alias {
			if m.Source != canonicalPolicyTestPath(t, target) || !m.ReadOnly {
				t.Fatalf("unsafe alias: %+v", m)
			}
			return
		}
	}
	t.Fatal("ordinary HOME dotfile alias disappeared")
}

func TestLinuxControlAllowlistOmitsDeniedSymlinkAliases(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprintf("directory=%v", directory), func(t *testing.T) {
			home := canonicalPolicyTestPath(t, t.TempDir())
			tree := filepath.Join(home, ".wingthing", "eggs")
			target := filepath.Join(home, "dotfiles", "netrc")
			for _, dir := range []string{tree, filepath.Dir(target)} {
				makeEggConfigTestDir(t, dir)
			}
			if directory {
				makeEggConfigTestDir(t, target)
				writeEggConfigTestFile(t, filepath.Join(target, "token"), "credential canary")
			} else {
				writeEggConfigTestFile(t, target, "credential canary")
			}
			alias := filepath.Join(home, ".netrc")
			otherAlias := filepath.Join(home, "credential-alias")
			for _, path := range []string{alias, otherAlias} {
				linkTarget := target
				if directory && path == otherAlias {
					linkTarget = filepath.Join(target, "token")
				}
				if err := os.Symlink(linkTarget, path); err != nil {
					t.Fatal(err)
				}
			}
			masks := sandbox.CanonicalDenyPaths([]string{alias})
			for _, path := range []string{alias, target} {
				if !containsString(masks, path) {
					t.Fatalf("deny mask missing at %s: %v", path, masks)
				}
			}
			mounts, err := isolateLinuxEggControl([]sandbox.Mount{{Source: home}}, []string{tree}, nil, masks)
			if err != nil {
				t.Fatal(err)
			}
			for _, mount := range mounts {
				if mount.Target == alias || mount.Target == otherAlias {
					t.Fatalf("denied alias mounted readable: %+v", mount)
				}
			}
		})
	}
}

func TestAgentKeyHelperMountUsesOwnerReadOnlyFile(t *testing.T) {
	owner, other := t.TempDir(), t.TempDir()
	for _, home := range []string{owner, other} {
		if err := os.WriteFile(filepath.Join(home, ".anthropic_key"), []byte("fixture-key"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mount, err := agentKeyHelperMount("claude", owner)
	if err != nil || mount.Source != filepath.Join(owner, ".anthropic_key") || mount.Target != mount.Source || !mount.ReadOnly {
		t.Fatalf("owner helper mount: %+v %v", mount, err)
	}
	if mount, err := agentKeyHelperMount("codex", owner); err != nil || mount.Source != "" {
		t.Fatalf("unrelated agent received key: %+v %v", mount, err)
	}
	if err := os.Remove(filepath.Join(owner, ".anthropic_key")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, ".anthropic_key"), filepath.Join(owner, ".anthropic_key")); err != nil {
		t.Fatal(err)
	}
	if _, err := agentKeyHelperMount("claude", owner); err == nil {
		t.Fatal("another owner's helper alias accepted")
	}
}

func TestControlProtectionIncludesConfiguredToolDefinitions(t *testing.T) {
	home := canonicalPolicyTestPath(t, t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, ".wingthing"))
	custom := filepath.Join(home, "workspace", "privileged-tools")
	source := filepath.Join(home, "workspace", "host-command.yaml")
	for _, dir := range []string{filepath.Join(home, ".wingthing"), custom} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".wingthing", "wing.yaml"), []byte("tools_dir: "+custom+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("name: host\nrun: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, filepath.Join(custom, "host.yaml")); err != nil {
		t.Fatal(err)
	}
	control := eggControlDenyPaths("")
	for _, path := range []string{filepath.Join(home, ".wingthing", "tools"), filepath.Join(home, ".wingthing-preview", "tools"), custom, source} {
		found := false
		for _, protected := range control {
			found = found || protected == path
		}
		if !found {
			t.Fatalf("privileged definitions remain writable: %s", path)
		}
	}
	mounts, err := isolateLinuxEggControl([]sandbox.Mount{{Source: home}}, control, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mount := range mounts {
		if controlPathWithin(source, mount.Source) || controlPathWithin(custom, mount.Source) {
			t.Fatalf("tool definition exposed: %+v", mount)
		}
	}
}
