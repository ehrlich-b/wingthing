//go:build darwin || linux

package protectedfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPolicyRefusesWritableAliasAndTargetDirectoryChains(t *testing.T) {
	for _, scenario := range []string{"alias-parent", "alias-ancestor", "target-parent", "target-ancestor", "direct-parent"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			aliasDir := filepath.Join(root, "aliases", "private")
			targetDir := filepath.Join(root, "targets", "private")
			for _, dir := range []string{aliasDir, targetDir} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			policy := filepath.Join(targetDir, "egg.yaml")
			if err := os.WriteFile(policy, []byte("sandbox: trusted-host\n"), 0644); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(aliasDir, "egg.yaml")
			if err := os.Symlink(policy, alias); err != nil {
				t.Fatal(err)
			}
			unsafe := aliasDir
			switch scenario {
			case "alias-ancestor":
				unsafe = filepath.Dir(aliasDir)
			case "target-parent", "direct-parent":
				unsafe = targetDir
			case "target-ancestor":
				unsafe = filepath.Dir(targetDir)
			}
			if err := os.Chmod(unsafe, 0777); err != nil {
				t.Fatal(err)
			}
			if scenario == "direct-parent" {
				alias = policy
			}
			_, err := ReadPolicyResolved(alias)
			var refusal *Error
			if !errors.As(err, &refusal) {
				t.Fatalf("accepted replaceable trusted policy through %s: %v", scenario, err)
			}
		})
	}
}

func TestPolicyPinsPersonalDotfileSymlinkTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dotfiles := filepath.Join(home, "dotfiles")
	if err := os.Mkdir(dotfiles, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dotfiles, "wing.yaml")
	if err := os.WriteFile(target, []byte("original"), 0400); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "wing.yaml")
	if err := os.Symlink("dotfiles/wing.yaml", alias); err != nil {
		t.Fatal(err)
	}
	f, err := OpenPolicyResolved(alias)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.Rename(dotfiles, dotfiles+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dotfiles, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("replacement"), 0400); err != nil {
		t.Fatal(err)
	}
	data, err := f.ReadAll()
	if err != nil || string(data) != "original" {
		t.Fatalf("policy descriptor followed replaced directory: %q, %v", data, err)
	}
}
