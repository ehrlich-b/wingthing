package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRemotesValidation(t *testing.T) {
	for _, name := range []string{"work", "Work_2", "dev-host", "123", "-"} {
		if err := ValidateRemoteName(name); err != nil {
			t.Errorf("valid name %q: %v", name, err)
		}
	}
	for _, name := range []string{"", "two words", "a.b", "a:b", "a/b", "é", "x\n"} {
		if err := ValidateRemoteName(name); err == nil {
			t.Errorf("accepted invalid name %q", name)
		}
	}
	for _, target := range []string{"work1", "me@example.com", "me@2001:db8::1"} {
		if err := ValidateSSHTarget(target); err != nil {
			t.Errorf("valid SSH target %q: %v", target, err)
		}
	}
	for _, target := range []string{"", "-oProxyCommand=bad", "host arg", "host\targ", "host\n", "host\r", "host\x00", "host\x1b", "host\x7f", "host\u0085", "host\u00a0"} {
		if err := ValidateSSHTarget(target); err == nil {
			t.Errorf("accepted invalid SSH target %q", target)
		}
	}
}

func TestRemotesRejectReservedLocalName(t *testing.T) {
	for _, name := range []string{"local", "LOCAL", "Local", "lOcAl"} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRemoteName(name); err == nil {
				t.Fatal("accepted reserved local machine name")
			}
			dir := t.TempDir()
			if err := SaveRemotes(dir, map[string]Remote{name: {SSHTarget: "host"}}); err == nil {
				t.Fatal("saved reserved remote name")
			}
			data := fmt.Sprintf("remotes:\n  %s:\n    ssh_target: host\n", name)
			if err := os.WriteFile(filepath.Join(dir, "remotes.yaml"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadRemotes(dir); err == nil {
				t.Fatal("loaded reserved remote name")
			}
		})
	}
}

func TestRemotesRoundTripAtomicOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	missing, err := LoadRemotes(dir)
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing registry: %v, %v", missing, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("loading missing registry wrote files")
	}
	want := map[string]Remote{
		"work": {SSHTarget: "me@host", WingthingDir: "/home/me/wt state/it's isolated", WTBinary: "/home/me/wt builds/wt-dev"},
		"lab":  {SSHTarget: "lab-alias"},
	}
	path := filepath.Join(dir, "remotes.yaml")
	if err := os.WriteFile(path, []byte("remotes: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	if err := SaveRemotes(dir, want); err != nil {
		t.Fatal(err)
	}
	oldInfo, _ := previous.Stat()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 || os.SameFile(oldInfo, info) {
		t.Fatalf("registry was not replaced atomically with mode 0600: %v, %v", info, err)
	}
	got, err := LoadRemotes(dir)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip: %#v, %v", got, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temporary files remain: %v", entries)
	}
	if err := SaveRemotes(dir, map[string]Remote{"bad": {SSHTarget: "-bad"}}); err == nil {
		t.Fatal("invalid save succeeded")
	}
	got, err = LoadRemotes(dir)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("invalid save changed the previous registry")
	}
}

func TestWTBinaryValidation(t *testing.T) {
	for _, binary := range []string{"wt", "wt-dev", "wt-preview", "wt_2.0+dev", "/home/me/bin/wt-dev", "/opt/wt builds/wt"} {
		if err := ValidateWTBinary(binary); err != nil {
			t.Errorf("valid binary %q: %v", binary, err)
		}
	}
	invalid := []string{"", ".", "..", "/", "/bin/", "~/bin/wt", "./wt", "bin/wt", "wt --client", "-wt", "/bin/wt\n", "/bin/wt\r", "/bin/wt\x00", "/bin/wt\t", "/bin/wt\x1b", "/bin/wt\u00a0"}
	for _, meta := range "'\"`$;&|<>(){}[]*?\\~!#" {
		invalid = append(invalid, "/bin/wt"+string(meta))
	}
	for _, binary := range invalid {
		if err := ValidateWTBinary(binary); err == nil {
			t.Errorf("accepted invalid binary %q", binary)
		}
		if binary == "" { // Omitted optional fields remain valid.
			continue
		}
		dir := t.TempDir()
		if err := SaveRemotes(dir, map[string]Remote{"work": {SSHTarget: "host", WTBinary: binary}}); err == nil {
			t.Errorf("saved invalid binary %q", binary)
		}
		data, err := yaml.Marshal(remotesFile{Remotes: map[string]Remote{"work": {SSHTarget: "host", WTBinary: binary}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "remotes.yaml"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRemotes(dir); err == nil {
			t.Errorf("loaded invalid binary %q", binary)
		}
	}
}

func TestRemotesLoadWithoutWTBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "remotes.yaml")
	data := "remotes:\n  legacy:\n    ssh_target: old-host\n  verified:\n    ssh_target: host\n    wingthing_dir: /state\n    wing_id: remote-wing\n    control_socket: /state/control.sock\n    control_version: v1\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	remotes, err := LoadRemotes(dir)
	if err != nil || len(remotes) != 2 {
		t.Fatalf("old registry: %v %v", remotes, err)
	}
	for name, remote := range remotes {
		if remote.WTBinary != "" || remote.Binary() != BinaryName() {
			t.Errorf("old entry %s lost PATH default: %+v", name, remote)
		}
	}
	if err := SaveRemotes(dir, remotes); err != nil {
		t.Fatal(err)
	}
	wire, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(wire), "wt_binary") {
		t.Fatalf("optional field not omitted: %s %v", wire, err)
	}
}

func TestRemotesRejectInvalidYAML(t *testing.T) {
	for _, data := range []string{
		"remotes:\n  bad.name:\n    ssh_target: host\n",
		"remotes:\n  work:\n    ssh_target: '-option'\n",
		"remotes:\n  work:\n    ssh_target: 'host arg'\n",
		"remotes:\n  work:\n    ssh_target: host\n    wingthing_dir: relative\n",
		"remotes:\n  work:\n    ssh_target: host\n    typo: value\n",
		"remotes:\n  work:\n    ssh_target: host\n  work:\n    ssh_target: other\n",
		"remotes: {}\n---\nremotes: {}\n",
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "remotes.yaml"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRemotes(dir); err == nil {
			t.Errorf("accepted invalid registry %q", data)
		}
	}
}

func TestRemotesConcurrentUpdates(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := UpdateRemotes(dir, func(remotes map[string]Remote) error {
				remotes[fmt.Sprintf("work%d", i)] = Remote{SSHTarget: "host"}
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, err := LoadRemotes(dir)
	if err != nil || len(got) != 8 {
		t.Fatalf("lost concurrent updates: %v, %v", got, err)
	}
}
