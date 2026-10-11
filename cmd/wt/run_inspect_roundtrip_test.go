//go:build runinspectfixture

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/runinspect"
	"github.com/ehrlich-b/wingthing/internal/runinspect/fixture"
	"github.com/ehrlich-b/wingthing/internal/store"
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

func TestRunInspectRoundTripCLIContractInventory(t *testing.T) {
	binary, b := cliRoundTripFixture(t)
	b.Wing("z-running", "z-execution", "", "observing", "running", "")
	b.Egg("z-running", "z-execution", "running", "")
	b.Wing("a-interrupted", "a-execution", "", "terminal", "timeout", fixture.PrivateOutput)
	b.Egg("a-interrupted", "a-execution", "timeout", fixture.PrivateOutput)
	c := b.Conversation("logical", "a-execution", "")
	seq := b.Event(c, 1)
	b.Checkpoint(c.ID, 0, seq)
	b.Journal("a-execution", "request", c.ID, 0, seq)
	b.Archive("a-execution", "claude", "native-a-execution")
	parent := &store.Task{ID: "legacy-parent", Type: "agent_run", What: fixture.PrivatePrompt, Status: "done", Principal: "owner", RunAt: fixture.CapturedAt, CreatedAt: fixture.CapturedAt}
	if err := b.Store.CreateTask(parent); err != nil {
		t.Fatal(err)
	}
	child := &store.Task{ID: "legacy-child", Type: "agent_run", What: fixture.PrivatePrompt, Status: "done", Principal: "owner", RunAt: fixture.CapturedAt, CreatedAt: fixture.CapturedAt, ParentID: &parent.ID}
	if err := b.Store.CreateTask(child); err != nil {
		t.Fatal(err)
	}
	if err := b.Store.SetTaskOutput(child.ID, fixture.PrivateOutput); err != nil {
		t.Fatal(err)
	}
	b.SQL("PRAGMA foreign_keys=OFF")
	b.SQL("DELETE FROM tasks WHERE id=?", parent.ID)
	b.Seal()
	before := fixture.Inventory(t, b.Workspace)
	typed, err := runinspect.Inspect(t.Context(), b.Path, runinspect.Options{WorkspaceRoot: b.Workspace})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := runInspectBinary(t, binary, b.Workspace, "run-inspect", "--snapshot", b.Path, "--json")
	if err != nil {
		t.Fatalf("json: %v %s", err, wire)
	}
	var decoded runinspect.Report
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(typed, &decoded) {
		t.Fatal("typed and serialized Go results differ")
	}
	human, err := runInspectBinary(t, binary, b.Workspace, "run-inspect", "--snapshot", b.Path)
	if err != nil {
		t.Fatalf("human: %v %s", err, human)
	}
	var rendered bytes.Buffer
	if err := runinspect.RenderText(&rendered, typed); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(human, rendered.Bytes()) {
		t.Fatal("human CLI differs from typed renderer")
	}
	// Independently require every source/issue/action requirement in the human
	// view. Equality with the renderer alone would miss fields both omit.
	require := func(value string) {
		t.Helper()
		if !bytes.Contains(human, []byte(value)) {
			t.Fatalf("human contract omits %q: %s", value, human)
		}
	}
	refs := func(values []runinspect.Ref) {
		for _, ref := range values {
			require(fmt.Sprintf("%s %s %s", strconv.QuoteToASCII(ref.Path), strconv.QuoteToASCII(ref.Record), strconv.QuoteToASCII(ref.Field)))
		}
	}
	issues := func(values []runinspect.Issue) {
		for _, issue := range values {
			require("issue=" + issue.Code)
			for _, id := range issue.IDs {
				require(strconv.QuoteToASCII(id))
			}
			refs(issue.Sources)
		}
	}
	if len(decoded.Runs) != 2 || decoded.Runs[0].ID != "a-interrupted" || decoded.Runs[1].ID != "z-running" || bytes.Index(human, []byte(`Run "a-interrupted"`)) >= bytes.Index(human, []byte(`Run "z-running"`)) {
		t.Fatal("unstable run order/identity")
	}
	for _, run := range decoded.Runs {
		require(fmt.Sprintf("Run %s execution %s: %s", strconv.QuoteToASCII(run.ID), strconv.QuoteToASCII(run.SessionID), run.Diagnosis))
		refs(run.Sources)
		issues(run.Issues)
		for _, receipt := range run.Receipts {
			refs([]runinspect.Ref{receipt.Source})
		}
		for _, out := range run.Output {
			refs([]runinspect.Ref{out.Source})
		}
		for _, action := range run.Actions {
			require("suggested_action=" + action.Kind)
			refs(action.Basis)
			for _, observation := range action.Requires {
				require("requires: " + observation)
			}
		}
	}
	for _, task := range decoded.LegacyTasks {
		require(strconv.QuoteToASCII(task.ID))
		refs([]runinspect.Ref{task.Source, task.Output.Source})
		issues(task.Issues)
	}
	for _, conversation := range decoded.Conversations {
		require(strconv.QuoteToASCII(conversation.ID))
		require("checkpoint=" + conversation.Checkpoint.State)
		refs(conversation.Sources)
		refs([]runinspect.Ref{conversation.Checkpoint.Content.Source})
		issues(conversation.Issues)
	}
	for _, archive := range decoded.Archives {
		require(archive.State)
		refs(archive.Sources)
	}
	for _, attempt := range decoded.CheckpointAttempts {
		require(attempt.FenceState)
		require(attempt.Acknowledgement)
		refs([]runinspect.Ref{attempt.Source})
	}
	for _, edge := range decoded.Edges {
		require(fmt.Sprintf("Edge %s %s -> %s: %s", edge.Kind, strconv.QuoteToASCII(edge.From), strconv.QuoteToASCII(edge.To), edge.State))
		refs(edge.Sources)
	}
	issues(decoded.Issues)
	if !reflect.DeepEqual(before, fixture.Inventory(t, b.Workspace)) {
		t.Fatal("contract reads changed input/ambient tree")
	}
	// Retain concrete serialized contract artifacts outside the immutable input.
	for name, data := range map[string][]byte{"cli.json": wire, "cli.txt": human} {
		if err := os.WriteFile(filepath.Join(b.Workspace, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
