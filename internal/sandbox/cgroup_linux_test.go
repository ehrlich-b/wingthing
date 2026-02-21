//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCgroupV2Path(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "simple v2",
			input: "0::/user.slice/user-1000.slice/session-1.scope\n",
			want:  "/user.slice/user-1000.slice/session-1.scope",
		},
		{
			name:  "root cgroup",
			input: "0::/\n",
			want:  "/",
		},
		{
			name:    "v1 only",
			input:   "12:cpuset:/\n11:memory:/user.slice\n",
			wantErr: true,
		},
		{
			name:    "empty",
			input:   "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCgroupV2Path(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got path=%q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseCgroupV2PathHybrid(t *testing.T) {
	// Hybrid system with both v1 controllers and v2 unified hierarchy
	input := "12:cpuset:/\n11:memory:/user.slice\n0::/user.slice/user-1000.slice\n"
	got, err := parseCgroupV2Path(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/user.slice/user-1000.slice" {
		t.Errorf("got %q, want /user.slice/user-1000.slice", got)
	}
}

func TestNewCgroupManagerNoCgroupV2(t *testing.T) {
	// If cgroups v2 is not available, newCgroupManager should return (nil, nil)
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		t.Skip("cgroups v2 is available, skipping no-cgroup test")
	}
	cg, err := newCgroupManager("test-session", 1024*1024*1024, 256)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if cg != nil {
		t.Fatal("expected nil cgroupManager when cgroups v2 unavailable")
	}
}

func TestNewCgroupManagerIntegration(t *testing.T) {
	// Skip if cgroups v2 not available
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Skip("cgroups v2 not available")
	}

	// Try to create a cgroup — may fail without delegation
	memLimit := uint64(512 * 1024 * 1024) // 512MB
	pidLimit := uint32(128)
	cg, err := newCgroupManager("test-integration", memLimit, pidLimit)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cg == nil {
		t.Skip("cgroup creation failed (no delegation?), skipping integration test")
	}
	defer cg.Destroy()

	// Verify memory.max
	data, err := os.ReadFile(filepath.Join(cg.path, "memory.max"))
	if err != nil {
		t.Fatalf("read memory.max: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "536870912" {
		t.Errorf("memory.max = %q, want 536870912", got)
	}

	// Verify pids.max
	data, err = os.ReadFile(filepath.Join(cg.path, "pids.max"))
	if err != nil {
		t.Fatalf("read pids.max: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "128" {
		t.Errorf("pids.max = %q, want 128", got)
	}

	// Verify AddPID with own process (just check it doesn't error)
	if err := cg.AddPID(os.Getpid()); err != nil {
		t.Logf("AddPID failed (expected in some environments): %v", err)
	}
}

func TestNewCgroupManagerZeroLimits(t *testing.T) {
	cg, err := newCgroupManager("test-zero", 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cg != nil {
		t.Fatal("expected nil cgroupManager when both limits are zero")
	}
}

func TestCgroupManagerNilSafety(t *testing.T) {
	// Nil cgroupManager should be safe to call
	var cg *cgroupManager
	if err := cg.AddPID(123); err != nil {
		t.Errorf("nil AddPID should return nil, got: %v", err)
	}
	if err := cg.Destroy(); err != nil {
		t.Errorf("nil Destroy should return nil, got: %v", err)
	}
}
