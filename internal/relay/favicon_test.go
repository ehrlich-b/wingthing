package relay

import (
	"bytes"
	"encoding/binary"
	"html/template"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func assertPageIcons(t *testing.T, body string) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"icon":             "/app/icons/wt.svg",
		"apple-touch-icon": "/app/icons/wt-180.png",
		"manifest":         "/app/manifest.json",
	}
	counts := make(map[string]int)
	var visit func(*html.Node, bool)
	visit = func(n *html.Node, inHead bool) {
		inHead = inHead || n.Type == html.ElementNode && n.Data == "head"
		if inHead && n.Type == html.ElementNode && n.Data == "link" {
			attrs := make(map[string]string)
			for _, attr := range n.Attr {
				attrs[attr.Key] = attr.Val
			}
			if href, ok := want[attrs["rel"]]; ok {
				counts[attrs["rel"]]++
				if attrs["href"] != href {
					t.Errorf("%s href = %q, want %q", attrs["rel"], attrs["href"], href)
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child, inHead)
		}
	}
	visit(doc, false)
	for rel := range want {
		if counts[rel] != 1 {
			t.Errorf("head has %d %s links, want 1", counts[rel], rel)
		}
	}
	if strings.Contains(body, "%!") {
		t.Error("page contains a formatting error")
	}
}

func TestPageTemplatesIncludeIcons(t *testing.T) {
	pages := map[string]struct {
		tmpl *template.Template
		data any
	}{
		"home.html":     {homeTmpl, pageData{SandboxBuilderAgentProfiles: sandboxBuilderAgentProfiles()}},
		"login.html":    {loginTmpl, loginPageData{}},
		"docs.html":     {docsTmpl, pageData{}},
		"patterns.html": {patternsTmpl, pageData{}},
		"terms.html":    {termsTmpl, pageData{}},
		"privacy.html":  {privacyTmpl, pageData{}},
		"abuse.html":    {abuseTmpl, pageData{}},
		"install.html":  {installTmpl, pageData{}},
		"claim.html":    {claimTmpl, struct{ UserCode, Error string }{UserCode: "TEST"}},
	}
	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		// The base layout is exercised by every content page; icons is a fragment.
		if name == "base.html" || name == "icons.html" {
			continue
		}
		page, ok := pages[name]
		if !ok {
			t.Errorf("template %s has no head coverage", name)
			continue
		}
		for _, mode := range []string{"embedded", "dev"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				s := &Server{}
				if mode == "dev" {
					s.DevTemplateDir = "templates"
				}
				files, root := []string{"base.html", name}, "base"
				if name == "claim.html" {
					files, root = []string{name}, name
				}
				var body bytes.Buffer
				if err := s.template(page.tmpl, files...).ExecuteTemplate(&body, root, page.data); err != nil {
					t.Fatal(err)
				}
				assertPageIcons(t, body.String())
			})
		}
	}
}

func TestInlinePagesIncludeIcons(t *testing.T) {
	s := &Server{Store: testStore(t)}
	inv, org := &OrgInvite{}, &Org{Name: "Example"}
	pages := map[string]func(http.ResponseWriter){
		"invite-status": func(w http.ResponseWriter) { s.renderInviteStatusPage(w, inv, org) },
		"invite-accept": func(w http.ResponseWriter) { s.renderInviteAcceptPage(w, inv, org) },
		"invite-login":  func(w http.ResponseWriter) { s.renderInviteLoginPage(w, inv, org) },
		"invite-error":  func(w http.ResponseWriter) { s.renderInviteErrorPage(w, "a@example.test", "b@example.test") },
		"oauth-consent": func(w http.ResponseWriter) {
			if err := mcpConsentTmpl.Execute(w, mcpConsentData{}); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, render := range pages {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			render(w)
			assertPageIcons(t, w.Body.String())
		})
	}
}

func TestFaviconAndPageIconAssets(t *testing.T) {
	s := NewServer(testStore(t), ServerConfig{AppHost: "app.example.test"})
	for _, host := range []string{"site.example.test", "app.example.test"} {
		for path, contentType := range map[string]string{
			"/favicon.ico":          "image/",
			"/app/icons/wt.svg":     "image/svg+xml",
			"/app/icons/wt-180.png": "image/png",
			"/app/manifest.json":    "application/json",
		} {
			t.Run(host+path, func(t *testing.T) {
				w := httptest.NewRecorder()
				s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://"+host+path, nil))
				if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), contentType) {
					t.Fatalf("GET = %d, %s", w.Code, w.Header().Get("Content-Type"))
				}
				if path == "/favicon.ico" {
					assertICO(t, w.Body.Bytes())
				}
				if strings.HasSuffix(path, ".png") {
					config, err := png.DecodeConfig(w.Body)
					if err != nil || config.Width != 180 || config.Height != 180 {
						t.Fatalf("apple icon = %+v, %v", config, err)
					}
				}
			})
		}
	}
}

func assertICO(t *testing.T, data []byte) {
	t.Helper()
	if len(data) < 86 || !bytes.Equal(data[:6], []byte{0, 0, 1, 0, 5, 0}) {
		t.Fatal("expected ICO header with five images")
	}
	for i, size := range []int{16, 29, 32, 48, 64} {
		entry := data[6+i*16 : 22+i*16]
		length := uint64(binary.LittleEndian.Uint32(entry[8:12]))
		offset := uint64(binary.LittleEndian.Uint32(entry[12:16]))
		if int(entry[0]) != size || int(entry[1]) != size || offset+length > uint64(len(data)) {
			t.Fatalf("invalid %d px ICO directory entry", size)
		}
		img, err := png.Decode(bytes.NewReader(data[offset : offset+length]))
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dx() != size || img.Bounds().Dy() != size {
			t.Fatalf("ICO frame bounds = %v, want %d px", img.Bounds(), size)
		}
	}
}
