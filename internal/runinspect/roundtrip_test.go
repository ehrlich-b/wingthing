//go:build runinspectfixture

package runinspect_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/runinspect"
	"github.com/ehrlich-b/wingthing/internal/runinspect/fixture"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func newRoundTrip(t *testing.T) *fixture.Builder {
	t.Helper()
	root := os.Getenv("WT_INSPECT_FIXTURE_ROOT")
	if root == "" {
		t.Fatal("WT_INSPECT_FIXTURE_ROOT required; run make test-run-inspect-roundtrip")
	}
	workspace := filepath.Join(testsocket.Dir(t), ".scratch")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	return fixture.New(t, filepath.Join(workspace, "snapshot"))
}
func inspectRoundTrip(t *testing.T, b *fixture.Builder, preview int) *runinspect.Report {
	t.Helper()
	before := fixture.Inventory(t, b.Path)
	r, err := runinspect.Inspect(t.Context(), b.Path, runinspect.Options{WorkspaceRoot: b.Workspace, PreviewBytes: preview})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, fixture.Inventory(t, b.Path)) {
		t.Fatal("inspection changed source inventory/mtime")
	}
	return r
}
func roundTripRun(t *testing.T, r *runinspect.Report, id string) runinspect.Run {
	t.Helper()
	for _, run := range r.Runs {
		if run.ID == id {
			return run
		}
	}
	t.Fatalf("run %s absent", id)
	return runinspect.Run{}
}
func roundTripAction(r runinspect.Run, kind string) bool {
	for _, a := range r.Actions {
		if a.Kind == kind {
			return true
		}
	}
	return false
}

func TestRunInspectRoundTripBuilderDestination(t *testing.T) {
	b := newRoundTrip(t)
	if second, err := fixture.Open(b.Path); err == nil {
		_ = second.Store.Close()
		t.Fatal("replaced existing snapshot destination")
	}
	for _, path := range []string{"", "snapshot", b.Workspace + "/../outside"} {
		if second, err := fixture.Open(path); err == nil {
			_ = second.Store.Close()
			t.Fatalf("accepted invalid destination %q", path)
		}
	}
	b.Seal()
	before := fixture.Inventory(t, b.Path)
	if err := fixture.WriteManifest(b.Path); err == nil {
		t.Fatal("replaced immutable manifest")
	}
	if !reflect.DeepEqual(before, fixture.Inventory(t, b.Path)) {
		t.Fatal("manifest refusal changed fixture")
	}
}

func TestRunInspectRoundTripTransitions(t *testing.T) {
	// Expectations come from RunTurnResult.Terminal and the wing acknowledgement
	// transition, never from a report generated during fixture construction.
	for _, tc := range []struct{ name, phase, wing, egg, diagnosis, ack string }{
		{"completed", "terminal", "done", "done", "completed_with_output", "terminal_in_wing"},
		{"lost_acknowledgement", "submitting", "pending", "done", "completed_with_output", "terminal_receipt_not_in_wing"},
		{"interrupted", "terminal", "timeout", "timeout", "timeout", "terminal_in_wing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newRoundTrip(t)
			b.Wing("run", "execution", "", tc.phase, tc.wing, fixture.PrivateOutput)
			b.Egg("run", "execution", tc.egg, fixture.PrivateOutput)
			b.Seal()
			r := inspectRoundTrip(t, b, 0)
			run := roundTripRun(t, r, "run")
			if len(r.Runs) != 1 || run.SessionID != "execution" || run.Diagnosis != tc.diagnosis || run.Acknowledgement != tc.ack || run.EvidenceState != "consistent" || len(run.Receipts) != 2 {
				t.Fatalf("transition: %+v", run)
			}
			for _, output := range run.Output {
				if output.Available && output.Bytes != int64(len(fixture.PrivateOutput)) {
					t.Fatalf("output: %+v", output)
				}
			}
			wire, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(wire, []byte("SERIALIZER-OUTPUT")) || bytes.Contains(wire, []byte(fixture.PrivatePrompt)) {
				t.Fatal("metadata exposed content")
			}
			if roundTripAction(run, "manual_resume_eligible") {
				t.Fatal("missing archive/checkpoint recommends resume")
			}
		})
	}
}
