package relay

import (
	"bytes"
	"strings"
	"testing"
)

func TestSiteKingfisherTemplates(t *testing.T) {
	for _, page := range []struct {
		name   string
		render func(*bytes.Buffer) error
	}{
		{"home", func(b *bytes.Buffer) error {
			return homeTmpl.ExecuteTemplate(b, "base", pageData{SandboxBuilderAgents: sandboxBuilderAgents, SandboxBuilderAgentProfiles: sandboxBuilderAgentProfiles()})
		}},
		{"docs", func(b *bytes.Buffer) error { return docsTmpl.ExecuteTemplate(b, "base", pageData{}) }},
		{"install", func(b *bytes.Buffer) error { return installTmpl.ExecuteTemplate(b, "base", pageData{}) }},
	} {
		t.Run(page.name, func(t *testing.T) {
			var b bytes.Buffer
			if err := page.render(&b); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"#07131A", "#F4F8FA", "#0F1F28", "#FFFFFF", "#E6F1F6", "#0B1B24", "#8CA5B2", "#4E6571", "#2AA8FF", "#0069B8", "#FF8A3D", "#B4500C", "#36D69A", "#0D7553", "#FF5D6C", "#C42B3D", `class="logo-mark"`, `class="logo-wing">wing</span>thing.ai`} {
				if !strings.Contains(b.String(), want) {
					t.Errorf("missing %q", want)
				}
			}
		})
	}
}
