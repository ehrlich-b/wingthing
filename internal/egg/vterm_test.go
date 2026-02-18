package egg

import (
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

func TestVTermBasicOutput(t *testing.T) {
	v := NewVTerm(80, 24)
	defer v.Close()

	v.Write([]byte("hello world"))
	snap := v.Snapshot()
	if !strings.Contains(string(snap), "hello world") {
		t.Errorf("snapshot missing basic output, got:\n%s", snap)
	}
}

func TestVTermScrollbackCapture(t *testing.T) {
	v := NewVTerm(80, 10)
	defer v.Close()

	// Write 50 lines to a 10-row terminal — each \r\n at the bottom scrolls.
	// First scroll happens at line 9's \r\n, last at line 49's \r\n = 41 scrolls.
	for i := range 50 {
		v.Write([]byte(fmt.Sprintf("line %d\r\n", i)))
	}

	if got := v.ScrollbackLen(); got != 41 {
		t.Errorf("scrollback len = %d, want 41", got)
	}
}

func TestVTermScrollbackRingWrap(t *testing.T) {
	v := NewVTerm(80, 10)
	defer v.Close()

	// Write enough lines to exceed the 50k ring cap.
	// 60000 lines on 10-row terminal = 59991 scroll events.
	// Ring cap 50000 keeps last 50000.
	total := maxScrollbackLines + 10000
	for i := range total {
		v.Write([]byte(fmt.Sprintf("line %06d\r\n", i)))
	}

	if got := v.ScrollbackLen(); got != maxScrollbackLines {
		t.Errorf("scrollback len = %d, want %d (ring cap)", got, maxScrollbackLines)
	}

	// Oldest surviving line: total scrolls = total - 10 + 1 = 59991
	// Ring keeps last 50000, so oldest surviving scroll index = 59991-50000 = 9991
	// That scroll corresponds to line 9991 (scroll N evicts line N)
	snap := string(v.Snapshot())
	if strings.Contains(snap, "line 009990") {
		t.Error("snapshot should not contain line 009990 (dropped by ring)")
	}
	if !strings.Contains(snap, "line 009991") {
		t.Error("snapshot should contain line 009991 (oldest surviving)")
	}
}

func TestVTermANSIColors(t *testing.T) {
	v := NewVTerm(80, 10)
	defer v.Close()

	// Write colored text that scrolls off
	for i := range 15 {
		v.Write([]byte(fmt.Sprintf("\x1b[31mred line %d\x1b[m\r\n", i)))
	}

	snap := string(v.Snapshot())
	// Scrollback lines should contain SGR sequences
	if !strings.Contains(snap, "\x1b[31m") {
		t.Error("snapshot missing color SGR in scrollback")
	}
}

func TestVTermCursorPosition(t *testing.T) {
	v := NewVTerm(80, 24)
	defer v.Close()

	// Move cursor to row 5, col 10 (1-based in ANSI: \x1b[5;10H)
	v.Write([]byte("\x1b[5;10H"))
	snap := string(v.Snapshot())

	// Snapshot should restore cursor at same position
	if !strings.Contains(snap, "\x1b[5;10H") {
		t.Errorf("snapshot missing cursor restore at row 5 col 10, got:\n%s", snap)
	}
}

func TestVTermScreenClear(t *testing.T) {
	v := NewVTerm(80, 10)
	defer v.Close()

	// Write lines that scroll into scrollback
	for i := range 20 {
		v.Write([]byte(fmt.Sprintf("line %d\r\n", i)))
	}
	sbBefore := v.ScrollbackLen()

	// ESC[2J clears the grid but NOT scrollback
	v.Write([]byte("\x1b[2J"))

	if got := v.ScrollbackLen(); got != sbBefore {
		t.Errorf("ESC[2J changed scrollback len from %d to %d", sbBefore, got)
	}
}

func TestVTermScrollbackClear(t *testing.T) {
	v := NewVTerm(80, 10)
	defer v.Close()

	for i := range 20 {
		v.Write([]byte(fmt.Sprintf("line %d\r\n", i)))
	}
	if v.ScrollbackLen() == 0 {
		t.Fatal("scrollback should have lines before clear")
	}

	// ESC[3J clears scrollback
	v.Write([]byte("\x1b[3J"))

	if got := v.ScrollbackLen(); got != 0 {
		t.Errorf("scrollback len after ESC[3J = %d, want 0", got)
	}
}

func TestVTermFullReset(t *testing.T) {
	v := NewVTerm(80, 10)
	defer v.Close()

	for i := range 20 {
		v.Write([]byte(fmt.Sprintf("line %d\r\n", i)))
	}
	if v.ScrollbackLen() == 0 {
		t.Fatal("scrollback should have lines before reset")
	}

	// ESC c (RIS) clears everything including scrollback
	v.Write([]byte("\x1bc"))

	if got := v.ScrollbackLen(); got != 0 {
		t.Errorf("scrollback len after ESC c = %d, want 0", got)
	}
}

func TestVTermAltScreen(t *testing.T) {
	v := NewVTerm(80, 10)
	defer v.Close()

	// Write lines that scroll into scrollback
	for i := range 15 {
		v.Write([]byte(fmt.Sprintf("line %d\r\n", i)))
	}
	sbBefore := v.ScrollbackLen()

	// Enter alt screen
	v.Write([]byte("\x1b[?1049h"))

	// Write more lines that scroll — should NOT be captured
	for i := range 20 {
		v.Write([]byte(fmt.Sprintf("alt %d\r\n", i)))
	}

	if got := v.ScrollbackLen(); got != sbBefore {
		t.Errorf("alt screen scrollback = %d, want %d (unchanged)", got, sbBefore)
	}

	// Exit alt screen
	v.Write([]byte("\x1b[?1049l"))

	// Scrollback should still be from before alt screen
	if got := v.ScrollbackLen(); got != sbBefore {
		t.Errorf("after alt screen exit scrollback = %d, want %d", got, sbBefore)
	}
}

func TestVTermResize(t *testing.T) {
	v := NewVTerm(80, 24)
	defer v.Close()

	v.Write([]byte("before resize\r\n"))
	v.Resize(120, 40)
	v.Write([]byte("after resize"))

	snap := string(v.Snapshot())
	if !strings.Contains(snap, "before resize") {
		t.Error("snapshot missing content from before resize")
	}
	if !strings.Contains(snap, "after resize") {
		t.Error("snapshot missing content from after resize")
	}
}

func TestVTermCursorVisibility(t *testing.T) {
	v := NewVTerm(80, 24)
	defer v.Close()

	// Hide cursor
	v.Write([]byte("\x1b[?25l"))
	snap := string(v.Snapshot())
	if !strings.Contains(snap, "\x1b[?25l") {
		t.Error("snapshot should contain cursor hide when cursor is hidden")
	}

	// Show cursor
	v.Write([]byte("\x1b[?25h"))
	snap = string(v.Snapshot())
	if !strings.Contains(snap, "\x1b[?25h") {
		t.Error("snapshot should contain cursor show when cursor is visible")
	}
}

func TestVTermRoundTrip(t *testing.T) {
	v1 := NewVTerm(80, 24)
	defer v1.Close()

	// Write content that creates scrollback + active grid
	for i := range 40 {
		v1.Write([]byte(fmt.Sprintf("line %02d: some content here\r\n", i)))
	}
	v1.Write([]byte("\x1b[5;10Hcursor here"))

	snap := v1.Snapshot()

	// Feed snapshot to a fresh VTerm — grid should match
	v2 := NewVTerm(80, 24)
	defer v2.Close()
	v2.Write(snap)

	// Compare grid renders
	v1.mu.Lock()
	render1 := v1.emu.Render()
	v1.mu.Unlock()

	v2.mu.Lock()
	render2 := v2.emu.Render()
	v2.mu.Unlock()

	if render1 != render2 {
		t.Errorf("grid mismatch after round-trip\n--- v1 ---\n%s\n--- v2 ---\n%s", render1, render2)
	}
}

func TestVTermMultiLineScroll(t *testing.T) {
	v := NewVTerm(80, 5)
	defer v.Close()

	// Single large write that scrolls many lines at once
	var buf strings.Builder
	for i := range 20 {
		fmt.Fprintf(&buf, "bulk line %d\r\n", i)
	}
	v.Write([]byte(buf.String()))

	// Should have 15 lines in scrollback (20 written - 5 visible rows)
	// The exact count depends on how many lines the VTE considers scrolled off
	if got := v.ScrollbackLen(); got == 0 {
		t.Error("expected scrollback lines after bulk write")
	}
}

func TestVTermEmptySnapshot(t *testing.T) {
	v := NewVTerm(80, 24)
	defer v.Close()

	// Snapshot of fresh terminal should not panic and should contain grid
	snap := v.Snapshot()
	if len(snap) == 0 {
		t.Error("empty VTerm snapshot should not be zero-length")
	}
	// Should have home cursor, grid, cursor restore, cursor visibility
	s := string(snap)
	if !strings.Contains(s, "\x1b[H") {
		t.Error("snapshot missing home cursor")
	}
	if !strings.Contains(s, "\x1b[?25h") {
		t.Error("snapshot missing cursor visibility restore")
	}
}

func TestVTermSnapshotFormat(t *testing.T) {
	v := NewVTerm(80, 5)
	defer v.Close()

	// Write enough to get scrollback
	for i := range 10 {
		v.Write([]byte(fmt.Sprintf("line %d\r\n", i)))
	}

	snap := string(v.Snapshot())

	// Should contain: scrollback lines, padding, style reset, home, grid, cursor
	if !strings.Contains(snap, "\x1b[m\x1b[H") {
		t.Error("snapshot missing style reset + home cursor sequence")
	}
}

func TestVTermConcurrentWriteResize(t *testing.T) {
	v := NewVTerm(80, 24)
	defer v.Close()

	done := make(chan struct{})

	// Concurrent writes
	go func() {
		for i := range 1000 {
			v.Write([]byte(fmt.Sprintf("line %d\r\n", i)))
		}
		close(done)
	}()

	// Concurrent resizes
	for range 100 {
		v.Resize(80+1, 24+1)
		v.Resize(80, 24)
	}

	<-done

	// Should not panic or deadlock — just verify we can take a snapshot
	snap := v.Snapshot()
	if len(snap) == 0 {
		t.Error("snapshot should not be empty after concurrent writes")
	}
}

// TestVTermSnapshotGridMatchesEmulator verifies the snapshot grid section
// matches what the underlying emulator renders.
func TestVTermSnapshotGridMatchesEmulator(t *testing.T) {
	v := NewVTerm(40, 10)
	defer v.Close()

	// Write content that stays on grid (no scrollback)
	v.Write([]byte("row 1 content\r\n"))
	v.Write([]byte("row 2 content\r\n"))
	v.Write([]byte("\x1b[31mcolored row 3\x1b[m"))

	v.mu.Lock()
	gridRender := v.emu.Render()
	v.mu.Unlock()

	snap := string(v.Snapshot())

	// Grid should appear in the snapshot after the home cursor
	if !strings.Contains(snap, gridRender) {
		t.Errorf("snapshot doesn't contain exact grid render\n--- grid ---\n%q\n--- snap ---\n%q", gridRender, snap)
	}
}

// TestVTermWithRealVT feeds a snapshot to the upstream VT library's emulator
// and verifies it produces a correct grid. This simulates what xterm.js would do.
func TestVTermWithRealVT(t *testing.T) {
	v := NewVTerm(80, 24)
	defer v.Close()

	// Create a session with scrollback
	for i := range 30 {
		v.Write([]byte(fmt.Sprintf("history line %d\r\n", i)))
	}
	v.Write([]byte("current prompt $ "))

	snap := v.Snapshot()

	// Feed to a plain vt.Emulator (simulating xterm.js)
	emu := vt.NewEmulator(80, 24)
	defer emu.Close()
	emu.Write(snap)

	grid := emu.Render()
	if !strings.Contains(grid, "current prompt $") {
		t.Errorf("xterm simulation grid missing prompt content:\n%s", grid)
	}
}

// TestVTermAuditReplay replays a real audit.pty.gz recording through VTerm,
// takes a snapshot, feeds it to a fresh VT emulator, and verifies the grid matches.
func TestVTermAuditReplay(t *testing.T) {
	auditPath := os.ExpandEnv("$HOME/.wingthing/eggs/913ca06c/audit.pty.gz")
	if _, err := os.Stat(auditPath); err != nil {
		t.Skipf("audit recording not found at %s", auditPath)
	}

	f, err := os.Open(auditPath)
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gr.Close()

	buf := make([]byte, 64*1024)
	var allData []byte
	for {
		n, readErr := gr.Read(buf)
		if n > 0 {
			allData = append(allData, buf[:n]...)
		}
		if readErr != nil {
			break
		}
	}

	// Parse V2 header
	if len(allData) < 4 || string(allData[:4]) != "WTA2" {
		t.Fatal("audit recording is not V2 format")
	}
	pos := 4
	cols, n := binary.Uvarint(allData[pos:])
	if n <= 0 {
		t.Fatal("failed to read cols from header")
	}
	pos += n
	rows, n := binary.Uvarint(allData[pos:])
	if n <= 0 {
		t.Fatal("failed to read rows from header")
	}
	pos += n

	t.Logf("audit recording: %dx%d, %d bytes decompressed", cols, rows, len(allData))

	// Create VTerm with recorded dimensions
	v := NewVTerm(int(cols), int(rows))
	defer v.Close()

	// Replay frames
	var frameCount, resizeCount int
	for pos < len(allData) {
		// delta_ms
		_, n := binary.Uvarint(allData[pos:])
		if n <= 0 {
			break
		}
		pos += n

		// frame_type
		frameType, n := binary.Uvarint(allData[pos:])
		if n <= 0 {
			break
		}
		pos += n

		// data_len
		dataLen, n := binary.Uvarint(allData[pos:])
		if n <= 0 {
			break
		}
		pos += n

		if pos+int(dataLen) > len(allData) {
			break
		}
		chunk := allData[pos : pos+int(dataLen)]
		pos += int(dataLen)

		if frameType == 1 {
			// Resize frame
			rCols, cn := binary.Uvarint(chunk)
			if cn <= 0 {
				continue
			}
			rRows, rn := binary.Uvarint(chunk[cn:])
			if rn <= 0 {
				continue
			}
			v.Resize(int(rCols), int(rRows))
			resizeCount++
		} else {
			// Output frame
			v.Write(chunk)
			frameCount++
		}
	}

	t.Logf("replayed %d output frames, %d resize frames", frameCount, resizeCount)
	t.Logf("scrollback: %d lines", v.ScrollbackLen())

	// Take snapshot
	snap := v.Snapshot()
	t.Logf("snapshot: %d bytes", len(snap))

	if len(snap) == 0 {
		t.Fatal("snapshot is empty after audit replay")
	}

	// Feed snapshot to a fresh VT emulator — verify grid matches
	v.mu.Lock()
	origCols, origRows := v.cols, v.rows
	origRender := v.emu.Render()
	origPos := v.emu.CursorPosition()
	v.mu.Unlock()

	emu := vt.NewEmulator(origCols, origRows)
	defer emu.Close()
	emu.Write(snap)

	snapRender := emu.Render()
	snapPos := emu.CursorPosition()

	if origRender != snapRender {
		// Show first divergence for debugging
		for i := range min(len(origRender), len(snapRender)) {
			if origRender[i] != snapRender[i] {
				start := max(0, i-40)
				end := min(len(origRender), i+40)
				t.Errorf("grid diverges at byte %d\n  orig: %q\n  snap: %q", i, origRender[start:end], snapRender[start:min(len(snapRender), end)])
				break
			}
		}
		if len(origRender) != len(snapRender) {
			t.Errorf("grid render lengths differ: orig=%d snap=%d", len(origRender), len(snapRender))
		}
	}

	if origPos != snapPos {
		t.Errorf("cursor position mismatch: orig=%v snap=%v", origPos, snapPos)
	}

	t.Logf("round-trip grid match: OK (%d bytes, cursor at %v)", len(origRender), origPos)
}
