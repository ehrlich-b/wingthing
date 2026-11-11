package relay

import (
	"io/fs"
	"os"
	"regexp"
	"testing"
)

var bannedSiteCopy = []*regexp.Regexp{
	regexp.MustCompile(`(?i)Your agents\. Your direction\.`),
	regexp.MustCompile(`(?i)\byour\s+\w+\.\s+your\s+\w+\.`),
	regexp.MustCompile(`(?i)\b(supercharge|seamless|unleash|empower|effortless|game-changer|next-level)\b`),
}

func TestSiteCopy(t *testing.T) {
	templates := os.DirFS("templates")
	files := 0
	err := fs.WalkDir(templates, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(templates, path)
		if err != nil {
			return err
		}
		files++
		for _, banned := range bannedSiteCopy {
			for _, match := range banned.FindAll(content, -1) {
				t.Errorf("templates/%s: banned site copy %q", path, match)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("no template files checked")
	}
	t.Logf("checked all %d template files", files)
}

func TestSiteCopyPatterns(t *testing.T) {
	for _, text := range []string{
		"Your agents. Your direction.", "yOuR code. YOUR rules.", "Your work.\nYour way.",
		"Supercharge", "SEAMLESS", "Unleash", "Empower", "Effortless", "GAME-CHANGER", "Next-Level",
	} {
		t.Run(text, func(t *testing.T) {
			for _, banned := range bannedSiteCopy {
				if banned.MatchString(text) {
					return
				}
			}
			t.Errorf("copy guard did not reject %q", text)
		})
	}
	for _, text := range []string{
		"Launch agents on your machines.", "Your agents run here. Your code stays here.", "empowerment",
	} {
		for _, banned := range bannedSiteCopy {
			if banned.MatchString(text) {
				t.Errorf("copy guard rejected plain copy %q", text)
			}
		}
	}
}
