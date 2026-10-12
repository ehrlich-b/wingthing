package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

const launchSecret = "EXCLUDED_CREDENTIAL_PROMPT_SENTINEL"

func TestClaudeLaunchDiagnosticTransport(t *testing.T) {
	f := claudeControlFixture(t)
	policy := filepath.Join(f.home, "selected.yaml")
	inherited := filepath.Join(f.home, "inherited.yaml")
	original := control.WithLaunchDiagnostic(control.ErrSessionNameInUse, control.LaunchDiagnostic{Stage: "admission/policy", Reason: "session_name_in_use", PolicyOrigin: "selected policy", PolicyPath: policy, PolicySource: inherited})
	var calls atomic.Int32
	server, err := controlsocket.Listen(t.Context(), f.local, "fixture", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{}, func(_ context.Context, request control.DirectRequest) control.DirectResponse {
			calls.Add(1)
			result := control.ErrorResult(original)
			delete(result, "error_kind") // The direct envelope may carry the kind.
			result["error"] = launchSecret
			result["argv"], result["environment"], result["prompt"] = launchSecret, launchSecret, launchSecret
			return control.DirectResponse{Version: control.ContractVersion, ID: request.ID, IsError: true, ErrorKind: control.ErrorSessionNameInUse, Result: result}
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	_, err = localmcp.CallLocalWingTool(t.Context(), f.local, "", "agent_start", json.RawMessage(`{}`))
	d, ok := control.LaunchDiagnosticOf(err)
	if !ok || !errors.Is(err, control.ErrSessionNameInUse) || d.PolicyPath != policy || d.PolicySource != inherited {
		t.Fatalf("structured failure lost: %+v %v", d, err)
	}
	cmd := newRootCommand()
	cmd.SetArgs([]string{"claude", "--", launchSecret})
	cmd.SetErr(new(bytes.Buffer))
	err = cmd.ExecuteContext(t.Context())
	if err == nil || !errors.Is(err, control.ErrSessionNameInUse) || !strings.Contains(err.Error(), inherited) || strings.Contains(err.Error(), launchSecret) {
		t.Fatalf("unsafe/incorrect diagnostic: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("repeated admission: %d", calls.Load())
	}
}

func TestClaudeLaunchDiagnosticBoundsAndPhases(t *testing.T) {
	canceled := diagnoseClaudeFailure(errors.Join(context.Canceled, errors.New(launchSecret)), "connect/startup", "startup_failed", "")
	if !errors.Is(canceled, context.Canceled) || !strings.Contains(canceled.Error(), "context canceled") || strings.Contains(canceled.Error(), launchSecret) {
		t.Fatalf("unsafe/incorrect cancellation diagnostic: %v", canceled)
	}
	for _, tc := range []struct{ stage, reason, session, want string }{
		{"connect/startup", "startup_failed", "", "wt wing start --local-only"},
		{"admission/policy", "admission_unknown", "", "Inspect wt ls"},
		{"provider startup", "provider_failed", "", "provider could not start"},
		{"attach-after-admission", "attach_failed", "exact-session", "wt attach exact-session"},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			err := diagnoseClaudeFailure(errors.New(launchSecret), tc.stage, tc.reason, tc.session)
			if !strings.Contains(err.Error(), tc.stage) || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), launchSecret) {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "wt doctor launch-plan --") {
				t.Fatal("invented doctor request")
			}
		})
	}
	d := control.LaunchDiagnostic{Stage: launchSecret, Reason: launchSecret, PolicyOrigin: launchSecret, PolicyPath: "/policy\x1b[31m", PolicySource: strings.Repeat("x", 4097), Session: "session\n"}.Bounded()
	wire, _ := json.Marshal(d)
	if bytes.Contains(wire, []byte(launchSecret)) || d.PolicyPath != "" || d.PolicySource != "" || d.Session != "" {
		t.Fatalf("unbounded metadata: %s", wire)
	}
}

func TestClaudeLaunchDiagnosticFrame(t *testing.T) {
	capture := &launchDiagnosticCapture{}
	_, _ = capture.Write(bytes.Repeat([]byte(launchSecret), 4096))
	if len(capture.tail) > 16<<10 {
		t.Fatal("unbounded remote output capture")
	}
	if _, ok := capture.diagnostic(); ok {
		t.Fatal("raw remote output became a diagnostic")
	}
	wire := []byte(`{"stage":"provider startup","reason":"provider_failed","policy_origin":"built-in defaults","argv":"` + launchSecret + `","prompt":"` + launchSecret + `"}`)
	frame := control.LaunchDiagnosticFramePrefix + base64.StdEncoding.EncodeToString(wire) + "\x1b\\"
	for _, fragment := range []string{frame[:11], frame[11:37], frame[37:]} {
		_, _ = capture.Write([]byte(fragment))
	}
	d, ok := capture.diagnostic()
	if !ok || d.Stage != "provider startup" || d.Reason != "provider_failed" {
		t.Fatalf("fragmented typed frame lost: %+v", d)
	}
	projected, _ := json.Marshal(d)
	if bytes.Contains(projected, []byte(launchSecret)) {
		t.Fatal("excluded remote fields entered metadata")
	}
	_, _ = capture.Write([]byte(control.LaunchDiagnosticFramePrefix + "invalid\x1b\\"))
	if _, ok := capture.diagnostic(); ok {
		t.Fatal("invalid terminal frame accepted")
	}
}

