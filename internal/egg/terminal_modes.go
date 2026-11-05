package egg

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// A CSI can span arbitrary PTY reads. Bound retained bytes, including malformed
// sequences, without letting a long mouse enable leak into the browser.
const maxTerminalEscape = 512

const (
	escapeGround = iota
	escapeStart
	escapeCSI
	escapeLongCSI
	escapeString
	escapeStringEnd
)

type terminalEscapeDecoder struct {
	state         int
	pending       []byte
	stringKind    byte
	utf8Remaining int
}

// C1 controls are recognized only outside a UTF-8 codepoint. For example,
// the 0x9c continuation byte in a Unicode title is not an end-of-string.
func (d *terminalEscapeDecoder) continuation(b byte) bool {
	if d.utf8Remaining > 0 && b >= 0x80 && b <= 0xbf {
		d.utf8Remaining--
		return true
	}
	d.utf8Remaining = 0
	switch {
	case b >= 0xc2 && b <= 0xdf:
		d.utf8Remaining = 1
	case b >= 0xe0 && b <= 0xef:
		d.utf8Remaining = 2
	case b >= 0xf0 && b <= 0xf4:
		d.utf8Remaining = 3
	}
	return false
}

// feed passes strings through without interpreting CSI-looking string payloads.
// Only complete CSI sequences reach transform; at most one bounded CSI is held.
func (d *terminalEscapeDecoder) feed(data []byte, transform func([]byte) []byte) []byte {
	out := make([]byte, 0, len(data))
	for _, b := range data {
		continuation := d.continuation(b)
		switch d.state {
		case escapeString, escapeStringEnd:
			out = append(out, b)
			if b == 0x18 || b == 0x1a || b == 0x9c && !continuation || b == 0x07 && d.stringKind == ']' || d.state == escapeStringEnd && b == '\\' {
				d.state = escapeGround
			} else if b == 0x1b {
				d.state = escapeStringEnd
			} else {
				d.state = escapeString
			}
		case escapeGround:
			if b == 0x1b {
				d.pending = append(d.pending[:0], b)
				d.state = escapeStart
			} else if b == 0x9b && !continuation {
				d.pending = append(d.pending[:0], b)
				d.state = escapeCSI
			} else if !continuation && (b == 0x90 || b == 0x98 || b == 0x9d || b == 0x9e || b == 0x9f) {
				out = append(out, b)
				d.state = escapeString
				d.stringKind = 'P'
				if b == 0x9d {
					d.stringKind = ']'
				}
			} else {
				out = append(out, b)
			}
		case escapeStart:
			if b == '[' {
				d.pending = append(d.pending, b)
				d.state = escapeCSI
			} else if strings.ContainsRune("]PX^_", rune(b)) {
				out = append(out, d.pending...)
				out = append(out, b)
				d.pending = d.pending[:0]
				d.stringKind = b
				d.state = escapeString
			} else if b == 0x1b {
				out = append(out, d.pending...)
				d.pending = append(d.pending[:0], b)
			} else {
				d.pending = append(d.pending, b)
				out = append(out, transform(d.pending)...)
				d.pending = d.pending[:0]
				d.state = escapeGround
			}
		case escapeCSI, escapeLongCSI:
			if b == 0x1b || b == 0x18 || b == 0x1a {
				out = append(out, d.pending...)
				d.pending = d.pending[:0]
				if b == 0x1b {
					d.pending = append(d.pending, b)
					d.state = escapeStart
				} else {
					out = append(out, b)
					d.state = escapeGround
				}
				continue
			}
			if d.state == escapeCSI {
				if len(d.pending) < maxTerminalEscape {
					d.pending = append(d.pending, b)
				} else {
					d.pending = d.pending[:0]
					d.state = escapeLongCSI
				}
			}
			if b >= 0x40 && b <= 0x7e {
				if d.state == escapeCSI {
					out = append(out, transform(d.pending)...)
				}
				// Oversized malformed CSI is discarded only in the browser projection.
				// The canonical/native stream never passes through this decoder.
				d.pending = d.pending[:0]
				d.state = escapeGround
			}
		}
	}
	return out
}

func privateModes(seq []byte) ([]string, byte, bool) {
	start := 0
	if len(seq) >= 5 && seq[0] == 0x1b && seq[1] == '[' && seq[2] == '?' {
		start = 3
	} else if len(seq) >= 4 && seq[0] == 0x9b && seq[1] == '?' {
		start = 2
	} else {
		return nil, 0, false
	}
	final := seq[len(seq)-1]
	if final != 'h' && final != 'l' {
		return nil, 0, false
	}
	// C0 controls may occur within CSI without cancelling it.
	params := make([]byte, 0, len(seq)-4)
	for _, b := range seq[start : len(seq)-1] {
		if b < 0x20 {
			continue
		}
		if b != ';' && (b < '0' || b > '9') {
			return nil, 0, false
		}
		params = append(params, b)
	}
	return strings.Split(string(params), ";"), final, true
}

func mouseMode(mode int) bool {
	switch mode {
	case 9, 1000, 1001, 1002, 1003, 1005, 1006, 1015, 1016:
		return true
	}
	return false
}

