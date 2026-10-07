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

func TestPolicyStickyDirectoryRequiresTrustedEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, os.ModeSticky|0777); err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(root, "owned")
	if err := os.Mkdir(owned, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(owned, "config.yaml")
	if err := os.WriteFile(path, []byte("trusted"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadPolicyResolved(path); err != nil || string(data) != "trusted" {
		t.Fatalf("refused owned entry under a sticky parent: %q, %v", data, err)
	}
	for _, entry := range []string{"directory", "symlink", "file"} {
		t.Run(entry, func(t *testing.T) {
			foreign := filepath.Join(root, entry)
			var policy string
			switch entry {
			case "directory":
				if err := os.Mkdir(foreign, 0700); err != nil {
					t.Fatal(err)
				}
				policy = filepath.Join(foreign, "config.yaml")
				if err := os.WriteFile(policy, []byte("untrusted"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(path, foreign); err != nil {
					t.Fatal(err)
				}
				policy = foreign
			case "file":
				if err := os.WriteFile(foreign, []byte("untrusted"), 0600); err != nil {
					t.Fatal(err)
				}
				policy = foreign
			}
			if err := os.Lchown(foreign, os.Getuid()+1, -1); err != nil {
				t.Skipf("cannot change owner: %v", err)
			}
			_, err := ReadPolicyResolved(policy)
			var refusal *Error
			if !errors.As(err, &refusal) {
				t.Fatalf("accepted another user's entry in sticky directory: %v", err)
			}
		})
	}
}
