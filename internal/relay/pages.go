package relay

import (
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
	patternfiles "github.com/ehrlich-b/wingthing/patterns"
)

//go:embed templates
var templateFS embed.FS

//go:embed install.sh
var installScript []byte

var tmplFuncs = template.FuncMap{
	"deref": func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	},
	"clipDomain": func(raw string) string {
		if raw == "" {
			return ""
		}
		u, err := url.Parse(raw)
		if err != nil {
			return raw
		}
		host := u.Hostname()
		host = strings.TrimPrefix(host, "www.")
		return host
	},
	"timeAgo": func(t time.Time) string {
		d := time.Since(t)
		switch {
		case d < time.Minute:
			return "just now"
		case d < time.Hour:
			return fmt.Sprintf("%dm ago", int(d.Minutes()))
		case d < 24*time.Hour:
			return fmt.Sprintf("%dh ago", int(d.Hours()))
		case d < 30*24*time.Hour:
			return fmt.Sprintf("%dd ago", int(d.Hours()/24))
		default:
			return t.Format("Jan 2, 2006")
		}
	},
}

var (
	homeTmpl     = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/home.html"))
	loginTmpl    = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/login.html"))
	docsTmpl     = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/docs.html"))
	patternsTmpl = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/patterns.html"))
	termsTmpl    = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/terms.html"))
	privacyTmpl  = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/privacy.html"))
	abuseTmpl    = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/abuse.html"))
	installTmpl  = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/install.html"))
	claimTmpl    = template.Must(template.New("claim.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/claim.html"))
)

// template returns a parsed template. In dev mode (DevTemplateDir set),
// re-reads from disk on every call so edits show up without rebuild.
func (s *Server) template(cached *template.Template, files ...string) *template.Template {
	if s.DevTemplateDir == "" {
		return cached
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = filepath.Join(s.DevTemplateDir, f)
	}
	t, err := template.New(filepath.Base(files[0])).Funcs(tmplFuncs).ParseFiles(paths...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dev template reload error: %v\n", err)
		return cached
	}
	return t
}

type pageData struct {
	User                        *User
	LocalMode                   bool
	HeroVideo                   bool
	AppURL                      string
	SandboxBuilderAgents        []string
	SandboxBuilderAgentProfiles map[string]egg.AgentProfile
}

var sandboxBuilderAgents = []string{"claude", "codex", "cursor", "ollama", "gemini"}

func sandboxBuilderAgentProfiles() map[string]egg.AgentProfile {
	profiles := make(map[string]egg.AgentProfile, len(sandboxBuilderAgents))
	for _, agent := range sandboxBuilderAgents {
		profiles[agent] = egg.Profile(agent)
	}
	return profiles
}

type loginPageData struct {
	User      *User
	LocalMode bool
	Sent      bool
	HasGitHub bool
	HasGoogle bool
	HasSMTP   bool
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	// Local/roost mode: skip marketing page, go straight to the app
	if s.LocalMode || s.RoostMode {
		http.Redirect(w, r, "/app/", http.StatusSeeOther)
		return
	}
	data := pageData{
		User:                        s.sessionUser(r),
		LocalMode:                   s.LocalMode,
		HeroVideo:                   s.Config.HeroVideo != "",
		AppURL:                      s.appURL(),
		SandboxBuilderAgents:        sandboxBuilderAgents,
		SandboxBuilderAgentProfiles: sandboxBuilderAgentProfiles(),
	}
	s.executePageTemplate(w, s.template(homeTmpl, "base.html", "home.html"), data)
}

func (s *Server) handleHeroVideo(w http.ResponseWriter, r *http.Request) {
	if s.Config.HeroVideo == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, s.Config.HeroVideo)
}

func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	data := pageData{User: s.sessionUser(r), LocalMode: s.LocalMode, AppURL: s.appURL()}
	s.executePageTemplate(w, s.template(docsTmpl, "base.html", "docs.html"), data)
}

