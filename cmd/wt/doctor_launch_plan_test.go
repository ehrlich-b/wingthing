package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/launchplan"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

type inventoryEntry struct {
	Modified int64
	Mode     fs.FileMode
	Size     int64
	Hash     string
}

func launchInventory(t *testing.T, root string) map[string]inventoryEntry {
	t.Helper()
	result := map[string]inventoryEntry{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := inventoryEntry{Mode: info.Mode(), Size: info.Size(), Modified: info.ModTime().UnixNano()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			hash := sha256.Sum256(data)
			item.Hash = hex.EncodeToString(hash[:])
		}
		if info.Mode()&os.ModeSymlink != 0 {
			item.Hash, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		result[path] = item
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}
func doctorLaunchArgs(t *testing.T) ([]string, string) {
	t.Helper()
	dir := testsocket.Dir(t)
	provider := filepath.Join(dir, "provider")
	home := filepath.Join(dir, "provider-home")
	if err := os.WriteFile(provider, []byte("DO NOT EXECUTE"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	return []string{"launch-plan", "--route", "agent_run", "--agent", "codex", "--cwd", dir, "--home", home, "--allowed-home-root", dir, "--executable", provider, "--unattended"}, dir
}
func TestDoctorLaunchPlanReadOnlyInventoryAndNoAmbientState(t *testing.T) {
	args, dir := doctorLaunchArgs(t)
	ambient := filepath.Join(dir, "must-not-create-wing")
	t.Setenv("WINGTHING_DIR", ambient)
	t.Setenv("WINGTHING_PREVIEW_DIR", ambient)
	t.Setenv("CODEX_HOME", filepath.Join(dir, "foreign-provider"))
	t.Setenv("OPENAI_API_KEY", "SENTINEL_AMBIENT_TOKEN")
	credential := filepath.Join(dir, "provider-home", "auth.json")
	if err := os.WriteFile(credential, []byte("SENTINEL_FILE_TOKEN"), 0600); err != nil {
		t.Fatal(err)
	}
	args = append(args, "--credential-path", credential, "--json")
	before := launchInventory(t, dir)
	cmd := doctorCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, launchInventory(t, dir)) {
		t.Fatal("launch-plan changed filesystem inventory")
	}
	var report launchplan.Report
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.ReadOnly || report.Readiness != "unverified" || report.Route != "agent_run" {
		t.Fatalf("report: %+v", report)
	}
	if strings.Contains(output.String(), "SENTINEL") || strings.Contains(output.String(), ambient) {
		t.Fatal("ambient secret/state exported")
	}
}
func TestDoctorLaunchPlanHumanJSONAndHelp(t *testing.T) {
	args, _ := doctorLaunchArgs(t)
	for _, jsonFlag := range []bool{false, true} {
		cmd := doctorCmd()
		var output bytes.Buffer
		cmd.SetOut(&output)
		copy := append([]string(nil), args...)
		if jsonFlag {
			copy = append(copy, "--json")
		}
		cmd.SetArgs(copy)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "REQUIRED-UNRUN") {
			t.Fatal(output.String())
		}
	}
	cmd := doctorCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"launch-plan", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--route", "--home", "--facts", "--provider-evidence", "--json"} {
		if !strings.Contains(output.String(), flag) {
			t.Fatalf("help missing %s", flag)
		}
	}
}
func TestDoctorLaunchPlanRejectsFixPromptAndPassthrough(t *testing.T) {
	base, _ := doctorLaunchArgs(t)
	for _, extra := range [][]string{{"--fix"}, {"--prompt", "SENTINEL_PROMPT"}, {"--", "SENTINEL_PROMPT"}, {"--token", "SENTINEL_TOKEN"}} {
		cmd := doctorCmd()
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetArgs(append(append([]string(nil), base...), extra...))
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted %v", extra)
		}
	}
	cmd := doctorCmd()
	cmd.SetArgs(append([]string{"--fix"}, base...))
	if err := cmd.Execute(); err == nil {
		t.Fatalf("parent fix accepted: %v", err)
	}
}
func TestLaunchPlanRootCommandDoesNotLoadState(t *testing.T) {
	args, dir := doctorLaunchArgs(t)
	t.Setenv("WINGTHING_DIR", filepath.Join(dir, "absent-state"))
	root := newRootCommand()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(append([]string{"doctor"}, args...))
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "absent-state")); !os.IsNotExist(err) {
		t.Fatal("root dispatch created state")
	}
}

func TestDoctorLaunchPlanIndependentJSONConsumer(t *testing.T) {
	args, _ := doctorLaunchArgs(t)
	cmd := doctorCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs(append(args, "--json"))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(output.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if wire["schema_version"] != float64(1) || wire["read_only"] != true || wire["acceptance"] != "externally-pending" {
		t.Fatalf("consumer contract: %v", wire)
	}
	gates, ok := wire["gates"].([]any)
	if !ok || len(gates) == 0 {
		t.Fatal("empty consumer gate list")
	}
	unrun := map[string]bool{}
	for _, item := range gates {
		gate := item.(map[string]any)
		if gate["status"] == "REQUIRED-UNRUN" {
			unrun[gate["code"].(string)] = true
		}
		if gate["source"] == "" {
			t.Fatal("consumer received unattributed gate")
		}
	}
	for _, code := range []string{"sandbox.native", "network.parent", "authentication", "provider.evidence", "route.agent_run"} {
		if !unrun[code] {
			t.Fatalf("consumer missing required gate %s", code)
		}
	}
}
func TestDoctorLaunchPlanRejectionRedactsValues(t *testing.T) {
	base, _ := doctorLaunchArgs(t)
	for _, extra := range [][]string{{"--prompt", "SENTINEL_PROMPT"}, {"--", "SENTINEL_PROMPT"}, {"--SENTINEL_TOKEN"}} {
		cmd := doctorCmd()
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetArgs(append(append([]string(nil), base...), extra...))
		err := cmd.Execute()
		if err == nil || strings.Contains(err.Error(), "SENTINEL") || strings.Contains(output.String(), "SENTINEL") {
			t.Fatalf("rejected values were retained: %v / %s", err, output.String())
		}
	}
}

func TestDoctorLaunchPlanSeparatePolicyBaseAndMergeHome(t *testing.T) {
	args, dir := doctorLaunchArgs(t)
	home := filepath.Join(dir, "provider-home")
	child := filepath.Join(dir, "private")
	host := filepath.Join(dir, "host-home")
	for _, path := range []string{child, host} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	parent := filepath.Join(dir, "parent.yaml")
	policy := filepath.Join(dir, "egg.yaml")
	if err := os.WriteFile(parent, []byte("base: none\nfs: [deny:~/.codex]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	body := "base: ./parent.yaml\nfs: [rw:./, deny:./private, rw:" + filepath.Join(home, ".codex") + "]\n"
	if err := os.WriteFile(policy, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	args = append(args, "--cwd", child, "--config", policy, "--policy-base", dir, "--merge-home", host, "--json")
	before := launchInventory(t, dir)
	cmd := doctorCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var report launchplan.Report
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Readiness != "blocked" || report.PolicyBase.Path != dir || report.MergeHome.Path != host {
		t.Fatalf("lost launch inputs: %+v", report)
	}
	for _, code := range []string{"workspace.write", "provider_home.write"} {
		found := false
		for _, gate := range report.Gates {
			if gate.Code == code && gate.Status == launchplan.Fail {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing denial for %s: %+v", code, report.Gates)
		}
	}
	if !reflect.DeepEqual(before, launchInventory(t, dir)) {
		t.Fatal("explicit launch inputs caused filesystem writes")
	}
}