var mouseTrackingMarker = []byte("\x1b]7771;1\x07")

type browserPresentation struct{ decoder terminalEscapeDecoder }

func (p *browserPresentation) write(data []byte) []byte {
	return p.decoder.feed(data, func(seq []byte) []byte {
		params, final, ok := privateModes(seq)
		if !ok || final != 'h' {
			return seq
		}
		kept := make([]string, 0, len(params))
		removed := false
		for _, param := range params {
			mode, err := strconv.Atoi(param)
			if err == nil && mouseMode(mode) {
				removed = true
			} else {
				kept = append(kept, param)
			}
		}
		if !removed {
			return seq
		}
		var out []byte
		// Keep unrelated parameters in a combined DECSET, such as alternate screen.
		if len(kept) > 0 {
			out = []byte("\x1b[?" + strings.Join(kept, ";") + "h")
		}
		for _, b := range seq {
			if b < 0x20 && b != 0x1b {
				out = append(out, b)
			}
		}
		return append(out, mouseTrackingMarker...)
	})
}

// Finish a truncated escape at session exit, never in between live PTY reads.
func (p *browserPresentation) finish() []byte {
	out := append([]byte(nil), p.decoder.pending...)
	p.decoder.pending = p.decoder.pending[:0]
	return out
}

// Only presentation state is tracked: mouse encodings/reporting, alternate
// screen/scroll, application cursor keys, cursor visibility, focus reporting and bracketed paste.
var presentationModes = [...]int{1, 9, 25, 47, 1047, 1049, 1000, 1001, 1002, 1003, 1004, 1005, 1006, 1007, 1015, 1016, 2004}

type terminalModeState struct{ seen, enabled bool }
type terminalModes struct {
	decoder terminalEscapeDecoder
	states  [len(presentationModes)]terminalModeState
}

func (m *terminalModes) write(data []byte) {
	m.decoder.feed(data, func(seq []byte) []byte {
		if bytes.Equal(seq, []byte("\x1bc")) {
			for i, mode := range presentationModes {
				m.states[i] = terminalModeState{true, mode == 25}
			}
			return nil
		}
		params, final, ok := privateModes(seq)
		if ok {
			for _, param := range params {
				mode, err := strconv.Atoi(param)
				if err != nil {
					continue
				}
				for i, tracked := range presentationModes {
					if tracked == mode {
						m.states[i] = terminalModeState{true, final == 'h'}
					}
				}
				// Alternate screen selectors address the same screen. Restore just the
				// latest selector, so a stale enable cannot override a later disable.
				if mode == 47 || mode == 1047 || mode == 1049 {
					for i, tracked := range presentationModes {
						if tracked == 47 || tracked == 1047 || tracked == 1049 {
							if tracked != mode {
								m.states[i] = terminalModeState{true, false}
							}
						}
					}
				}
			}
		}
		return nil
	})
}

func alternateMode(mode int) bool { return mode == 47 || mode == 1047 || mode == 1049 }

func (m *terminalModes) restore() []byte { return m.restoreSelected(func(int) bool { return true }) }

func (m *terminalModes) restoreSelected(include func(int) bool) []byte {
	var out []byte
	// Disable before enabling, particularly when switching alternate selectors.
	for _, enabled := range []bool{false, true} {
		for i, mode := range presentationModes {
			state := m.states[i]
			if include(mode) && state.seen && state.enabled == enabled {
				final := 'l'
				if enabled {
					final = 'h'
				}
				out = fmt.Appendf(out, "\x1b[?%d%c", mode, final)
			}
		}
	}
	return out
}

// Inject restoration before any trailing incomplete CSI, preserving continuation
// across snapshot/live output. String payloads cannot accept mode sequences.
func (m *terminalModes) replaySnapshot(data []byte) []byte {
	// Repeating DECSET 1049 after replay can erase the just-replayed screen.
	// Raw history already carries its screen transitions; only a trimmed
	// replay needs an alternate-screen prefix, supplied by AttachmentSnapshot.
	restore := m.restoreSelected(func(mode int) bool { return !alternateMode(mode) })
	if m.decoder.state == escapeString || m.decoder.state == escapeStringEnd || m.decoder.state == escapeLongCSI {
		return append(restore, data...)
	}
	tail := m.decoder.pending
	split := len(data)
	if len(tail) > 0 && bytes.HasSuffix(data, tail) {
		split -= len(tail)
	}
	out := append([]byte(nil), data[:split]...)
	out = append(out, restore...)
	return append(out, data[split:]...)
}

// Native selection uses the terminal emulator's selection modifier (usually
// Shift or Option) while the provider has mouse tracking enabled. No keybindings,
// approval input or clipboard operations are installed by the attachment.
func NativeTerminalReset() []byte {
	var out []byte
	out = append(out, 0x18) // cancel an unfinished provider escape before cleanup
	for _, mode := range presentationModes {
		final := 'l'
		if mode == 25 {
			final = 'h'
		}
		out = fmt.Appendf(out, "\x1b[?%d%c", mode, final)
	}
	return out
}
