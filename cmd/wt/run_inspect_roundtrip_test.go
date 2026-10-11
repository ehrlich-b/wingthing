//go:build runinspectfixture

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/runinspect"
	"github.com/ehrlich-b/wingthing/internal/runinspect/fixture"
)

func cliRoundTripFixture(t *testing.T) (string, *fixture.Builder) {
	t.Helper()
	binary := os.Getenv("WT_TEST_BINARY")
	root := os.Getenv("WT_INSPECT_FIXTURE_ROOT")
	if binary == "" || root == "" {
		t.Fatal("built binary and scratch fixture root required; run make test-run-inspect-roundtrip")
	}
	workspace, err := os.MkdirTemp(root, strings.ReplaceAll(t.Name(), "/", "_")+"-")
	if err != nil {
		t.Fatal(err)
	}
	return binary, fixture.New(t, filepath.Join(workspace, "snapshot"))
}

func TestRunInspectRoundTripBuiltCLITransitions(t *testing.T) {
	binary, b := cliRoundTripFixture(t)
	b.Wing("completed", "execution-completed", "", "terminal", "done", fixture.PrivateOutput)
	b.Egg("completed", "execution-completed", "done", fixture.PrivateOutput)
	b.Wing("lost-ack", "execution-lost", "", "submitting", "pending", "")
	b.Egg("lost-ack", "execution-lost", "done", fixture.PrivateOutput)
	b.Wing("interrupted", "execution-interrupted", "", "terminal", "timeout", fixture.PrivateOutput)
	b.Egg("interrupted", "execution-interrupted", "timeout", fixture.PrivateOutput)
	c := b.Conversation("logical", "execution-interrupted", "")
	seq := b.Event(c, 1)
	b.Checkpoint(c.ID, 0, seq)
	b.Archive("execution-interrupted", "claude", "native-execution-interrupted")
	b.Seal()
	before := fixture.Inventory(t, b.Workspace)
	typed, err := runinspect.Inspect(t.Context(), b.Path, runinspect.Options{WorkspaceRoot: b.Workspace})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := runInspectBinary(t, binary, b.Workspace, "run-inspect", "--snapshot", b.Path, "--json")
	if err != nil {
		t.Fatalf("CLI: %v %s", err, wire)
	}
	var decoded runinspect.Report
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(typed, &decoded) {
		t.Fatal("Go/CLI JSON contract differs")
	}
	human, err := runInspectBinary(t, binary, b.Workspace, "run-inspect", "--snapshot", b.Path)
	if err != nil {
		t.Fatalf("human CLI: %v %s", err, human)
	}
	for _, want := range []string{`Run "completed" execution "execution-completed": completed_with_output`, `Run "interrupted" execution "execution-interrupted": timeout`, `Run "lost-ack" execution "execution-lost": completed_with_output`, "acknowledgement=terminal_receipt_not_in_wing", "suggested_action=manual_resume_eligible"} {
		if !bytes.Contains(human, []byte(want)) {
			t.Fatalf("missing human conclusion %s: %s", want, human)
		}
	}
	for _, text := range []string{"SERIALIZER-OUTPUT", fixture.PrivatePrompt, fixture.PrivateCheckpoint, fixture.PrivateArchive} {
		if bytes.Contains(wire, []byte(text)) || bytes.Contains(human, []byte(text)) {
			t.Fatalf("metadata exposed %q", text)
		}
	}
	if !reflect.DeepEqual(before, fixture.Inventory(t, b.Workspace)) {
		t.Fatal("built CLI changed input tree or ambient state")
	}
}
