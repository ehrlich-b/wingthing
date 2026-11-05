package eggclient

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/parser"
)

// The terminal's save slots belong exclusively to the outer snapshot. Provider
// saves live in this separate map, including references to an unknown original
// value. Thus restoring a provider save never overwrites the guardian's fallback.
type terminalModeWriter struct {
	mu             sync.Mutex
	output         io.Writer
	parser         *ansi.Parser
	pending        []byte
	current, saved map[int]byte // 'h', 'l', or 'r' (the protected original)
}

func newTerminalModeWriter(output io.Writer, known map[int]int) *terminalModeWriter {
	w := &terminalModeWriter{output: output, parser: ansi.NewParser(), current: map[int]byte{}, saved: map[int]byte{}}
	w.parser.SetDataSize(0)
	for _, mode := range outerModes {
		w.current[mode] = 'r'
		if value := known[mode]; value != 0 {
			w.current[mode] = 'l'
			if value == 1 || value == 3 {
				w.current[mode] = 'h'
			}
		}
	}
	return w
}

func (w *terminalModeWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []byte
	for _, b := range data {
		before := w.parser.State()
		w.parser.Advance(b)
		after := w.parser.State()
		if b == 0x1b && len(w.pending) > 0 {
			out = append(out, w.pending...)
			w.pending = w.pending[:0]
		}
		if len(w.pending) == 0 && (after == parser.EscapeState || after == parser.CsiEntryState) && before != parser.Utf8State {
			w.pending = append(w.pending, b)
		} else if len(w.pending) > 0 {
			w.pending = append(w.pending, b)
		} else {
			out = append(out, b)
			continue
		}
		switch after {
		case parser.EscapeState, parser.CsiEntryState, parser.CsiParamState, parser.CsiIntermediateState:
			continue
		}
		out = append(out, w.transform(w.pending)...)
		w.pending = w.pending[:0]
	}
	if len(out) > 0 {
		n, err := w.output.Write(out)
		if err != nil {
			return 0, err
		}
		if n != len(out) {
			return 0, io.ErrShortWrite
		}
	}
	return len(data), nil
}

func (w *terminalModeWriter) transform(seq []byte) []byte {
	original := seq
	var clean, controls []byte
	for _, b := range seq {
		// DEL is ignored within CSI. It is neither a parameter byte nor an
		// executable control to replay, and must not bypass save protection.
		if b == 0x7f {
			continue
		}
		if b < 0x20 && b != 0x1b {
			controls = append(controls, b)
		} else {
			clean = append(clean, b)
		}
	}
	seq = clean
	start := 0
	if len(seq) >= 5 && string(seq[:3]) == "\x1b[?" {
		start = 3
	} else if len(seq) >= 4 && seq[0] == 0x9b && seq[1] == '?' {
		start = 2
	} else {
		return original
	}
	final := seq[len(seq)-1]
	if !strings.ContainsRune("hlsr", rune(final)) {
		return original
	}
	params := strings.Split(string(seq[start:len(seq)-1]), ";")
	// Leave non-mode CSI (including intermediates/subparameters) alone.
	for i, raw := range params {
		// An omitted CSI parameter is zero, including leading, interior and
		// trailing omissions. It must not bypass protection of the save slots.
		if raw == "" {
			raw = "0"
			params[i] = raw
		}
		if _, err := strconv.Atoi(raw); err != nil {
			return original
		}
	}
	out := controls
	changed := false
	for _, raw := range params {
		mode, _ := strconv.Atoi(raw)
		current, protected := w.current[mode]
		if !protected {
			out = fmt.Appendf(out, "\x1b[?%d%c", mode, final)
			continue
		}
		switch final {
		case 'h', 'l':
			w.setMode(mode, final)
		case 's':
			w.saved[mode] = current
			changed = true
		case 'r':
			if saved, ok := w.saved[mode]; ok {
				out = fmt.Appendf(out, "\x1b[?%d%c", mode, saved)
				w.setMode(mode, saved)
			}
			changed = true
		}
	}
	if changed {
		return out
	}
	return original
}

// The alternate selectors share a screen even when a provider mixes them.
func (w *terminalModeWriter) setMode(mode int, value byte) {
	w.current[mode] = value
	if mode == 47 || mode == 1047 || mode == 1049 {
		for _, alias := range []int{47, 1047, 1049} {
			w.current[alias] = value
		}
	}
}
