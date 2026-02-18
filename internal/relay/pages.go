package relay

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	homeTmpl        = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/home.html"))
	loginTmpl       = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/login.html"))
	skillsTmpl      = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/skills.html"))
	skillDetailTmpl = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/skill_detail.html"))
	docsTmpl        = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/docs.html"))
	termsTmpl       = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/terms.html"))
	privacyTmpl     = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/privacy.html"))
	abuseTmpl       = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/abuse.html"))
	installTmpl     = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/install.html"))
	claimTmpl       = template.Must(template.New("claim.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/claim.html"))
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
	User      *User
	LocalMode bool
	HeroVideo bool
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
	data := pageData{User: s.sessionUser(r), LocalMode: s.LocalMode, HeroVideo: s.Config.HeroVideo != ""}
	s.template(homeTmpl, "base.html", "home.html").ExecuteTemplate(w, "base", data)
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
	data := pageData{User: s.sessionUser(r), LocalMode: s.LocalMode}
	s.template(docsTmpl, "base.html", "docs.html").ExecuteTemplate(w, "base", data)
}

func (s *Server) handleTerms(w http.ResponseWriter, r *http.Request) {
	data := pageData{User: s.sessionUser(r), LocalMode: s.LocalMode}
	s.template(termsTmpl, "base.html", "terms.html").ExecuteTemplate(w, "base", data)
}

func (s *Server) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	data := pageData{User: s.sessionUser(r), LocalMode: s.LocalMode}
	s.template(privacyTmpl, "base.html", "privacy.html").ExecuteTemplate(w, "base", data)
}

func (s *Server) handleAbuse(w http.ResponseWriter, r *http.Request) {
	data := pageData{User: s.sessionUser(r), LocalMode: s.LocalMode}
	s.template(abuseTmpl, "base.html", "abuse.html").ExecuteTemplate(w, "base", data)
}

func (s *Server) handleInstallPage(w http.ResponseWriter, r *http.Request) {
	data := pageData{User: s.sessionUser(r), LocalMode: s.LocalMode}
	s.template(installTmpl, "base.html", "install.html").ExecuteTemplate(w, "base", data)
}

func (s *Server) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(installScript)
}

type skillsPageItem struct {
	Name        string
	Description string
	Category    string
	Publisher   string
	SourceURL   string
	Weight      int
}

type skillsPageData struct {
	User      *User
	LocalMode bool
	Skills    []skillsPageItem
}

func (s *Server) handleSkillsPage(w http.ResponseWriter, r *http.Request) {
	skills, err := s.Store.ListSkills("")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var items []skillsPageItem
	for _, sk := range skills {
		items = append(items, skillsPageItem{
			Name:        sk.Name,
			Description: sk.Description,
			Category:    sk.Category,
			Publisher:   sk.Publisher,
			SourceURL:   sk.SourceURL,
			Weight:      sk.Weight,
		})
	}
	data := skillsPageData{
		User:      s.sessionUser(r),
		LocalMode: s.LocalMode,
		Skills:    items,
	}
	s.template(skillsTmpl, "base.html", "skills.html").ExecuteTemplate(w, "base", data)
}

type skillDetailPageData struct {
	User        *User
	LocalMode   bool
	Name        string
	Description string
	Category    string
	Publisher   string
	SourceURL   string
	Content     string
	Tags        string
}

func (s *Server) handleSkillDetailPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.NotFound(w, r)
		return
	}

	sk, err := s.Store.GetSkill(name)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if sk == nil {
		http.NotFound(w, r)
		return
	}

	// External skills redirect to their source
	if sk.SourceURL != "" {
		http.Redirect(w, r, sk.SourceURL, http.StatusFound)
		return
	}

	data := skillDetailPageData{
		User:        s.sessionUser(r),
		LocalMode:   s.LocalMode,
		Name:        sk.Name,
		Description: sk.Description,
		Category:    sk.Category,
		Publisher:   sk.Publisher,
		SourceURL:   sk.SourceURL,
		Content:     sk.Content,
		Tags:        sk.Tags,
	}
	s.template(skillDetailTmpl, "base.html", "skill_detail.html").ExecuteTemplate(w, "base", data)
}

// isSafeRedirect returns true if dest is a relative path (no open redirect).
func isSafeRedirect(dest string) bool {
	if dest == "" {
		return false
	}
	// Block protocol-relative (//evil.com) and absolute URLs
	if strings.HasPrefix(dest, "//") || strings.Contains(dest, "://") {
		return false
	}
	// Must start with /
	return strings.HasPrefix(dest, "/")
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
		http.SetCookie(w, &http.Cookie{
			Name:     "oauth_next",
			Value:    next,
			Path:     "/auth",
			Domain:   s.cookieDomain(),
			MaxAge:   600,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	} else {
		// Clear stale oauth_next cookie from previous flows
		http.SetCookie(w, &http.Cookie{
			Name:   "oauth_next",
			Path:   "/auth",
			Domain: s.cookieDomain(),
			MaxAge: -1,
		})
	}
	data := loginPageData{
		User:      s.sessionUser(r),
		LocalMode: s.LocalMode,
		Sent:      r.URL.Query().Get("sent") == "1",
		HasGitHub: s.Config.GitHubClientID != "",
		HasGoogle: s.Config.GoogleClientID != "",
		HasSMTP:   s.Config.SMTPHost != "",
	}
	s.template(loginTmpl, "base.html", "login.html").ExecuteTemplate(w, "base", data)
}
