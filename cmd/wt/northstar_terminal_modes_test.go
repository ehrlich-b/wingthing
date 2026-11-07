package main

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"
)

// The screen emulator does not implement XTSAVE/XTRESTORE. Model DEC private
// modes independently and translate restores for its renderer, while preserving
// the original transcript. The parser retains state across PTY read boundaries
// and handles cancellation of an incomplete provider CSI before cleanup.
type northstarTerminalModes struct {
	parser         *ansi.Parser
	current, saved map[int]bool
	output         []byte
}

func newNorthstarTerminalModes(initial map[int]bool) *northstarTerminalModes {
	m := &northstarTerminalModes{parser: ansi.NewParser(), current: map[int]bool{}, saved: map[int]bool{}}
	for _, mode := range fidelityTrackedModes {
		m.current[mode] = mode == 25
	}
	for mode, enabled := range initial {
		m.current[mode] = enabled
	}
	m.parser.SetHandler(ansi.Handler{HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
		if cmd.Prefix() != '?' || cmd.Intermediate() != 0 {
			return
		}
		for _, param := range params {
			mode := param.Param(0)
			if _, tracked := m.current[mode]; !tracked {
				continue
			}
			switch cmd.Final() {
			case 'h', 'l':
				m.current[mode] = cmd.Final() == 'h'
			case 's':
				m.saved[mode] = m.current[mode]
			case 'r':
				if enabled, ok := m.saved[mode]; ok {
					m.current[mode] = enabled
					final := 'l'
					if enabled {
						final = 'h'
					}
					m.output = fmt.Appendf(m.output, "\x1b[?%d%c", mode, final)
				}
			}
		}
	}})
	return m
}

func (m *northstarTerminalModes) render(data []byte) []byte {
	m.output = m.output[:0]
	for _, b := range data {
		m.output = append(m.output, b)
		m.parser.Advance(b)
	}
	return m.output
}

func (m *northstarTerminalModes) snapshot() map[int]bool {
	copy := make(map[int]bool, len(m.current))
	for mode, enabled := range m.current {
		copy[mode] = enabled
	}
	return copy
}
