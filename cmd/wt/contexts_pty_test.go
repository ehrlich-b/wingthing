//go:build !windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"gopkg.in/yaml.v3"
)

func TestContextBuiltPTYHomeWork(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	if err := config.SaveWingConfig(f.local, &config.WingConfig{MachineContext: "home"}); err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(f.local, "remotes.yaml"), fmt.Sprintf("remotes:\n  studio:\n    ssh_target: ns-devbox\n    wingthing_dir: %s\n    context: work\n  office:\n    ssh_target: ns-empty\n    wingthing_dir: %s\n    context: work\n", f.remote, filepath.Join(f.home, "e")), 0600)
	northstarStandaloneEgg(t, f, f.local, "home-task", "home-long-running-task", "needs_input", "home")
	northstarStandaloneEgg(t, f, f.remote, "work-task", "work-long-running-task", "needs_input", "work")
	northstarStandaloneEgg(t, f, filepath.Join(f.home, "e"), "office-task", "office-long-running-task", "working", "office")
	h := northstarStartPTY(t, f, binary, 120, 36)
	screen := h.expect("Home and Work groups", 0, "Home · 1 needs you", "Work · 1 needs you", "studio", "office", "This machine")
	northstarWrite(t, filepath.Join(f.evidence, "context-built-pty-120x36.txt"), screen, 0600)
	h.expect("home filter", h.send("c"), "Home   1 machine connected")
	h.expect("work filter", h.send("c"), "Work   2 machines connected")
	h.exit("q")
}

func TestContextBuiltCLIValidatesLocalDocument(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	legacy, err := os.ReadFile("../../internal/config/testdata/context-legacy-wing-id")
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []string{"context-conflicting-wing.yaml", "context-wing.yaml"} {
		t.Run(fixture, func(t *testing.T) {
			before, err := os.ReadFile(filepath.Join("../../internal/config/testdata", fixture))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.local, "wing.yaml")
			if err := os.WriteFile(path, before, 0640); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0640); err != nil {
				t.Fatal(err)
			}
			legacyPath := filepath.Join(f.local, "wing-id")
			if err := os.WriteFile(legacyPath, legacy, 0600); err != nil {
				t.Fatal(err)
			}
			run := func(args ...string) (string, error) {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), binary, args...)
				cmd.Env = f.env
				out, err := cmd.CombinedOutput()
				t.Logf("wt %v: %v\n%s", args, err, out)
				return string(out), err
			}
			invalid := fixture == "context-conflicting-wing.yaml"
			for _, value := range []string{"none", "home", "work", "none"} {
				_, loadErr := config.LoadWingConfig(f.local)
				if invalid && loadErr == nil {
					t.Fatal("loader accepted conflicting keys")
				}
				out, err := run("context", "set", "local", value)
				if (err != nil) != invalid {
					t.Errorf("set %s: %v\n%s", value, err, out)
				}
				list, listErr := run("context", "list")
				_, wingErr := run("wing", "config")
				cfg, loadErr := config.LoadWingConfig(f.local)
				if (listErr != nil) != invalid || (wingErr != nil) != invalid || (loadErr != nil) != invalid {
					t.Errorf("loader/list/wing disagree: %v, %v, %v", loadErr, listErr, wingErr)
				}
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if invalid {
					if !bytes.Equal(after, before) || info.Mode().Perm() != 0640 {
						t.Errorf("refusal changed bytes or mode: %q, %v", after, info.Mode())
					}
				} else {
					want, listed := value, value
					if value == "none" {
						want, listed = "", "none"
					}
					if cfg == nil || cfg.MachineContext != want || !strings.Contains(strings.Join(strings.Fields(list), " "), "local "+listed) {
						t.Errorf("transition %s: loader=%+v, list=%s", value, cfg, list)
					}
					var expected, actual map[string]any
					if err := yaml.Unmarshal(before, &expected); err != nil {
						t.Fatal(err)
					}
					if err := yaml.Unmarshal(after, &actual); err != nil {
						t.Fatal(err)
					}
					delete(expected, "context")
					delete(actual, "context")
					if !reflect.DeepEqual(expected, actual) || info.Mode().Perm() != 0600 {
						t.Errorf("unrelated fields or private mode changed: %#v, %v", actual, info.Mode())
					}
				}
				legacyAfter, err := os.ReadFile(legacyPath)
				if err != nil || !bytes.Equal(legacyAfter, legacy) {
					t.Fatalf("legacy identity changed: %q, %v", legacyAfter, err)
				}
			}
		})
	}
}

func TestContextSetBuiltPTY(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	northstarWrite(t, filepath.Join(f.local, "remotes.yaml"), fmt.Sprintf("remotes:\n  studio:\n    ssh_target: ns-devbox\n    wingthing_dir: %s\n", f.remote), 0600)
	northstarStandaloneEgg(t, f, f.local, "home-task", "investigate-long-running-home-task", "needs_input", "home")
	h := northstarStartPTY(t, f, binary, 120, 36)
	screen := h.expect("ungrouped context hint", 0, "wt context set", "Needs you")
	if strings.Count(screen, "wt context set") != 1 {
		t.Fatalf("expected one hint:\n%s", screen)
	}
	northstarWrite(t, filepath.Join(f.evidence, "context-set-unassigned-120x36.txt"), screen, 0600)
	h.exit("q")
	for _, args := range [][]string{{"context", "set", "local", "home"}, {"context", "set", "studio", "work"}} {
		command := northstarStartPTY(t, f, binary, 120, 36, args...)
		command.exit("")
	}
	h = northstarStartPTY(t, f, binary, 120, 36)
	screen = h.expect("command configured Home and Work", 0, "Home · 1 needs you", "Work · 0 needs you", "studio", "This machine")
	if strings.Contains(screen, "wt context set") {
		t.Fatalf("assigned machines still show setup hint:\n%s", screen)
	}
	northstarWrite(t, filepath.Join(f.evidence, "context-set-grouped-120x36.txt"), screen, 0600)
	h.exit("q")
}

func TestContextBuiltCLIPreservesLegacyWingID(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	legacy, err := os.ReadFile("../../internal/config/testdata/context-legacy-wing-id")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.local, "wing-id")
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Env = f.env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("wt %v: %v\n%s", args, err, out)
		}
		t.Logf("wt %v:\n%s", args, out)
		return string(out)
	}
	for _, value := range []string{"none", "home", "work", "none"} {
		run("context", "set", "local", value)
		out := run("wing", "config")
		if !strings.Contains(out, "wing_id:    0123456789abcdef0123\n") {
			t.Fatalf("identity lost after %s:\n%s", value, out)
		}
		cfg, err := config.LoadWingConfig(f.local)
		if err != nil || cfg.WingID != "0123456789abcdef0123" {
			t.Fatalf("loaded identity after %s: %+v, %v", value, cfg, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, legacy) {
			t.Fatalf("legacy bytes changed after %s: %q, %v", value, after, err)
		}
	}
}
