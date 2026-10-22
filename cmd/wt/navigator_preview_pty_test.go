//go:build !windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func TestNavigatorPreviewPTY(t *testing.T) {
	f := northstarNewFixture(t)
	if root := os.Getenv("WT_NAVIGATOR_EVIDENCE"); root != "" {
		if !filepath.IsAbs(root) {
			t.Fatal("capture directory must be absolute")
		}
		f.evidence = root
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	binary := northstarBuild(t, f, "stable")
	northstarWrite(t, filepath.Join(f.local, "remotes.yaml"), "remotes: {}\n", 0600)
	eggs := []*northstarEgg{}
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("pv%02d", i)
		e := northstarStandaloneEgg(t, f, f.local, id, fmt.Sprintf("task-%02d-investigate-long-running-deployment-and-verify-rollback", i), "working", fmt.Sprintf("OUTPUT_FOR_%02d", i))
		// Shells and older providers must have output previews too.
		northstarWrite(t, filepath.Join(e.dir, "lifecycle.jsonl"), "", 0600)
		eggs = append(eggs, e)
	}
	h := northstarStartPTY(t, f, binary, 160, 44)
	for i := 0; i < 8; i++ {
		after := uint64(0)
		if i > 0 {
			after = h.send("j")
		}
		want := []string{"8 sessions", fmt.Sprintf("OUTPUT_FOR_%02d", i), "OUTPUT · read only"}
		for j := 0; j < 8; j++ {
			want = append(want, fmt.Sprintf("task %02d", j))
		}
		screen := h.expect(fmt.Sprintf("selected output %d and all eight rows", i), after, want...)
		northstarWrite(t, filepath.Join(f.evidence, fmt.Sprintf("pty-160x44-selected-%02d.txt", i)), screen, 0600)
	}
	// Rename and refresh while the same immutable full ID stays selected.
	if err := eggclient.WriteSessionName(eggs[7].dir, "renamed-session-with-another-long-display-name"); err != nil {
		t.Fatal(err)
	}
	h.expect("rename preserves output", h.send("R"), "renamed session", "OUTPUT_FOR_07")
	h.expect("compact navigator", h.resize(80, 44), "renamed session", "v read only")
	h.expect("existing read-only viewer", h.send("v"), "OUTPUT_FOR_07")
	screen := h.expect("single q returns to selected session", h.send("q"), "8 sessions", "renamed session", "Enter open")
	northstarWrite(t, filepath.Join(f.evidence, "pty-80x44-return.txt"), screen, 0600)
	// A second open proves the return retained the original full ID.
	h.expect("reopen retains identity", h.send("v"), "OUTPUT_FOR_07")
	h.expect("second single-key return", h.send("q"), "8 sessions", "renamed session")
	h.exit("q")
	records := []map[string]any{}
	for i, e := range eggs {
		if e.previewReads.Load() == 0 || e.previewMutations.Load() != 0 || e.killed.Load() != 0 {
			t.Fatalf("session %d: reads=%d mutations=%d kills=%d", i, e.previewReads.Load(), e.previewMutations.Load(), e.killed.Load())
		}
		records = append(records, map[string]any{"session": filepath.Base(e.dir), "read_only_attachments": e.previewReads.Load(), "mutation_calls": e.previewMutations.Load(), "kills": e.killed.Load()})
	}
	wire, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(f.evidence, "pty-transport.json"), string(wire), 0600)
	t.Log("eight mocked egg transports: zero input, resize, claim, takeover or kill calls")
	if strings.Contains(screen, "OUTPUT_FOR_00") {
		t.Fatal("return changed target")
	}
}
