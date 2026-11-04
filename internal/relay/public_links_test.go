package relay

import (
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestPublicSkillAndLinks(t *testing.T) {
	_, ts := testServer(t)
	skill, err := os.ReadFile("../../SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/skill.md", "/SKILL.md"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || string(body) != string(skill) || resp.Header.Get("Content-Type") != "text/markdown; charset=utf-8" {
			t.Errorf("%s: status %d or content differs", path, resp.StatusCode)
		}
	}
	for _, path := range []string{"/", "/docs"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || regexp.MustCompile(`(?i)href\s*=\s*["'][^"']*github\.com`).Match(body) {
			t.Errorf("%s: status %d or GitHub link", path, resp.StatusCode)
		}
		if path == "/" && !strings.Contains(string(body), `href="/skill.md"`) {
			t.Error("homepage missing skill link")
		}
	}
}
