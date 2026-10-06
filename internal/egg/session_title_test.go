package egg

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanSessionTitle(t *testing.T) {
	cases := map[string]string{
		"◐ Math question":              "Math question",
		"✳ Arithmetic calculation":     "Arithmetic calculation",
		"✳ Claude Code":                "",
		"codex":                        "",
		"  SLIDE-1234 refund   check":  "SLIDE-1234 refund check",
		"<img src=x onerror=alert(1)>": "img src=x onerror=alert(1)",
		"case \"42\" & 'quotes'":       "case 42 quotes",
		"\x1b[31mred\x07":              "31mred",
		"":                             "",
	}
	for raw, want := range cases {
		if got := CleanSessionTitle(raw); got != want {
			t.Errorf("CleanSessionTitle(%q) = %q, want %q", raw, got, want)
		}
	}
	long := CleanSessionTitle("Investigate a very long support case title that keeps going well past the sidebar width")
	if n := len([]rune(long)); n > maxSessionTitleRunes {
		t.Errorf("title has %d runes, want at most %d", n, maxSessionTitleRunes)
	}
}

func TestVTermReportsTitleChangesOnce(t *testing.T) {
	h := newAsyncVTermHarness(80, 24, 256)
	defer h.close()
	titles := make(chan string, 8)
	h.vterm.OnTitle(func(title string) { titles <- title })

	// A title split across PTY reads, the next spinner frame, then Claude's idle
	// frame, whose ✳ the parser truncates at its 0x9C byte, then a new topic.
	h.writeAndEnqueue([]byte("\x1b]0;◐ Math qu"))
	h.writeAndEnqueue([]byte("estion\x07"))
	h.writeAndEnqueue([]byte("\x1b]0;◑ Math question\x07"))
	h.writeAndEnqueue([]byte("\x1b]0;✳ Math question\x07"))
	h.writeAndEnqueue([]byte("\x1b]2;◐ Refund lookup\x07"))
	h.fence()

	var got []string
	for {
		select {
		case title := <-titles:
			got = append(got, title)
			continue
		case <-time.After(50 * time.Millisecond):
		}
		break
	}
	if len(got) != 2 || got[0] != "Math question" || got[1] != "Refund lookup" {
		t.Fatalf("title changes = %q, want [Math question Refund lookup]", got)
	}
}

func TestSessionTitleFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if got := ReadSessionTitle(dir); got != "" {
		t.Fatalf("missing title = %q, want empty", got)
	}
	if err := WriteSessionTitle(dir, "Math question"); err != nil {
		t.Fatal(err)
	}
	if got := ReadSessionTitle(dir); got != "Math question" {
		t.Fatalf("title = %q, want Math question", got)
	}
	// A tampered file is cleaned on read like a live title.
	if err := os.WriteFile(filepath.Join(dir, SessionTitleFile), []byte("✳ <b>bold</b>\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadSessionTitle(dir); got != "bbold/b" {
		t.Fatalf("tampered title = %q, want bbold/b", got)
	}
	if err := WriteSessionTitle(dir, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, SessionTitleFile)); !os.IsNotExist(err) {
		t.Fatalf("empty title left the file behind: %v", err)
	}
}
