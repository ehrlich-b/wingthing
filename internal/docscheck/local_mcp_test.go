package docscheck

import (
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Each entry bounds one independently readable recipe, including the second
// install/docs examples and both provider-specific recipe blocks. A prerequisite
// elsewhere on the page cannot satisfy it.
func TestLocalMCPEntryPrerequisites(t *testing.T) {
	const codex = "codex mcp add wingthing -- wt mcp stdio --client codex"
	const claude = "claude mcp add --scope user wingthing -- wt mcp stdio --client claude"
	for _, recipe := range []struct {
		name, path, begin, end string
		registrations          []string
	}{
		{"readme", "README.md", "## 1. Local agent control", "## 2.", []string{codex, claude}},
		{"skill", "SKILL.md", "1. **Local agent control:", "2. **Local human", []string{"wt mcp stdio --client NAME"}},
		{"pattern skill", "patterns/SKILL.md", "1. **Local agent control", "2. **Local sandboxed", []string{"wt mcp stdio"}},
		{"local subagents", "patterns/local-subagents/INSTRUCTIONS.md", "## Add Wingthing", "## Try it", []string{codex, claude}},
		{"home", "internal/relay/templates/home.html", `<h3 id="home-local-mcp">`, "<h3>Optional browser", []string{codex, claude}},
		{"install account-free", "internal/relay/templates/install.html", `<h3 id="account-free-mcp">`, "More options in", []string{codex, claude}},
		{"install route", "internal/relay/templates/install.html", `data-route="local-agent"`, `data-route="direct-remote"`, []string{codex, claude}},
		{"docs quickstart", "internal/relay/templates/docs.html", `data-route="local-agent"`, `data-route="direct-remote"`, []string{codex, claude}},
		{"docs agent-control", "internal/relay/templates/docs.html", `id="agent-control"`, `id="installation"`, []string{codex, claude}},
		{"pattern route", "internal/relay/templates/patterns.html", `data-route="local-agent"`, `data-route="direct-remote"`, []string{"wt mcp stdio"}},
		{"installer template", "internal/downloads/installer.sh.tmpl", "give your local Codex", "manage agents across machines", []string{codex, claude}},
		{"installer shell", "scripts/install.sh", "give your local Codex", "manage agents across machines", []string{codex, claude}},
		{"served installer", "internal/relay/install.sh", "give your local Codex", "manage agents across machines", []string{codex, claude}},
	} {
		t.Run(recipe.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(repositoryRoot(t), recipe.path))
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			start := strings.Index(text, recipe.begin)
			if start < 0 {
				t.Fatal("missing recipe beginning")
			}
			text = text[start:]
			end := strings.Index(text, recipe.end)
			if end < 0 {
				t.Fatal("missing recipe end")
			}
			text = text[:end]
			plain := html.UnescapeString(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(text, " "))
			plain = strings.Join(strings.Fields(plain), " ")
			startup := strings.Index(plain, "wt wing start --local-only")
			if startup < 0 {
				t.Fatal("recipe must positively start a local-only wing")
			}
			for _, registration := range recipe.registrations {
				index := strings.Index(plain, registration)
				if index < 0 || index <= startup {
					t.Errorf("start the wing before registration %q", registration)
				}
			}
			for _, fact := range []string{"running", "same", "WINGTHING_DIR"} {
				if !strings.Contains(plain, fact) {
					t.Errorf("recipe must explain the running wing and same state environment: missing %q", fact)
				}
			}
			for _, denied := range []string{"account or daemon", "account, daemon", "no daemon", "without a daemon"} {
				if strings.Contains(strings.ToLower(plain), denied) {
					t.Errorf("recipe denies the wing prerequisite: %q", denied)
				}
			}
			// A copied fenced provider recipe must also carry startup locally.
			for _, block := range regexp.MustCompile("(?s)```(?:sh|bash)\\n(.*?)```").FindAllStringSubmatch(text, -1) {
				if strings.Contains(block[1], "mcp add") && strings.Contains(block[1], "wt mcp stdio") {
					if index := strings.Index(block[1], "wt wing start --local-only"); index < 0 || index > strings.Index(block[1], "mcp add") {
						t.Error("copyable provider recipe omits startup before registration")
					}
				}
			}
		})
	}
}

func TestPublicSetupQualifications(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(repositoryRoot(t), path))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for _, path := range []string{"internal/relay/templates/install.html", "internal/relay/templates/docs.html"} {
		text := read(path)
		for _, route := range []struct{ name, prerequisite, command string }{
			{"self-hosted-browser", "no wingthing account", "wt roost start --https"},
			{"hosted-relay", "hosted_relay: allow", "wt login"},
		} {
			start := strings.Index(text, `data-route="`+route.name+`"`)
			if start < 0 {
				t.Fatalf("%s missing %s", path, route.name)
			}
			section := strings.ToLower(text[start:])
			prerequisite := strings.Index(section, route.prerequisite)
			command := strings.Index(section, route.command)
			if prerequisite < 0 || command < 0 || prerequisite > command {
				t.Errorf("%s must explain %s access before its command", path, route.name)
			}
		}
	}
	docs := read("internal/relay/templates/docs.html")
	if strings.Contains(docs, `href="/">sandbox builder`) {
		t.Error("docs link to a nonexistent homepage builder")
	}
	for _, phrase := range []string{"TCP via HTTP CONNECT", "route-less namespace", "UDP, ICMP or raw sockets", "network.local_ports"} {
		if !strings.Contains(docs, phrase) {
			t.Errorf("wildcard policy must explain %q", phrase)
		}
	}
	home := read("internal/relay/templates/home.html")
	for _, hook := range []string{"Your agents. Your direction.", "One place to direct the work", "Launch agents. <span>Join their terminals.", "Launch agents on your machines, follow their progress"} {
		if strings.Contains(home, hook) {
			t.Errorf("homepage retains promotional hook %q", hook)
		}
	}
	for _, path := range []string{"internal/relay/templates/home.html", "internal/relay/templates/install.html", "internal/relay/templates/docs.html"} {
		if !strings.Contains(read(path), `{{template "release-info" .}}`) {
			t.Errorf("%s must show feed-derived release information", path)
		}
	}
}
