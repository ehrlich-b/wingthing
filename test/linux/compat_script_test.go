//go:build (darwin || linux) && !e2e

package linux_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func compatScript(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../scripts/test-backward-compat.sh")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCompatDefaultBaselines(t *testing.T) {
	header, _, ok := strings.Cut(compatScript(t), "\nBASELINE_REF=")
	if !ok {
		t.Fatal("compatibility script baseline selection is missing")
	}
	root := t.TempDir()
	log := filepath.Join(root, "baselines")
	recorder := filepath.Join(root, "record-baseline")
	if err := os.WriteFile(recorder, []byte("#!/bin/sh\nprintf '%s\\n' \"$WT_COMPAT_BASELINE_REF\" >> \"$BASELINE_LOG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", header, recorder)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "WT_COMPAT_BASELINE_REF=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "BASELINE_LOG="+log)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("default baseline dispatch: %v\n%s", err, output)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v0.144.1\nv0.147.0\n" {
		t.Fatalf("default baselines = %q", data)
	}
}

func TestCompatTokenUpgradeAndRollback(t *testing.T) {
	script := compatScript(t)
	section := func(start, end string) string {
		t.Helper()
		_, tail, ok := strings.Cut(script, start+"\n")
		if !ok {
			t.Fatalf("compatibility phase start is missing: %s", start)
		}
		body, _, ok := strings.Cut(tail, end)
		if !ok {
			t.Fatalf("compatibility phase end is missing: %s", end)
		}
		return body
	}
	snapshot := section(`start_gateway "$BASELINE_BIN" "$TMP_ROOT/baseline-gateway-initial.log"`+"\nstop_gateway", "# Additive current wing fields")
	upgrade := section(`start_gateway "$CANDIDATE_BIN" "$TMP_ROOT/candidate-gateway.log"`, `start_wing "$BASELINE_BIN"`)
	rollback := section(`start_gateway "$BASELINE_BIN" "$TMP_ROOT/baseline-gateway-rollback.log"`, `start_wing "$CANDIDATE_BIN"`)
	for _, tc := range []struct {
		name, initial, upgrade, rollback string
		wantFailure                      bool
	}{
		{"legacy token", "device_token.yaml", "cp device_token.yaml local_device_token.yaml", ":", false},
		{"local token", "local_device_token.yaml", ":", ":", false},
		{"both tokens", "device_token.yaml local_device_token.yaml", ":", ":", false},
		{"missing tokens", "", ":", ":", true},
		{"empty token", "local_device_token.yaml", ": > local_device_token.yaml", ":", true},
		{"changed legacy token", "device_token.yaml", "echo changed > device_token.yaml; cp device_token.yaml local_device_token.yaml", ":", true},
		{"changed local token", "local_device_token.yaml", "echo changed > local_device_token.yaml", ":", true},
		{"wrong legacy migration", "device_token.yaml", "echo changed > local_device_token.yaml", ":", true},
		{"unexpected hosted token", "local_device_token.yaml", "cp local_device_token.yaml device_token.yaml", ":", true},
		{"rollback changes token", "local_device_token.yaml", ":", "echo changed > local_device_token.yaml", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, "state")
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range strings.Fields(tc.initial) {
				if err := os.WriteFile(filepath.Join(state, name), []byte("baseline token\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("sh", "-ec", snapshot+tc.upgrade+"\n"+upgrade+tc.rollback+"\n"+rollback)
			cmd.Dir = state
			cmd.Env = append(os.Environ(), "STATE_DIR="+state, "TMP_ROOT="+root)
			output, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantFailure {
				t.Fatalf("token preservation: %v\n%s", err, output)
			}
		})
	}
}

func TestCompatWingUsesBaselineTokenStore(t *testing.T) {
	_, launch, ok := strings.Cut(compatScript(t), "\nstart_wing() {\n")
	if !ok {
		t.Fatal("compatibility wing launcher is missing")
	}
	launch, _, ok = strings.Cut(launch, "\n    WING_PID=$!")
	if !ok {
		t.Fatal("compatibility wing PID capture is missing")
	}
	// Capture the real launch arguments synchronously without starting a wing.
	launch = strings.TrimSuffix(launch, " &")
	for _, legacy := range []bool{true, false} {
		name := "local"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if legacy {
				if err := os.WriteFile(filepath.Join(root, "baseline-device_token.yaml"), []byte("token"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			argsFile := filepath.Join(root, "args")
			recorder := filepath.Join(root, "record-wing")
			if err := os.WriteFile(recorder, []byte("#!/bin/sh\ntest \"$HOME\" = \"$STATE_DIR\" || exit 1\nprintf '%s\\n' \"$@\" > \"$WT_COMPAT_ARGS_FILE\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-ec", "start_wing() {\n"+launch+"\n}\nstart_wing \"$1\" \"$TMP_ROOT/wing.log\"", "sh", recorder)
			cmd.Env = append(os.Environ(), "TMP_ROOT="+root, "STATE_DIR="+root, "AGENT_DIR="+root, "REPO_ROOT="+root, "PORT=12345", "WT_COMPAT_ARGS_FILE="+argsFile)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("launch arguments: %v\n%s", err, output)
			}
			data, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			want := "daemon\nstart\n--foreground\n"
			if !legacy {
				want += "--local\n"
			}
			want += "--roost\nhttp://127.0.0.1:12345\n--paths\n" + root + "\n--egg-config\n" + root + "/test/web/egg.yaml\n"
			if string(data) != want {
				t.Fatalf("wing arguments = %q, want %q", data, want)
			}
		})
	}
}
