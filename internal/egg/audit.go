package egg

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// inputAuditor converts raw PTY input into readable text lines, handling
// backspace, escape sequences, and control characters. Output is timestamped
// and written to audit.log in the session directory.
type inputAuditor struct {
	buf        []byte
	file       *os.File
	mu         sync.Mutex
	escState   int // 0=normal, 1=got ESC, 2=in CSI sequence
	flushTimer *time.Timer
}

func newInputAuditor(path string) (*inputAuditor, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &inputAuditor{file: f}, nil
}

// Process takes raw input bytes, applies edits, writes completed lines.
func (a *inputAuditor) Process(input []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, b := range input {
		// Skip escape sequences (arrows, function keys, etc.)
		if a.escState > 0 {
			a.consumeEsc(b)
			continue
		}
		switch {
		case b == 0x1b: // ESC
			a.escState = 1
		case b == 0x0d || b == 0x0a: // Enter
			a.emitLine()
		case b == 0x7f || b == 0x08: // Backspace / Delete
			if len(a.buf) > 0 {
				a.buf = a.buf[:len(a.buf)-1]
			}
		case b == 0x09: // Tab
			a.buf = append(a.buf, '\t')
		case b == 0x03: // Ctrl+C
			a.buf = append(a.buf, '^', 'C')
			a.emitLine()
		case b == 0x04: // Ctrl+D
			a.buf = append(a.buf, '^', 'D')
			a.emitLine()
		case b >= 0x20: // Printable
			a.buf = append(a.buf, b)
		}
	}
	a.resetFlushTimer()
}

func (a *inputAuditor) emitLine() {
	line := string(a.buf)
	a.buf = a.buf[:0]
	ts := time.Now().UTC().Format(time.RFC3339)
	fmt.Fprintf(a.file, "%s\t%s\n", ts, line)
	if a.flushTimer != nil {
		a.flushTimer.Stop()
		a.flushTimer = nil
	}
}

// consumeEsc handles CSI sequences: ESC [ <params> <final byte 0x40-0x7E>
func (a *inputAuditor) consumeEsc(b byte) {
	switch a.escState {
	case 1: // got ESC, expecting [
		if b == '[' {
			a.escState = 2
		} else {
			a.escState = 0
		}
	case 2: // in CSI, waiting for final byte
		if b >= 0x40 && b <= 0x7E {
			a.escState = 0
		}
	}
}

// resetFlushTimer flushes partial line after 2s of idle.
func (a *inputAuditor) resetFlushTimer() {
	if a.flushTimer != nil {
		a.flushTimer.Stop()
	}
	a.flushTimer = time.AfterFunc(2*time.Second, func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if len(a.buf) > 0 {
			a.emitLine()
		}
	})
}

// Close flushes any remaining buffer and closes the file.
func (a *inputAuditor) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.flushTimer != nil {
		a.flushTimer.Stop()
	}
	if len(a.buf) > 0 {
		a.emitLine()
	}
	a.file.Close()
}