func TestClaudeLaunchDiagnosticPreparation(t *testing.T) {
	for _, tc := range []struct{ name, rule, reason string }{
		{"inherited executable denial", "deny:", "executable_denied"},
		{"provider home denial", "deny:", "provider_home_denied"},
		{"provider config denial", "deny:", "provider_config_denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := northstarNewFixture(t)
			t.Setenv("HOME", f.home)
			t.Setenv("TMPDIR", f.tmp)
			provider := filepath.Join(f.bin, "claude")
			northstarWrite(t, provider, "#!/bin/sh\nexit 99\n", 0700)
			if err := os.MkdirAll(filepath.Join(f.home, ".claude"), 0700); err != nil {
				t.Fatal(err)
			}
			settings := filepath.Join(f.home, ".claude", "settings.json")
			northstarWrite(t, settings, `{"secret":"`+launchSecret+`"}`, 0600)
			target := provider
			if tc.reason == "provider_home_denied" {
				target = "~/.claude"
			}
			if tc.reason == "provider_config_denied" {
				target = settings
			}
			inherited := filepath.Join(f.home, "inherited.yaml")
			selected := filepath.Join(f.home, "selected.yaml")
			northstarWrite(t, inherited, "base: none\nfs: ['ro:/', 'rw:"+f.work+"', '"+tc.rule+target+"']\n", 0600)
			northstarWrite(t, selected, "base: ./inherited.yaml\nenv: [HOME, PATH]\n", 0600)
			cfg, err := egg.ResolveEggConfig(selected)
			if err != nil {
				t.Fatal(err)
			}
			nested := filepath.Join(f.work, "nested")
			if err := os.Mkdir(nested, 0700); err != nil {
				t.Fatal(err)
			}
			req := &egg.LaunchRequirements{PolicyBase: f.work, PolicySource: selected, Rules: cfg.LaunchPolicy.Rules}
			rc := egg.RunConfig{Agent: "claude", CWD: nested, UserHome: f.home, FS: cfg.FS, ProviderSessionID: "fixture-session"}
			if tc.reason == "provider_config_denied" {
				rc.AgentArgs = []string{"--settings", settings}
			}
			eggDir := filepath.Join(f.local, "eggs", "fixture")
			if err := os.MkdirAll(eggDir, 0700); err != nil {
				t.Fatal(err)
			}
			northstarSocket(t, filepath.Join(eggDir, "egg.sock"))
			prepared, prepErr := egg.PrepareLaunch(rc, egg.LaunchPreparationOptions{Binary: provider, Home: f.home, HostHome: f.home, EggDir: eggDir, Environment: map[string]string{"HOME": f.home, "PATH": f.bin}, CodexCapability: egg.SavedCodexCapability(""), Requirements: req}, egg.NativeLaunchState())
			if tc.reason == "provider_config_denied" && (prepErr == nil || !strings.Contains(prepErr.Error(), "settings file is outside the session filesystem policy")) {
				t.Fatalf("config fixture did not exercise policy refusal: %v", prepErr)
			}
			if prepErr == nil {
				prepErr = errors.New(launchSecret)
			}
			projected := egg.ProjectLaunchFailure(prepErr, cfg.LaunchPolicy, prepared, "admission/policy", "preparation_failed")
			d, ok := control.LaunchDiagnosticOf(projected)
			if !ok || d.PolicyPath != selected || d.Reason != tc.reason {
				t.Fatalf("preparation projection: %+v %v checks=%+v", d, projected, prepared.Checks)
			}
			if d.PolicySource != inherited {
				t.Fatalf("inherited denial lost: %+v", d)
			}
			out := diagnoseClaudeFailure(projected, "admission/policy", "policy_refused", "").Error()
			if strings.Contains(out, launchSecret) {
				t.Fatalf("excluded content escaped: %s", out)
			}
		})
	}
}

func TestClaudeLaunchAdmissionPolicyProvenance(t *testing.T) {
	f := claudeControlFixture(t)
	clients := filepath.Join(f.local, "clients.yaml")
	northstarWrite(t, clients, "clients:\n  default:\n    grants: [read, spawn, control]\n", 0600)
	wc := &config.WingConfig{WingID: "fixture-wing", Paths: config.PathList{{Path: f.work}}}
	service := &wingsession.Service{Config: &config.Config{Dir: f.local, WingID: wc.WingID}, Home: f.home, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wc, Egg: egg.DefaultEggConfig()} }}
	server, err := localmcp.ListenLocalWingControl(t.Context(), "fixture", service, "fixture-owner", localmcp.NewMCPAdmissionState())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	t.Setenv("WT_MCP_CLIENT", launchSecret)
	for _, tc := range []struct {
		locked         bool
		reason, phrase string
	}{
		{false, "client_not_configured", "not configured in clients.yaml"},
		{true, "authentication_required", "passkey authentication is required"},
	} {
		wc.Locked = tc.locked
		cmd := newRootCommand()
		cmd.SetArgs([]string{"claude", "--", launchSecret})
		cmd.SetErr(new(bytes.Buffer))
		err := cmd.ExecuteContext(t.Context())
		d, ok := control.LaunchDiagnosticOf(err)
		if err == nil || !ok || d.Stage != "admission/policy" || d.Reason != tc.reason || !strings.Contains(err.Error(), tc.phrase) || strings.Contains(err.Error(), launchSecret) {
			t.Fatalf("unsafe/incorrect admission projection: %+v %v", d, err)
		}
		if !tc.locked && d.PolicyPath != clients {
			t.Fatalf("selected client policy lost: %+v", d)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(f.local, "eggs")); len(entries) != 0 {
		t.Fatal("refused authentication admitted an egg")
	}
}
