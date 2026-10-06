package egg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// SessionTitleFile holds the latest title the agent set for its terminal.
const SessionTitleFile = "session.title"

const maxSessionTitleRunes = 60

// Program names agents set before they have a topic say nothing about the session.
var genericSessionTitles = map[string]bool{"claude code": true, "claude": true, "codex": true}

// CleanSessionTitle turns an OSC terminal title into a display label: it drops
// the leading spinner or status glyph (Claude uses ✳, ◐ and ◑), keeps letters,
// digits and plain punctuation only, and returns "" for generic program names.
func CleanSessionTitle(raw string) string {
	var b strings.Builder
	started, space := false, false
	for _, r := range raw {
		keep := unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || strings.ContainsRune(".,:;-_/#()+?!@%=", r)
		if !started && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			continue
		}
		started = true
		if !keep {
			space = space || unicode.IsSpace(r)
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	title := []rune(b.String())
	if len(title) > maxSessionTitleRunes {
		title = title[:maxSessionTitleRunes]
	}
	cleaned := strings.TrimSpace(string(title))
	if genericSessionTitles[strings.ToLower(cleaned)] {
		return ""
	}
	return cleaned
}

// WriteSessionTitle replaces the egg's title file atomically; "" removes it.
func WriteSessionTitle(dir, title string) error {
	path := filepath.Join(dir, SessionTitleFile)
	if title == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	tmp, err := os.CreateTemp(dir, ".session-title-*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(title + "\n"); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// ReadSessionTitle returns the saved title, cleaned again on the way out.
func ReadSessionTitle(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, SessionTitleFile))
	if err != nil {
		return ""
	}
	return CleanSessionTitle(string(data))
}
