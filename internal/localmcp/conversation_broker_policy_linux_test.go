//go:build linux

package localmcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestLinuxProviderWriteModel(t *testing.T) {
	if err := sandbox.CheckProviderWriteProtection(); err != nil {
		t.Skip(err)
	}
	root := testsocket.Dir(t)
	home, work, tmp := filepath.Join(root, "home"), filepath.Join(root, "work"), filepath.Join(root, "tmp")
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", tmp)
	cfg := &config.Config{Dir: "/opt/controller-state"}
	model, err := modelProviderWrites(cfg, egg.DefaultEggConfig(), "claude", work, "session", eggclient.EggIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if !model.ExplicitProtection {
		t.Fatal("Linux lacks explicit protection contract")
	}
	if err := model.verifyProtected(cfg.Dir, []string{"/opt/controller"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{work, filepath.Join(home, ".claude-state"), tmp, "/tmp"} {
		if err := model.verifyProtected(path, nil); err == nil {
			t.Fatalf("exposed state admitted: %s", path)
		}
	}
	if _, err := modelProviderWrites(cfg, egg.UnsandboxedEggConfig(), "claude", work, "session", eggclient.EggIdentity{}); err == nil {
		t.Fatal("outer boundary admitted")
	}
	if os.Getuid() == 0 {
		t.Log("native ancestor pins, rather than POSIX ownership, enforce Linux protection")
	}
}

func TestLinuxProviderWriteMailboxPinsPreserveHomeDescendants(t *testing.T) {
	home := testsocket.Dir(t)
	t.Setenv("HOME", home)
	cfg := &config.Config{Dir: filepath.Join(home, "state")}
	if err := os.Mkdir(cfg.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	policy := egg.DefaultEggConfig()
	policy.FS = append(policy.FS, "deny-write:"+filepath.Join(home, "policy-file"))
	if err := sealMailboxPolicy(cfg, policy); err != nil {
		t.Fatal(err)
	}
	compiled := policy.ToSandboxConfig(home)
	found := false
	for _, pin := range compiled.DenyRename {
		if pin == home {
			found = true
		}
	}
	if !found {
		t.Fatal("Linux HOME ancestry lacks rename pin")
	}
	for _, deny := range compiled.DenyWrite {
		if deny == home || deny == "/" {
			t.Fatal("generated pin seals provider HOME descendants")
		}
	}
	if len(compiled.DenyWrite) < 2 {
		t.Fatal("existing file write denial removed")
	}
}
