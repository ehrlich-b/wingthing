package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/runinspect"
)

func inspectTree(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !d.Type().IsRegular() {
			t.Fatalf("unexpected file type: %s", p)
		}
		data, err := os.ReadFile(p)
		out[p] = string(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func inspectBinaryFixture(t *testing.T) (string, string, string) {
	t.Helper()
	binary := os.Getenv("WT_TEST_BINARY")
	if binary == "" {
		t.Fatal("WT_TEST_BINARY is required; run make test-run-inspect")
	}
	workspace := t.TempDir()
	snapshot := filepath.Join(workspace, "snapshot")
	source := filepath.Join("..", "..", "internal", "runinspect", "testdata", "orphan")
	if err := os.CopyFS(snapshot, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	return binary, workspace, snapshot
}

func runInspectBinary(t *testing.T, binary, workspace string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binary, args...)
	cmd.Dir = workspace
	// Deliberately unusable ambient state proves snapshot inspection bypasses
	// configuration initialization. The real user's state is never selected.
	cmd.Env = append(os.Environ(), "WINGTHING_DIR="+filepath.Join(workspace, "ambient-do-not-open"), "WINGTHING_PREVIEW_DIR="+filepath.Join(workspace, "ambient-do-not-open"))
	return cmd.CombinedOutput()
}

func TestRunInspectBuiltBinaryOfflineSnapshot(t *testing.T) {
	binary, workspace, snapshot := inspectBinaryFixture(t)
	before := inspectTree(t, workspace)
	first, err := runInspectBinary(t, binary, workspace, "run", "inspect", "--snapshot", snapshot, "--json")
	if err != nil {
		t.Fatalf("inspect: %v %s", err, first)
	}
	var report runinspect.Report
	if err := json.Unmarshal(first, &report); err != nil {
		t.Fatal(err)
	}
	if report.Version != 1 || len(report.Runs) != 1 || report.Runs[0].ID != "fixture-orphan" || report.Runs[0].Diagnosis != "completed_with_output" || report.Runs[0].Acknowledgement != "terminal_receipt_not_in_wing" {
		t.Fatalf("offline diagnosis: %+v", report)
	}
	if strings.Contains(string(first), "ORIGINAL-FIXTURE-PROVIDER-SENTINEL") {
		t.Fatal("default JSON exposed provider content")
	}
	second, err := runInspectBinary(t, binary, workspace, "run", "inspect", "--snapshot", snapshot, "--json")
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("CLI report is nondeterministic")
	}
	human, err := runInspectBinary(t, binary, workspace, "run", "inspect", "--snapshot", snapshot)
	if err != nil || !bytes.Contains(human, []byte("completed_with_output")) || bytes.Contains(human, []byte("ORIGINAL-FIXTURE-PROVIDER-SENTINEL")) {
		t.Fatalf("human summary: %v %s", err, human)
	}
	if !reflect.DeepEqual(before, inspectTree(t, workspace)) {
		t.Fatal("stdout inspection created files or changed supplied state")
	}
}

func TestRunInspectBuiltBinaryOutputBoundaries(t *testing.T) {
	binary, workspace, snapshot := inspectBinaryFixture(t)
	for _, args := range [][]string{
		{"run", "inspect"},
		{"run", "inspect", "--snapshot", snapshot, "--preview-bytes", "4097"},
		{"run", "inspect", "--snapshot", snapshot, "--output-dir", snapshot},
		{"run", "inspect", "--snapshot", snapshot, "--output-dir", workspace},
		{"run", "inspect", "--snapshot", snapshot, "--remote", "invalid-host"},
	} {
		before := inspectTree(t, workspace)
		if out, err := runInspectBinary(t, binary, workspace, args...); err == nil {
			t.Fatalf("invalid inspection accepted: %v %s", args, out)
		}
		if !reflect.DeepEqual(before, inspectTree(t, workspace)) {
			t.Fatal("invalid invocation modified state")
		}
	}
	outputDir := filepath.Join(workspace, "reports")
	if err := os.Mkdir(outputDir, 0700); err != nil {
		t.Fatal(err)
	}
	before := inspectTree(t, snapshot)
	out, err := runInspectBinary(t, binary, workspace, "run", "inspect", "--snapshot", snapshot, "--json", "--output-dir", outputDir)
	if err != nil || len(out) != 0 {
		t.Fatalf("explicit output: %v %s", err, out)
	}
	file := filepath.Join(outputDir, "run-inspect.json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("report permissions are not private")
	}
	if out, err := runInspectBinary(t, binary, workspace, "run", "inspect", "--snapshot", snapshot, "--json", "--output-dir", outputDir); err == nil {
		t.Fatalf("overwrote prior report: %s", out)
	}
	current, _ := os.ReadFile(file)
	if !bytes.Equal(data, current) || !reflect.DeepEqual(before, inspectTree(t, snapshot)) {
		t.Fatal("report retry changed bytes")
	}
}
