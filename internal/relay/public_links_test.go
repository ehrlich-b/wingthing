package relay

import (
	"io"
	"net/http"
	"os"
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
	for _, path := range []string{"/", "/docs", "/install", "/patterns", "/terms", "/privacy", "/abuse"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		page := string(body)
		start := strings.Index(page, `<nav class="site-nav">`)
		if resp.StatusCode != http.StatusOK || start < 0 {
			t.Fatalf("%s: status %d or missing site navigation", path, resp.StatusCode)
		}
		end := strings.Index(page[start:], "</nav>")
		if end < 0 {
			t.Fatal("missing site navigation end")
		}
		nav := page[start : start+end]
		for _, link := range []string{
			`href="https://gitlab.com/ehrlich-b/wingthing">GitLab source</a>`,
			`href="https://github.com/ehrlich-b/wingthing">GitHub mirror</a>`,
		} {
			if strings.Count(nav, link) != 1 {
				t.Errorf("%s: missing/duplicate source navigation link %s", path, link)
			}
		}
		if path == "/" && !strings.Contains(string(body), `href="/skill.md"`) {
			t.Error("homepage missing skill link")
		}
	}
}
