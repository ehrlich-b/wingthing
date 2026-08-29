package relay

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPublishedInstallerMatchesRepositoryCopyAndReleaseContract(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryCopy := filepath.Join(filepath.Dir(filename), "..", "..", "scripts", "install.sh")
	data, err := os.ReadFile(repositoryCopy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(installScript, data) {
		t.Fatal("embedded /install.sh and scripts/install.sh differ")
	}

	script := string(installScript)
	for _, required := range []string{
		"SHA256SUMS",
		`mcp connect --help`,
		`serve --help`,
		`roost start --help`,
		`local-cert status --help`,
		`no binary was installed`,
		`--proto '=https' --proto-redir '=https'`,
		`wt mcp stdio --client codex`,
		`wt mcp connect --client <parent-agent>`,
		`wt roost start --https`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("installer does not enforce %q", required)
		}
	}
	if strings.Index(script, `ACTUAL=$(sha256sum`) > strings.Index(script, `mcp connect --help`) {
		t.Fatal("installer executes a downloaded binary before verifying its checksum")
	}
}

func TestPublishedInstallerVerifiesBeforeAtomicReplacement(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("published installer supports Linux and macOS")
	}
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		t.Skip("published installer supports amd64 and arm64")
	}
	_, filename, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(filename), "..", "..", "scripts", "install.sh")

	fixture := []byte(`#!/bin/sh
if [ -n "$EXEC_MARKER" ]; then : > "$EXEC_MARKER"; fi
case "$*" in
  "--version") echo "wt version v-test" ;;
  "mcp connect --help") echo "connect directly" ;;
  "serve --help") echo "--https" ;;
  "roost start --help") echo "--https" ;;
  "local-cert status --help") echo "status" ;;
  *) exit 2 ;;
esac
`)
	broken := []byte("#!/bin/sh\nif [ -n \"$EXEC_MARKER\" ]; then : > \"$EXEC_MARKER\"; fi\necho old release\n")

	run := func(t *testing.T, binary []byte, checksum string) (string, []byte, bool, error) {
		t.Helper()
		root := t.TempDir()
		installDir := filepath.Join(root, "install")
		if err := os.MkdirAll(installDir, 0o755); err != nil {
			t.Fatal(err)
		}
		existing := filepath.Join(installDir, "wt")
		if err := os.WriteFile(existing, []byte("existing-install"), 0o755); err != nil {
			t.Fatal(err)
		}
		asset := filepath.Join(root, "asset")
		if err := os.WriteFile(asset, binary, 0o755); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("wt-%s-%s", runtime.GOOS, arch)
		if checksum == "" {
			digest := sha256.Sum256(binary)
			checksum = fmt.Sprintf("%x", digest[:])
		}
		sums := filepath.Join(root, "SHA256SUMS")
		if err := os.WriteFile(sums, []byte(checksum+"  "+name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fakeBin := filepath.Join(root, "bin")
		if err := os.Mkdir(fakeBin, 0o755); err != nil {
			t.Fatal(err)
		}
		fakeCurl := []byte(`#!/bin/sh
out=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    *) url="$1"; shift ;;
  esac
done
case "$url" in
  */releases/latest) printf '%s\n' '{"tag_name":"v-test"}' ;;
  */SHA256SUMS) cp "$FAKE_SUMS" "$out" ;;
  *) cp "$FAKE_BINARY" "$out" ;;
esac
`)
		if err := os.WriteFile(filepath.Join(fakeBin, "curl"), fakeCurl, 0o755); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(root, "executed")
		cmd := exec.Command("sh", script)
		cmd.Env = append(os.Environ(),
			"HOME="+root,
			"WT_INSTALL_DIR="+installDir,
			"FAKE_BINARY="+asset,
			"FAKE_SUMS="+sums,
			"EXEC_MARKER="+marker,
			"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		)
		output, err := cmd.CombinedOutput()
		installed, readErr := os.ReadFile(existing)
		if readErr != nil {
			t.Fatal(readErr)
		}
		_, markerErr := os.Stat(marker)
		return string(output), installed, markerErr == nil, err
	}

	t.Run("success", func(t *testing.T) {
		output, installed, executed, err := run(t, fixture, "")
		if err != nil || !bytes.Equal(installed, fixture) || !executed {
			t.Fatalf("installer err=%v executed=%v output=%s installed=%q", err, executed, output, installed)
		}
	})
	t.Run("contract rejection keeps existing install", func(t *testing.T) {
		output, installed, executed, err := run(t, broken, "")
		if err == nil || string(installed) != "existing-install" || !executed || !strings.Contains(output, "no binary was installed") {
			t.Fatalf("installer err=%v executed=%v output=%s installed=%q", err, executed, output, installed)
		}
	})
	t.Run("checksum rejection happens before execution", func(t *testing.T) {
		output, installed, executed, err := run(t, fixture, strings.Repeat("0", 64))
		if err == nil || string(installed) != "existing-install" || executed || !strings.Contains(output, "checksum mismatch") {
			t.Fatalf("installer err=%v executed=%v output=%s installed=%q", err, executed, output, installed)
		}
	})
}
