package egg

import (
	"os"
	"path/filepath"
	"testing"
)

// Ordinary sessions keep the browser bridge exactly as before; the broker-only
// omission is a skip of this whole installation, never a partial one.
func TestInstallBrowserBridgeKeepsOrdinaryLayout(t *testing.T) {
	dir := t.TempDir()
	requests, shims := filepath.Join(dir, "browser-requests"), filepath.Join(dir, "shims")
	if err := os.WriteFile(requests, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"PATH": "/usr/local/bin:/usr/bin"}
	if err := installBrowserBridge(env, dir, requests, shims); err != nil {
		t.Fatal(err)
	}
	if env["BROWSER"] != "wt-browser" || env["WT_SESSION_DIR"] != dir || env["PATH"] != shims+":/usr/local/bin:/usr/bin" {
		t.Fatalf("bridge env %v", env)
	}
	if data, err := os.ReadFile(requests); err != nil || len(data) != 0 {
		t.Fatalf("request file not truncated: %q %v", data, err)
	}
	script, err := os.ReadFile(filepath.Join(shims, "wt-browser"))
	if want := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$WT_SESSION_DIR/browser-requests\"\n"; err != nil || string(script) != want {
		t.Fatalf("shim script %q %v", script, err)
	}
	for _, alias := range []string{"open", "xdg-open"} {
		if _, err := os.Lstat(filepath.Join(shims, alias)); err != nil {
			t.Fatalf("%s alias: %v", alias, err)
		}
	}

	// Alias installation is not idempotent, as before; use a fresh session.
	fresh := t.TempDir()
	bareShims := filepath.Join(fresh, "shims")
	bare := map[string]string{}
	if err := installBrowserBridge(bare, fresh, filepath.Join(fresh, "browser-requests"), bareShims); err != nil {
		t.Fatal(err)
	}
	if bare["PATH"] != bareShims+":/usr/bin:/bin" {
		t.Fatalf("default bridge PATH %q", bare["PATH"])
	}
}
