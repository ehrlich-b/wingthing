//go:build runinspectfixture

package runinspect_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/runinspect"
	"github.com/ehrlich-b/wingthing/internal/runinspect/fixture"
)

func TestRunInspectRoundTripRepeatedInspectionAndPreview(t *testing.T) {
	for _, text := range []string{"Ω🙂\n", "\x1b[31m\n\x00\u202e", "tiny\t\r\b", ""} {
		t.Run(fmt.Sprintf("bytes_%d", len(text)), func(t *testing.T) {
			b := newRoundTrip(t)
			b.Wing("run", "execution", "", "terminal", "done", text)
			b.Egg("run", "execution", "done", text)
			c := b.Conversation("logical", "execution", "")
			seq := b.Event(c, 1)
			b.Checkpoint(c.ID, 0, seq)
			b.Archive("execution", "claude", "native-execution")
			b.Seal()
			before := fixture.Inventory(t, b.Workspace)
			first := inspectRoundTrip(t, b, 0)
			for _, limit := range []int{0, 1, 2, 3, 5, 4096} {
				report := inspectRoundTrip(t, b, limit)
				wire, err := json.Marshal(report)
				if err != nil {
					t.Fatal(err)
				}
				var human bytes.Buffer
				if err := runinspect.RenderText(&human, report); err != nil {
					t.Fatal(err)
				}
				run := roundTripRun(t, report, "run")
				if len(run.Output) != 2 {
					t.Fatal("receipt/output inventory missing")
				}
				for _, output := range run.Output {
					count := min(limit, len(text))
					wantPreview := ""
					if limit > 0 {
						wantPreview = strconv.QuoteToASCII(text[:count])
					}
					if output.Bytes != int64(len(text)) || output.OmittedBytes != int64(len(text)-count) || output.Preview != wantPreview {
						t.Fatalf("byte-exact preview: %+v want %q", output, wantPreview)
					}
					if strings.ContainsAny(output.Preview, "\x1b\x00\u202e\n\r\b") {
						t.Fatal("raw control character in preview")
					}
				}
				if limit == 0 {
					if !reflect.DeepEqual(first, report) {
						t.Fatal("repeated metadata report differs")
					}
					if text != "" && (bytes.Contains(wire, []byte(strconv.Quote(text))) || strings.Contains(human.String(), strconv.QuoteToASCII(text))) {
						t.Fatal("metadata-only report exposes output")
					}
				}
				if bytes.Contains(wire, []byte(fixture.PrivateCheckpoint)) || bytes.Contains(wire, []byte(fixture.PrivateArchive)) || strings.Contains(human.String(), fixture.PrivateCheckpoint) || strings.Contains(human.String(), fixture.PrivateArchive) {
					t.Fatal("preview opt-in exposed checkpoint/archive content")
				}
			}
			if !reflect.DeepEqual(before, fixture.Inventory(t, b.Workspace)) {
				t.Fatal("repeated inspections changed files/sidecars/mtimes")
			}
		})
	}
}

func TestRunInspectRoundTripCompareOneAcknowledgedEvent(t *testing.T) {
	// Two independently generated snapshots contain identical receipts/events.
	// The after snapshot performs one additional successful checkpoint CAS.
	build := func(acknowledge bool) (*fixture.Builder, *runinspect.Report) {
		b, c, seq := interruptedRoundTrip(t)
		next := b.Event(c, 2)
		if next != seq+1 {
			t.Fatal("fixture event sequence is not deterministic")
		}
		if acknowledge {
			b.Checkpoint(c.ID, 1, next)
		}
		durable, err := b.Store.GetConversation("owner", c.ID)
		if err != nil {
			t.Fatal(err)
		}
		expectedRevision := int64(1)
		expectedCursor := seq
		if acknowledge {
			expectedRevision = 2
			expectedCursor = next
		}
		if durable.Revision != expectedRevision || durable.DeliveredCursor != expectedCursor {
			t.Fatalf("durable transition: %+v", durable)
		}
		b.Seal()
		return b, inspectRoundTrip(t, b, 0)
	}
	beforeBuilder, before := build(false)
	afterBuilder, after := build(true)
	beforeInventory := fixture.Inventory(t, beforeBuilder.Path)
	afterInventory := fixture.Inventory(t, afterBuilder.Path)
	changed := []string{}
	for path, stamp := range beforeInventory {
		other, ok := afterInventory[path]
		if !ok {
			t.Fatalf("missing after artifact %s", path)
		}
		if stamp.SHA256 != other.SHA256 {
			changed = append(changed, path)
		}
	}
	// Only DB bytes and their completed manifest hash change. Provider captures
	// are deliberately identical and never borrowed across the two inputs.
	sort.Strings(changed)
	if !reflect.DeepEqual(changed, []string{"snapshot.json", "wt.db"}) {
		t.Fatalf("unexpected durable artifact differences: %v", changed)
	}
	cpBefore, cpAfter := before.Conversations[0].Checkpoint, after.Conversations[0].Checkpoint
	if cpBefore.State != "unacknowledged_events" || cpAfter.State != "current_at_snapshot" || *cpBefore.Revision != 1 || *cpAfter.Revision != 2 || *cpBefore.AcknowledgedCursor != 1 || *cpAfter.AcknowledgedCursor != 2 || *cpBefore.EventHead != 2 || *cpAfter.EventHead != 2 || *cpBefore.PendingEvents != 1 || *cpAfter.PendingEvents != 0 {
		t.Fatalf("checkpoint difference: before=%+v after=%+v", cpBefore, cpAfter)
	}
	if roundTripAction(before.Runs[0], "manual_resume_eligible") || !roundTripAction(after.Runs[0], "manual_resume_eligible") {
		t.Fatal("acknowledgement did not change conditional resume eligibility")
	}
	// Normalize precisely those expected differences, then compare every other
	// report field. This is a test consumer, not a product comparison dashboard.
	for i := range after.InputFiles {
		if after.InputFiles[i].Path == "wt.db" {
			after.InputFiles[i] = before.InputFiles[i]
		}
	}
	after.Conversations[0].Checkpoint = cpBefore
	after.Runs[0].Actions = before.Runs[0].Actions
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("unexpected report differences after one ACK: before=%+v after=%+v", before, after)
	}
	if err := os.WriteFile(filepath.Join(afterBuilder.Workspace, "comparison.json"), []byte(`{"changed_artifacts":["snapshot.json","wt.db"],"acknowledged_events":1}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}
