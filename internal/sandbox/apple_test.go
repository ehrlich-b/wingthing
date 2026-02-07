//go:build darwin

package sandbox

import (
	"testing"
	"time"
)

func TestValidContainerName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid", "wt-550e8400-e29b-41d4-a716-446655440000", true},
		{"missing prefix", "550e8400-e29b-41d4-a716-446655440000", false},
		{"wrong prefix", "xx-550e8400-e29b-41d4-a716-446655440000", false},
		{"bad uuid", "wt-not-a-uuid", false},
		{"empty", "", false},
		{"prefix only", "wt-", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validContainerName(tt.input); got != tt.want {
				t.Errorf("validContainerName(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildMountsStrict(t *testing.T) {
	cfg := Config{
		Isolation: Strict,
		Mounts: []Mount{
			{Source: "/host/data", Target: "/data", ReadOnly: false},
			{Source: "/host/config", Target: "/config", ReadOnly: true},
		},
	}
	mounts := buildMounts(cfg)
	if len(mounts) != 2 {
		t.Fatalf("got %d mounts, want 2", len(mounts))
	}
	// Strict forces all mounts read-only
	for i, m := range mounts {
		if m[len(m)-3:] != ":ro" {
			t.Errorf("mount[%d] = %q, want :ro suffix (strict forces read-only)", i, m)
		}
	}
}

func TestBuildMountsStandard(t *testing.T) {
	cfg := Config{
		Isolation: Standard,
		Mounts: []Mount{
			{Source: "/host/data", Target: "/data", ReadOnly: false},
			{Source: "/host/config", Target: "/config", ReadOnly: true},
		},
	}
	mounts := buildMounts(cfg)
	if len(mounts) != 2 {
		t.Fatalf("got %d mounts, want 2", len(mounts))
	}
	// Standard respects the ReadOnly flag
	if mounts[0] != "/host/data:/data" {
		t.Errorf("mount[0] = %q, want %q", mounts[0], "/host/data:/data")
	}
	if mounts[1] != "/host/config:/config:ro" {
		t.Errorf("mount[1] = %q, want %q", mounts[1], "/host/config:/config:ro")
	}
}

func TestBuildMountsNetwork(t *testing.T) {
	cfg := Config{
		Isolation: Network,
		Mounts: []Mount{
			{Source: "/host/data", Target: "/data", ReadOnly: false},
		},
	}
	mounts := buildMounts(cfg)
	if len(mounts) != 1 {
		t.Fatalf("got %d mounts, want 1", len(mounts))
	}
	if mounts[0] != "/host/data:/data" {
		t.Errorf("mount[0] = %q, want %q", mounts[0], "/host/data:/data")
	}
}

func TestBuildMountsPrivileged(t *testing.T) {
	cfg := Config{
		Isolation: Privileged,
		Mounts: []Mount{
			{Source: "/host/all", Target: "/all", ReadOnly: false},
		},
	}
	mounts := buildMounts(cfg)
	if len(mounts) != 1 {
		t.Fatalf("got %d mounts, want 1", len(mounts))
	}
	if mounts[0] != "/host/all:/all" {
		t.Errorf("mount[0] = %q, want %q", mounts[0], "/host/all:/all")
	}
}

func TestBuildMountsEmpty(t *testing.T) {
	cfg := Config{Isolation: Standard}
	mounts := buildMounts(cfg)
	if len(mounts) != 0 {
		t.Errorf("got %d mounts, want 0", len(mounts))
	}
}

func TestHasNetwork(t *testing.T) {
	tests := []struct {
		level Level
		want  bool
	}{
		{Strict, false},
		{Standard, false},
		{Network, true},
		{Privileged, true},
	}
	for _, tt := range tests {
		if got := hasNetwork(tt.level); got != tt.want {
			t.Errorf("hasNetwork(%s) = %v, want %v", tt.level, got, tt.want)
		}
	}
}

func TestBuildExecArgs(t *testing.T) {
	tests := []struct {
		name      string
		container string
		cmd       string
		args      []string
		want      []string
	}{
		{
			name:      "simple",
			container: "wt-abc",
			cmd:       "echo",
			args:      []string{"hello"},
			want:      []string{"exec", "wt-abc", "--", "echo", "hello"},
		},
		{
			name:      "no args",
			container: "wt-abc",
			cmd:       "ls",
			args:      nil,
			want:      []string{"exec", "wt-abc", "--", "ls"},
		},
		{
			name:      "multiple args",
			container: "wt-abc",
			cmd:       "bash",
			args:      []string{"-c", "echo hello && echo world"},
			want:      []string{"exec", "wt-abc", "--", "bash", "-c", "echo hello && echo world"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildExecArgs(tt.container, tt.cmd, tt.args)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d: %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("arg[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestAppleSandboxExecBuildsCommand(t *testing.T) {
	// Unit test the Exec method without a real container runtime.
	// We construct the struct directly (skipping newPlatform which requires the CLI).
	sb := &appleSandbox{
		cfg:  Config{Isolation: Standard, Timeout: 30 * time.Second},
		name: "wt-test-fake",
	}

	ctx := t.Context()
	cmd, err := sb.Exec(ctx, "echo", []string{"hello"})
	if err != nil {
		t.Fatalf("Exec error: %v", err)
	}

	// cmd.Path should be the container binary (resolved path)
	// cmd.Args should be [container exec wt-test-fake -- echo hello]
	args := cmd.Args
	if len(args) < 6 {
		t.Fatalf("expected at least 6 args, got %d: %v", len(args), args)
	}
	// args[0] is the resolved path to "container", check the rest
	if args[1] != "exec" {
		t.Errorf("args[1] = %q, want %q", args[1], "exec")
	}
	if args[2] != "wt-test-fake" {
		t.Errorf("args[2] = %q, want %q", args[2], "wt-test-fake")
	}
	if args[3] != "--" {
		t.Errorf("args[3] = %q, want %q", args[3], "--")
	}
	if args[4] != "echo" {
		t.Errorf("args[4] = %q, want %q", args[4], "echo")
	}
	if args[5] != "hello" {
		t.Errorf("args[5] = %q, want %q", args[5], "hello")
	}
}
