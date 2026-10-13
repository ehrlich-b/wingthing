package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"io"
	"strings"
	"unicode"
)

// Snapshots are replayed into the existing terminal emulator; cursor moves,
// clipboard OSCs and terminal replies are never sent to the caller's terminal.
func writeTerminalSnapshot(out io.Writer, result map[string]any) error {
	const maxSnapshot = 4 << 20
	var data []byte
	if encoded, ok := result["base64"].(string); ok {
		if len(encoded) > base64.StdEncoding.EncodedLen(maxSnapshot) {
			return errors.New("terminal snapshot exceeds display limit")
		}
		var err error
		data, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return fmt.Errorf("decode terminal snapshot: %w", err)
		}
	} else if text, ok := result["ansi"].(string); ok {
		data = []byte(text)
	} else {
		return errors.New("terminal snapshot is unavailable")
	}
	if len(data) > maxSnapshot {
		return errors.New("terminal snapshot exceeds display limit")
	}
	terminal := egg.NewVTerm(120, 36)
	defer terminal.Close()
	if _, err := terminal.Write(data); err != nil {
		return err
	}
	text := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return '�'
		}
		return r
	}, terminal.ScreenText())
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	_, err := fmt.Fprintln(out, strings.TrimRight(strings.Join(lines, "\n"), "\n"))
	return err
}
