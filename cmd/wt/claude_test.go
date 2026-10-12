package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/google/uuid"
)

func TestClaudeWithoutWingFailsWithGuidance(t *testing.T) {
	scratch, err := filepath.Abs("../../.scratch")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(scratch, "claude-none-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("HOME", root)
	t.Setenv("WINGTHING_DIR", filepath.Join(root, "absent"))
	t.Setenv("WT_MCP_CLIENT", "named-client")
	cmd := newRootCommand()
	cmd.SetArgs([]string{"claude", "--name", "weekend", "--", "--model", "fake"})
	cmd.SetIn(bytes.NewReader(nil))
	cmd.SetOut(new(bytes.Buffer))
	err = cmd.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "wt wing start --local-only") {
		t.Fatalf("missing guidance: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "absent")); !os.IsNotExist(err) {
		t.Fatalf("CLI created state without a wing: %v", err)
	}
}

func TestClaudeCWDAndProviderArgsUseOneScopedAdmission(t *testing.T) {
	f := claudeControlFixture(t)
	t.Chdir(f.work)
	providerArgs := []string{"--model", "fixture-model", "--remote", "provider-host", "--remote-state", "/provider state", "--remote-cwd", "relative provider cwd", "--cwd", "provider cwd", "--name", "provider name", "--json", "--remote-batch"}
	requests := make(chan scopedNewLaunch, 4)
	var calls atomic.Int32
	const refusal = "fixture refuses admission without replacing caller policy"
	server, err := controlsocket.Listen(t.Context(), f.local, "fixture-wing", func(hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		if hello.Client != "scoped-client" {
			t.Errorf("selected client changed: %q", hello.Client)
		}
		return controlsocket.Welcome{}, func(_ context.Context, request control.DirectRequest) control.DirectResponse {
			calls.Add(1)
			var launch scopedNewLaunch
			if err := json.Unmarshal(request.Arguments, &launch); err != nil || request.Tool != "agent_start" {
				t.Errorf("launch request: %s %v", request.Arguments, err)
			}
			requests <- launch
			return control.DirectResponse{Version: control.ContractVersion, ID: request.ID, IsError: true, Result: map[string]any{"error": refusal}}
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	t.Setenv("WT_MCP_CLIENT", "scoped-client")
	ids := map[string]bool{}
	for _, test := range []struct {
		name, cwd, want string
	}{
		{"omitted", "", f.work},
		{"relative with spaces", "project space", filepath.Join(f.work, "project space")},
		{"absolute with spaces", filepath.Join(f.root, "absolute space"), filepath.Join(f.root, "absolute space")},
		{"normalized relative", "project space/../chosen", filepath.Join(f.work, "chosen")},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"claude", "--name", "week end"}
			if test.cwd != "" {
				args = append(args, "--cwd", test.cwd)
			}
			args = append(append(args, "--"), providerArgs...)
			cmd := newRootCommand()
			cmd.SetArgs(args)
			cmd.SetIn(bytes.NewReader(nil))
			cmd.SetOut(new(bytes.Buffer))
			cmd.SetErr(new(bytes.Buffer))
			if err := cmd.ExecuteContext(t.Context()); err == nil || errors.Unwrap(err) == nil || errors.Unwrap(err).Error() != refusal || !strings.Contains(err.Error(), "admission/policy") || strings.Contains(err.Error(), refusal) {
				t.Fatalf("admission error changed: %v", err)
			}
			launch := <-requests
			if launch.Agent != "claude" || launch.CWD != test.want || launch.Label != "week end" || launch.Role != "parent" || !launch.ScopedMCP || !reflect.DeepEqual(launch.Args, providerArgs) {
				t.Fatalf("scoped admission changed: %+v", launch)
			}
			if _, err := uuid.Parse(launch.RequestID); err != nil || ids[launch.RequestID] {
				t.Fatalf("request ID invalid/reused: %q %v", launch.RequestID, err)
			}
			ids[launch.RequestID] = true
		})
	}
	if calls.Load() != 4 {
		t.Fatalf("admission retried: %d calls", calls.Load())
	}
	if _, err := os.Stat(filepath.Join(f.local, "wing.pid")); !os.IsNotExist(err) {
		t.Fatalf("refusal silently became owner autostart: %v", err)
	}
}

func TestClaudeCWDRefusalsRetainWingPolicy(t *testing.T) {
	f := claudeControlFixture(t)
	wc := &config.WingConfig{WingID: "fixture-wing", Paths: config.PathList{{Path: f.work}}}
	service := &wingsession.Service{Config: &config.Config{Dir: f.local, WingID: wc.WingID}, Home: f.home,
		Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wc, Egg: egg.DefaultEggConfig()} }}
	server, err := localmcp.ListenLocalWingControl(t.Context(), "fixture", service, "fixture-owner", localmcp.NewMCPAdmissionState())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	coldPolicyGuard(t, f)
	file := filepath.Join(f.work, "regular-file")
	northstarWrite(t, file, "fixture", 0600)
	for _, test := range []struct {
		name, cwd, errorText string
	}{
		{"missing", filepath.Join(f.work, "missing project"), "no such file or directory"},
		{"file", file, "is not a directory"},
		{"denied", f.home, "outside this user's roost paths"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := newRootCommand()
			cmd.SetArgs([]string{"claude", "--cwd", test.cwd})
			cmd.SetErr(new(bytes.Buffer))
			if err := cmd.ExecuteContext(t.Context()); err == nil || errors.Unwrap(err) == nil || !strings.Contains(errors.Unwrap(err).Error(), test.errorText) || !strings.Contains(err.Error(), "admission/policy") {
				t.Fatalf("cwd refusal changed: %v", err)
			}
		})
	}
	if entries, _ := os.ReadDir(filepath.Join(f.local, "eggs")); len(entries) != 0 {
		t.Fatal("refused cwd admitted an egg")
	}
	if _, err := os.Stat(filepath.Join(f.local, "wing.pid")); !os.IsNotExist(err) {
		t.Fatalf("cwd refusal restarted wing: %v", err)
	}
}

func TestClaudeRemoteRoutingPreservesCWDAndProviderDelimiter(t *testing.T) {
	for _, cwd := range []string{"../forge project space", "--remote", "-r", "--remote-state", "--remote-batch"} {
		for _, interactive := range []bool{false, true} {
			want := []string{"claude", "--cwd", cwd, "--name", "--remote-cwd", "--", "--remote", "provider-host", "--remote-cwd", "provider path", "--remote-state", "provider state", "--json", "--remote-batch"}
			argv := append([]string{"--remote", "fixture-forge", "--remote-binary", "/opt/wt custom", "--remote-state", "/forge state"}, want...)
			route, remote, err := remotepkg.ParseRemoteInvocation(argv, interactive, newRootCommand)
			if err != nil || !remote || route.Target != "fixture-forge" || route.Binary != "/opt/wt custom" || route.State != "/forge state" || route.CWD != "" || route.NonInteractive || route.AllocateTTY != interactive || !reflect.DeepEqual(route.Args, want) {
				t.Fatalf("remote cwd/args changed: %+v remote=%t err=%v", route, remote, err)
			}
		}
	}
	if _, _, err := remotepkg.ParseRemoteInvocation([]string{"--remote", "fixture-forge", "--remote-cwd", "/forge", "claude"}, true, newRootCommand); err == nil || !strings.Contains(err.Error(), "command's --cwd") {
		t.Fatalf("bare remote cwd became a launch flag: %v", err)
	}
}
