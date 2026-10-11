//go:build runinspectfixture

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/runinspect"
	"github.com/ehrlich-b/wingthing/internal/runinspect/fixture"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestRunInspectRoundTripCLIPreservationAndFailedDestinations(t *testing.T) {
	binary, b := cliRoundTripFixture(t)
	b.Wing("run", "execution", "", "terminal", "done", fixture.PrivateOutput)
	b.Egg("run", "execution", "done", fixture.PrivateOutput)
	b.Seal()
	reports := filepath.Join(b.Workspace, "reports")
	if err := os.Mkdir(reports, 0700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(reports, "run-inspect.json")
	if err := os.WriteFile(existing, []byte("existing report must survive"), 0600); err != nil {
		t.Fatal(err)
	}
	notDirectory := filepath.Join(b.Workspace, "not-a-directory")
	if err := os.WriteFile(notDirectory, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	before := fixture.Inventory(t, b.Workspace)
	var first []byte
	for i := 0; i < 3; i++ {
		wire, err := runInspectBinary(t, binary, b.Workspace, "run-inspect", "--snapshot", b.Path, "--json")
		if err != nil {
			t.Fatalf("inspect: %v %s", err, wire)
		}
		if i == 0 {
			first = wire
		} else if !bytes.Equal(first, wire) {
			t.Fatal("repeated CLI output changed")
		}
	}
	for _, output := range []string{reports, notDirectory, filepath.Join(b.Workspace, "absent-output"), b.Path, b.Workspace} {
		wire, err := runInspectBinary(t, binary, b.Workspace, "run-inspect", "--snapshot", b.Path, "--json", "--output-dir", output)
		if err == nil {
			t.Fatalf("failed destination accepted: %s %s", output, wire)
		}
		if !reflect.DeepEqual(before, fixture.Inventory(t, b.Workspace)) {
			t.Fatalf("destination failure changed inventory/mtimes: %s", output)
		}
	}
	if !reflect.DeepEqual(before, fixture.Inventory(t, b.Workspace)) {
		t.Fatal("repeated CLI changed source tree")
	}
	freshOutput := filepath.Join(b.Workspace, "fresh-output")
	if err := os.Mkdir(freshOutput, 0700); err != nil {
		t.Fatal(err)
	}
	sourceBefore := fixture.Inventory(t, b.Path)
	wire, err := runInspectBinary(t, binary, b.Workspace, "run-inspect", "--snapshot", b.Path, "--json", "--output-dir", freshOutput)
	if err != nil || len(wire) != 0 {
		t.Fatalf("explicit output: %v %s", err, wire)
	}
	reportPath := filepath.Join(freshOutput, "run-inspect.json")
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, data) || info.Mode().Perm() != 0600 {
		t.Fatal("report content/privacy differs from stdout")
	}
	if !reflect.DeepEqual(sourceBefore, fixture.Inventory(t, b.Path)) {
		t.Fatal("report creation changed source tree/mtimes")
	}
}

type inspectFailedWriter struct{}

func (inspectFailedWriter) Write([]byte) (int, error) {
	return 0, errors.New("deliberate output failure")
}

func TestRunInspectRoundTripFailedStdout(t *testing.T) {
	_, b := cliRoundTripFixture(t)
	b.Wing("run", "execution", "", "terminal", "done", fixture.PrivateOutput)
	b.Egg("run", "execution", "done", fixture.PrivateOutput)
	b.Seal()
	before := fixture.Inventory(t, b.Workspace)
	t.Chdir(b.Workspace)
	cmd := runInspectCmd()
	cmd.SetArgs([]string{"--snapshot", b.Path, "--json"})
	cmd.SetOut(inspectFailedWriter{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "deliberate output failure") {
		t.Fatalf("output error lost: %v", err)
	}
	if !reflect.DeepEqual(before, fixture.Inventory(t, b.Workspace)) {
		t.Fatal("stdout failure changed source tree")
	}
}

func TestRunInspectRoundTripCLIPreviews(t *testing.T) {
	binary, b := cliRoundTripFixture(t)
	text := "Ω🙂\n\x1b\x00\u202e"
	b.Wing("run", "execution", "", "terminal", "done", text)
	b.Egg("run", "execution", "done", text)
	b.Seal()
	before := fixture.Inventory(t, b.Workspace)
	for _, limit := range []int{0, 1, 2, 3, 5, 4096} {
		args := []string{"run-inspect", "--snapshot", b.Path, "--preview-bytes", strconv.Itoa(limit)}
		wire, err := runInspectBinary(t, binary, b.Workspace, append(args, "--json")...)
		if err != nil {
			t.Fatalf("preview JSON: %v %s", err, wire)
		}
		var report runinspect.Report
		if err := json.Unmarshal(wire, &report); err != nil {
			t.Fatal(err)
		}
		for _, output := range report.Runs[0].Output {
			count := min(limit, len(text))
			want := ""
			if limit > 0 {
				want = strconv.QuoteToASCII(text[:count])
			}
			if output.OmittedBytes != int64(len(text)-count) || output.Bytes != int64(len(text)) || output.Preview != want {
				t.Fatalf("preview JSON accounting: %+v", output)
			}
		}
		human, err := runInspectBinary(t, binary, b.Workspace, args...)
		if err != nil {
			t.Fatalf("preview human: %v %s", err, human)
		}
		if strings.ContainsAny(string(human), "\x1b\x00\u202e") {
			t.Fatal("raw controls exposed")
		}
		if limit == 0 && (bytes.Contains(human, []byte("untrusted output preview")) || bytes.Contains(wire, []byte(`"preview"`))) {
			t.Fatal("implicit preview")
		}
		if limit > 0 && !bytes.Contains(human, []byte("untrusted output preview")) {
			t.Fatal("opted-in preview omitted")
		}
	}
	if !reflect.DeepEqual(before, fixture.Inventory(t, b.Workspace)) {
		t.Fatal("preview inspection changed source/mtimes")
	}
}

func TestRunInspectRoundTripCLIIsolatedWorkspaceOrphan(t *testing.T) {
	binary, b := cliRoundTripFixture(t)
	parent := &store.Task{ID: "missing-parent", Type: "agent_run", What: fixture.PrivatePrompt, Status: "done", Principal: "owner", RunAt: fixture.CapturedAt, CreatedAt: fixture.CapturedAt}
	if err := b.Store.CreateTask(parent); err != nil {
		t.Fatal(err)
	}
	b.Wing("orphan", "execution-orphan", parent.ID, "submitting", "pending", "")
	b.Egg("orphan", "execution-orphan", "done", fixture.PrivateOutput)
	// Healthy current writers retain their parent. This explicit copied-state
	// corruption proves an orphan does not erase a valid terminal receipt.
	b.SQL("PRAGMA foreign_keys=OFF")
	b.SQL("DELETE FROM tasks WHERE id=?", parent.ID)
	b.Seal()
	// The binary must work in a separate workspace with only supplied artifacts,
	// and no PATH entry for an installed wt or helper tool.
	isolated := filepath.Join(b.Workspace, "isolated-workspace")
	if err := os.Mkdir(isolated, 0700); err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(isolated, "fixture-orphan")
	if err := os.CopyFS(copyPath, os.DirFS(b.Path)); err != nil {
		t.Fatal(err)
	}
	before := fixture.Inventory(t, isolated)
	invocation := exec.CommandContext(t.Context(), binary, "run-inspect", "--snapshot", copyPath, "--json")
	invocation.Dir = isolated
	invocation.Env = append(os.Environ(), "PATH="+filepath.Join(isolated, "no-installed-tools"), "WINGTHING_DIR="+filepath.Join(isolated, "ambient-do-not-open"), "WINGTHING_PREVIEW_DIR="+filepath.Join(isolated, "ambient-do-not-open"))
	wire, err := invocation.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated built CLI: %v %s", err, wire)
	}
	var report runinspect.Report
	if err := json.Unmarshal(wire, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Runs) != 1 || report.Runs[0].ID != "orphan" || report.Runs[0].SessionID != "execution-orphan" || report.Runs[0].Diagnosis != "completed_with_output" || report.Runs[0].Acknowledgement != "terminal_receipt_not_in_wing" || report.Runs[0].EvidenceState != "incomplete" {
		t.Fatalf("orphan report: %+v", report.Runs)
	}
	found := false
	for _, issue := range report.Runs[0].Issues {
		if issue.Code == "missing_parent" && reflect.DeepEqual(issue.IDs, []string{"orphan", "missing-parent"}) {
			found = true
		}
	}
	if !found {
		t.Fatal("orphan identity/issue omitted")
	}
	for _, action := range report.Runs[0].Actions {
		if action.Kind == "manual_resume_eligible" || len(action.Requires) == 0 {
			t.Fatalf("orphan action: %+v", action)
		}
	}
	original, err := runinspect.Inspect(t.Context(), b.Path, runinspect.Options{WorkspaceRoot: b.Workspace})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, &report) {
		t.Fatal("report depends on cwd/source location")
	}
	if !reflect.DeepEqual(before, fixture.Inventory(t, isolated)) {
		t.Fatal("isolated CLI created ambient files or changed snapshot")
	}
	for name, data := range map[string][]byte{"orphan.json": wire} {
		if err := os.WriteFile(filepath.Join(b.Workspace, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("serializer orphan fixture: %s", b.Path)
}
