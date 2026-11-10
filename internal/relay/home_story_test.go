package relay

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func storyElements(root *html.Node, match func(*html.Node) bool) []*html.Node {
	var nodes []*html.Node
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && match(n) {
			nodes = append(nodes, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(root)
	return nodes
}

func storyAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func storyText(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(storyText(c))
	}
	return b.String()
}

func TestHomepageStoryHeroContract(t *testing.T) {
	for _, signedIn := range []bool{false, true} {
		name := "guest"
		data := pageData{AppURL: "https://app.example.test/"}
		if signedIn {
			name = "signed in"
			data.User = &User{ID: "person", DisplayName: "Person"}
		}
		t.Run(name, func(t *testing.T) {
			var b bytes.Buffer
			if err := homeTmpl.ExecuteTemplate(&b, "base", data); err != nil {
				t.Fatal(err)
			}
			page, err := html.Parse(&b)
			if err != nil {
				t.Fatal(err)
			}
			heroes := storyElements(page, func(n *html.Node) bool { return storyAttr(n, "class") == "hero" })
			if len(heroes) != 1 {
				t.Fatalf("hero count = %d, want 1", len(heroes))
			}
			hero := heroes[0]
			headings := storyElements(page, func(n *html.Node) bool { return n.Data == "h1" })
			if len(headings) != 1 || storyText(headings[0]) != "The agent manager for agents (and people)" {
				t.Fatal("page must have exactly one h1 with the approved headline")
			}
			if len(storyElements(hero, func(n *html.Node) bool { return n == headings[0] })) != 1 {
				t.Fatal("headline is outside the hero")
			}
			for _, href := range []string{"/docs"} {
				links := storyElements(hero, func(n *html.Node) bool { return n.Data == "a" && storyAttr(n, "href") == href })
				if len(links) != 1 || storyAttr(links[0], "class") != "hero-resource" {
					t.Errorf("hero must give %s the shared resource-link style", href)
				}
			}
			lines := storyElements(hero, func(n *html.Node) bool { return n.Data == "code" && storyAttr(n, "id") == "install-line" })
			if len(lines) != 1 || storyText(lines[0]) != "curl -fsSL https://wingthing.ai/install.sh | sh" {
				t.Fatal("hero must contain the exact copyable install line")
			}
			buttons := storyElements(hero, func(n *html.Node) bool {
				return n.Data == "button" && storyAttr(n, "aria-label") == "Copy install command"
			})
			if len(buttons) != 1 {
				t.Fatal("hero is missing its labelled copy button")
			}
			var titles []string
			for _, h := range storyElements(page, func(n *html.Node) bool { return n.Data == "h3" }) {
				titles = append(titles, storyText(h))
			}
			want := "Launch with context|Observe the work|Steer together|Come back to it|Stop when needed|Set the boundaries"
			if strings.Join(titles, "|") != want {
				t.Errorf("feature grid = %q, want %q", titles, want)
			}
			for role, state := range map[string]string{"migrate-sessions-table": "Working", "fix-login-redirect": "Needs you", "bump-playwright": "Done"} {
				rows := storyElements(page, func(n *html.Node) bool {
					return n.Data == "li" && storyAttr(n, "class") == "work-row" && strings.Contains(storyText(n), role)
				})
				if len(rows) != 1 {
					t.Fatalf("illustration needs one %s row", role)
				}
				states := storyElements(rows[0], func(n *html.Node) bool { return strings.HasPrefix(storyAttr(n, "class"), "work-state ") })
				if len(states) != 1 || storyText(states[0]) != state {
					t.Errorf("%s row must say %s", role, state)
				}
			}
			closing := storyElements(page, func(n *html.Node) bool { return storyAttr(n, "class") == "closing" })
			if len(closing) != 1 {
				t.Fatal("missing closing band")
			}
			closingHeadings := storyElements(closing[0], func(n *html.Node) bool { return n.Data == "h2" })
			if len(closingHeadings) != 1 || storyText(closingHeadings[0]) != "Agents can work. People can join." {
				t.Fatal("closing band must contain the approved invitation")
			}
		})
	}
}