func (s *Server) handlePatterns(w http.ResponseWriter, r *http.Request) {
	data := pageData{User: s.sessionUser(r), LocalMode: s.LocalMode, AppURL: s.appURL()}
	s.executePageTemplate(w, s.template(patternsTmpl, "base.html", "patterns.html"), data)
}

func (s *Server) handlePatternSkill(w http.ResponseWriter, _ *http.Request) {
	s.servePatternMarkdown(w, "SKILL.md")
}

func (s *Server) handlePatternInstructions(w http.ResponseWriter, r *http.Request) {
	s.servePatternMarkdown(w, r.PathValue("slug")+"/INSTRUCTIONS.md")
}

func (s *Server) servePatternMarkdown(w http.ResponseWriter, name string) {
	data, err := patternfiles.Files.ReadFile(name)
	if err != nil {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(data)
}

func (s *Server) handleTerms(w http.ResponseWriter, r *http.Request) {
	data := pageData{User: s.sessionUser(r), LocalMode: s.LocalMode, AppURL: s.appURL()}
	s.executePageTemplate(w, s.template(termsTmpl, "base.html", "terms.html"), data)
}

func (s *Server) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	data := pageData{User: s.sessionUser(r), LocalMode: s.LocalMode, AppURL: s.appURL()}
	s.executePageTemplate(w, s.template(privacyTmpl, "base.html", "privacy.html"), data)
}

func (s *Server) handleAbuse(w http.ResponseWriter, r *http.Request) {
	data := pageData{User: s.sessionUser(r), LocalMode: s.LocalMode, AppURL: s.appURL()}
	s.executePageTemplate(w, s.template(abuseTmpl, "base.html", "abuse.html"), data)
}

func (s *Server) handleInstallPage(w http.ResponseWriter, r *http.Request) {
	data := pageData{User: s.sessionUser(r), LocalMode: s.LocalMode, AppURL: s.appURL()}
	s.executePageTemplate(w, s.template(installTmpl, "base.html", "install.html"), data)
}

func (s *Server) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(installScript)
}

// isSafeRedirect returns true if dest is a relative path (no open redirect).
func isSafeRedirect(dest string) bool {
	if dest == "" {
		return false
	}
	if strings.ContainsAny(dest, "\\\r\n") {
		return false
	}
	parsed, err := url.Parse(dest)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil {
		return false
	}
	// Inspect the decoded path as well as the raw spelling. Browsers normalize
	// encoded slashes and backslashes before navigation, so /%2f/example or
	// /%5cexample must not become a protocol-relative redirect after validation.
	return strings.HasPrefix(parsed.Path, "/") &&
		!strings.HasPrefix(parsed.Path, "//") &&
		!strings.Contains(parsed.Path, "\\")
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.LocalMode {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	next := r.URL.Query().Get("next")
	if next != "" && !isSafeRedirect(next) {
		next = ""
	}
	// Already logged in — skip login page and go to next (or home)
	if user := s.sessionUser(r); user != nil {
		dest := "/"
		if next != "" {
			dest = next
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	// Store next redirect in cookie so it survives OAuth round-trip
	if next != "" {
		s.setAuthFlowCookie(w, "oauth_next", next, "/auth", 600)
	} else {
		// Clear stale oauth_next cookie from previous flows
		s.expireAuthFlowCookie(w, "oauth_next", "/auth")
	}
	data := loginPageData{
		User:      s.sessionUser(r),
		LocalMode: s.LocalMode,
		Sent:      r.URL.Query().Get("sent") == "1",
		HasGitHub: s.Config.GitHubClientID != "",
		HasGoogle: s.Config.GoogleClientID != "",
		HasSMTP:   s.Config.SMTPHost != "",
	}
	s.executePageTemplate(w, s.template(loginTmpl, "base.html", "login.html"), data)
}

func (s *Server) executePageTemplate(w http.ResponseWriter, tmpl *template.Template, data any) {
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("render page template: %v", err)
	}
}
