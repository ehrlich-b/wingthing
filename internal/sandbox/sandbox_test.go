package sandbox

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseLevelRoundTrip(t *testing.T) {
	tests := []struct {
		level Level
		str   string
	}{
		{Strict, "strict"},
		{Standard, "standard"},
		{Network, "network"},
		{Privileged, "privileged"},
	}
	for _, tt := range tests {
		if got := tt.level.String(); got != tt.str {
			t.Errorf("Level(%d).String() = %q, want %q", tt.level, got, tt.str)
		}
		if got := ParseLevel(tt.str); got != tt.level {
			t.Errorf("ParseLevel(%q) = %d, want %d", tt.str, got, tt.level)
		}
	}
}

func TestParseLevelUnknown(t *testing.T) {
	if got := ParseLevel("bogus"); got != Standard {
		t.Errorf("ParseLevel(bogus) = %d, want Standard(%d)", got, Standard)
	}
}

func TestNewReturnsSandbox(t *testing.T) {
	sb, err := New(Config{Isolation: Standard})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer sb.Destroy()

	if sb == nil {
		t.Fatal("New() returned nil sandbox")
	}
}

func TestFallbackExecEcho(t *testing.T) {
	sb, err := newFallback(Config{Isolation: Standard, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("newFallback error: %v", err)
	}
	defer sb.Destroy()

	ctx := context.Background()
	cmd, err := sb.Exec(ctx, "echo", []string{"hello"})
	if err != nil {
		t.Fatalf("Exec error: %v", err)
	}

	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if got := out.String(); got != "hello\n" {
		t.Errorf("output = %q, want %q", got, "hello\n")
	}
}

func TestFallbackRestrictedEnv(t *testing.T) {
	sb, err := newFallback(Config{Isolation: Standard})
	if err != nil {
		t.Fatalf("newFallback error: %v", err)
	}
	defer sb.Destroy()

	fb := sb.(*fallbackSandbox)
	env := fb.buildEnv()

	// Fallback sandbox passes through full env, overrides TMPDIR only
	var hasTmpdir bool
	for _, e := range env {
		if len(e) > 7 && e[:7] == "TMPDIR=" {
			hasTmpdir = true
			want := "TMPDIR=" + fb.tmpDir
			if e != want {
				t.Errorf("TMPDIR = %q, want %q", e, want)
			}
		}
	}
	if !hasTmpdir {
		t.Errorf("missing TMPDIR in env")
	}
}

func TestFallbackWorkingDir(t *testing.T) {
	sb, err := newFallback(Config{Isolation: Standard})
	if err != nil {
		t.Fatalf("newFallback error: %v", err)
	}
	defer sb.Destroy()

	ctx := context.Background()
	cmd, err := sb.Exec(ctx, "pwd", nil)
	if err != nil {
		t.Fatalf("Exec error: %v", err)
	}

	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	fb := sb.(*fallbackSandbox)
	// Resolve symlinks for comparison (macOS /var -> /private/var)
	wantDir, _ := filepath.EvalSymlinks(fb.tmpDir)
	got := string(bytes.TrimSpace(out.Bytes()))
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantDir {
		t.Errorf("pwd = %q, want %q", got, wantDir)
	}
}

func TestFallbackDestroy(t *testing.T) {
	sb, err := newFallback(Config{Isolation: Standard})
	if err != nil {
		t.Fatalf("newFallback error: %v", err)
	}
	fb := sb.(*fallbackSandbox)
	dir := fb.tmpDir

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("tmpdir should exist: %v", err)
	}

	if err := sb.Destroy(); err != nil {
		t.Fatalf("Destroy error: %v", err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("tmpdir should be removed after Destroy, got err: %v", err)
	}
}
