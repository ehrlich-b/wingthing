package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func dogfoodScriptFixture(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "scripts", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(filepath.Dir(executable), "fixtures", "scripts", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("dogfood test assets were not packaged: %v", err)
	}
	return path
}

func TestDogfoodEntryRecoversItsPrivateForward(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".local", "share", "wingthing-dogfood")
	bin := filepath.Join(home, "fixtures")
	for _, dir := range []string{bin, filepath.Join(root, "bin"), filepath.Join(root, "ssh"), filepath.Join(root, "client")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0755); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "ssh", "id_ed25519"), "fixture-not-a-key")
	write(filepath.Join(root, "ssh", "known_hosts"), "fixture-not-a-host-pin")
	write(filepath.Join(bin, "ssh"), `#!/bin/sh
printf '%s\n' "$*" >> "$WT_TEST_DOGFOOD_LOG"
case " $* " in
  *" -O check "*) test -f "$WT_TEST_DOGFOOD_STATE" ;;
  *) test "${WT_TEST_DOGFOOD_SSH_FAIL:-0}" != 1 || exit 1
     touch "$WT_TEST_DOGFOOD_STATE"
     rm -f "$WT_TEST_DOGFOOD_HEALTH_FAIL" ;;
esac
`)
	write(filepath.Join(bin, "curl"), "#!/bin/sh\ntest ! -f \"$WT_TEST_DOGFOOD_HEALTH_FAIL\"\n")
	write(filepath.Join(root, "bin", "wt"), "#!/bin/sh\nprintf '%s\\n' \"$WINGTHING_DIR\" \"$@\"\n")
	logPath := filepath.Join(home, "ssh.log")
	state := filepath.Join(home, "connection")
	health := filepath.Join(home, "health-fail")
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WINGTHING_DIR", "wrong-ambient-profile")
	t.Setenv("WT_TEST_DOGFOOD_LOG", logPath)
	t.Setenv("WT_TEST_DOGFOOD_STATE", state)
	t.Setenv("WT_TEST_DOGFOOD_HEALTH_FAIL", health)
	run := func(args ...string) (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "/bin/bash", append([]string{dogfoodScriptFixture(t, "wt-dogfood")}, args...)...).CombinedOutput()
		return string(out), err
	}
	for i := 0; i < 2; i++ {
		out, err := run("review", "list", "--json")
		if err != nil || !strings.HasPrefix(out, filepath.Join(root, "client")+"\n") || !strings.Contains(out, "--wing-id\n5003a9a0a5b64b2f8521db78\n--roost\nhttp://127.0.0.1:17881\n") {
			t.Fatalf("private dispatch: %v %s", err, out)
		}
	}
	log, err := os.ReadFile(logPath)
	if err != nil || strings.Count(string(log), "-fNT") != 1 || !strings.Contains(string(log), "StrictHostKeyChecking=yes") || !strings.Contains(string(log), "ConnectTimeout=8") {
		t.Fatalf("connection not bounded/idempotent/pinned: %v %s", err, log)
	}
	write(health, "unreachable")
	if out, err := run("review", "list", "--json"); err != nil {
		t.Fatalf("restore existing master's forward: %v %s", err, out)
	}
	log, _ = os.ReadFile(logPath)
	if !strings.Contains(string(log), "-O forward") || strings.Count(string(log), "-fNT") != 1 {
		t.Fatalf("existing master was not reused: %s", log)
	}
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	if out, err := run("mcp", "connect", "--client", "fresh-client"); err != nil || !strings.Contains(out, "--roost\nhttp://127.0.0.1:17881") || strings.Contains(out, "--wing-id") {
		t.Fatalf("fresh MCP dispatch: %v %s", err, out)
	}
	for _, args := range [][]string{{"review", "list", "--roost=https://production.example"}, {"review", "list", "--wing-id", "another-wing"}, {"update"}} {
		if out, err := run(args...); err == nil {
			t.Fatalf("unsafe dispatch accepted %v: %s", args, out)
		}
	}
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_TEST_DOGFOOD_SSH_FAIL", "1")
	if out, err := run("review", "list", "--json"); err == nil || !strings.Contains(out, "check office/VPN reachability") || strings.Contains(out, "wrong-ambient-profile") {
		t.Fatalf("connection failure: %v %s", err, out)
	}
}

func TestDogfoodInstallerPreservesDefaultClientAndImmutableBuild(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0700); err != nil {
		t.Fatal(err)
	}
	defaultPath := filepath.Join(localBin, "wt")
	if err := os.WriteFile(defaultPath, []byte("default-client-unchanged"), 0755); err != nil {
		t.Fatal(err)
	}
	build := filepath.Join(home, "private-build")
	version := "dogfood-aaaaaaaaaaaa-bbbbbbbbbbbb"
	if err := os.WriteFile(build, []byte("#!/bin/sh\necho 'wt version "+version+"'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		out, err := exec.Command("/bin/bash", dogfoodScriptFixture(t, "install-dogfood.sh"), build).CombinedOutput()
		if err != nil {
			t.Fatalf("install: %v %s", err, out)
		}
	}
	root := filepath.Join(home, ".local", "share", "wingthing-dogfood")
	target, err := os.Readlink(filepath.Join(root, "bin", "wt"))
	if err != nil || target != filepath.Join(root, "builds", version, "wt") {
		t.Fatalf("private build pointer: %s %v", target, err)
	}
	for _, path := range []string{filepath.Join(localBin, "wt-dogfood"), filepath.Join(root, "README.md")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	unchanged, err := os.ReadFile(defaultPath)
	if err != nil || string(unchanged) != "default-client-unchanged" {
		t.Fatal("default wt replaced")
	}
	if _, err := os.Stat(filepath.Join(root, "client")); !os.IsNotExist(err) {
		t.Fatal("installer created or copied a credential profile")
	}
	if err := os.WriteFile(build, []byte("#!/bin/sh\necho 'wt version "+version+"'\n# different bytes\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/bin/bash", dogfoodScriptFixture(t, "install-dogfood.sh"), build).CombinedOutput(); err == nil || !strings.Contains(string(out), "different bytes") {
		t.Fatalf("immutable build overwritten: %v %s", err, out)
	}
}
