package egg

import (
	"fmt"
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

const maxScrollbackLines = 50000 // ~50k lines, generous — real data the user wants

// VTerm wraps charmbracelet/x/vt with scrollback capture via ScrollOut callback.
// All methods are thread-safe. Callbacks fire inside Write, so mu is already held.
type VTerm struct {
	emu        *vt.Emulator
	scrollback []string // ring buffer of rendered lines scrolled off the top
	sbHead     int      // next write position in ring
	sbLen      int      // current count (≤ len(scrollback))

	mu           sync.Mutex
	altScreen    bool
	cursorHidden bool
	cols, rows   int
}

// NewVTerm creates a VTerm with the given dimensions.
func NewVTerm(cols, rows int) *VTerm {
	v := &VTerm{
		emu:        vt.NewEmulator(cols, rows),
		scrollback: make([]string, maxScrollbackLines),
		cols:       cols,
		rows:       rows,
	}
	v.emu.SetCallbacks(vt.Callbacks{
		ScrollOut: func(lines []uv.Line) {
			// mu already held by caller (Write)
			if v.altScreen {
				return
			}
			for _, line := range lines {
				rendered := line.Render()
				// Evict old entry if ring is full (release string for GC)
				if v.sbLen == len(v.scrollback) {
					v.scrollback[v.sbHead] = ""
				}
				v.scrollback[v.sbHead] = rendered
				v.sbHead = (v.sbHead + 1) % len(v.scrollback)
				if v.sbLen < len(v.scrollback) {
					v.sbLen++
				}
			}
		},
		ScrollbackClear: func() {
			// mu already held by caller (Write)
			for i := range v.scrollback {
				v.scrollback[i] = ""
			}
			v.sbLen = 0
			v.sbHead = 0
		},
		AltScreen: func(on bool) {
			// mu already held by caller (Write)
			v.altScreen = on
		},
		CursorVisibility: func(visible bool) {
			// mu already held by caller (Write)
			v.cursorHidden = !visible
		},
	})
	return v
}

// Write feeds PTY output to the emulator.
func (v *VTerm) Write(p []byte) (int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.emu.Write(p)
}

// Resize changes the terminal dimensions.
func (v *VTerm) Resize(cols, rows int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.emu.Resize(cols, rows)
	v.cols = cols
	v.rows = rows
}

// Snapshot generates a reconnect payload: scrollback + grid + cursor restore.
// The output is valid ANSI that any terminal emulator can consume directly.
func (v *VTerm) Snapshot() []byte {
	v.mu.Lock()
	defer v.mu.Unlock()

	var buf strings.Builder

	// Section 1: Scrollback lines (oldest-first from ring)
	lines := v.scrollbackLines()
	for _, line := range lines {
		buf.WriteString(line)
		buf.WriteString("\r\n")
	}

	// Section 2: Flush padding — push scrollback into xterm.js scrollback region.
	// rows-1 newlines push all remaining visible content off-screen.
	if len(lines) > 0 {
		for range v.rows - 1 {
			buf.WriteByte('\n')
		}
	}

	// Section 3: Reset styles + home cursor + grid repaint
	buf.WriteString("\x1b[m\x1b[H")
	buf.WriteString(v.emu.Render())

	// Section 4: Cursor position restore (1-based)
	pos := v.emu.CursorPosition()
	fmt.Fprintf(&buf, "\x1b[%d;%dH", pos.Y+1, pos.X+1)

	// Section 5: Cursor visibility restore
	if v.cursorHidden {
		buf.WriteString("\x1b[?25l")
	} else {
		buf.WriteString("\x1b[?25h")
	}

	return []byte(buf.String())
}

// ScrollbackLen returns the number of scrollback lines currently stored.
func (v *VTerm) ScrollbackLen() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.sbLen
}

// Close releases the emulator resources.
func (v *VTerm) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.emu.Close()
}

// scrollbackLines returns all scrollback lines oldest-first.
// Must be called with mu held.
func (v *VTerm) scrollbackLines() []string {
	if v.sbLen == 0 {
		return nil
	}
	lines := make([]string, v.sbLen)
	start := (v.sbHead - v.sbLen + len(v.scrollback)) % len(v.scrollback)
	for i := range v.sbLen {
		lines[i] = v.scrollback[(start+i)%len(v.scrollback)]
	}
	return lines
}
