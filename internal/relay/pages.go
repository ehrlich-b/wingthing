package relay

import (
	"embed"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

//go:embed templates
var templateFS embed.FS

var tmplFuncs = template.FuncMap{
	"deref": func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	},
	"slugDisplay": func(s string) string {
		return strings.ReplaceAll(s, "-", " ")
	},
	"stripMd": func(s string) string {
		s = regexp.MustCompile(`\*\*\[([^\]]*)\]\*\*`).ReplaceAllString(s, "$1")
		s = regexp.MustCompile(`\[([^\]]*)\]`).ReplaceAllString(s, "$1")
		s = strings.ReplaceAll(s, "**", "")
		return s
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
}

var (
	homeTmpl   = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/home.html"))
	socialTmpl = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/social.html"))
	anchorTmpl = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/anchor.html"))
	loginTmpl  = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/login.html"))
)

type pageData struct {
	User *SocialUser
}

type anchorCard struct {
	Name      string
	Slug      string
	PostCount int
	Mass      float64
	TopPost   string // text of highest-mass post
	TopLink   string // link domain of that post
}

type socialPageData struct {
	User    *SocialUser
	Anchors []anchorCard
}

type anchorPageData struct {
	User   *SocialUser
	Anchor *SocialEmbedding
	Posts  []*SocialEmbedding
}

type loginPageData struct {
	User      *SocialUser
	Sent      bool
	HasGitHub bool
	HasGoogle bool
	HasSMTP   bool
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	data := pageData{User: s.sessionUser(r)}
	homeTmpl.ExecuteTemplate(w, "base", data)
}

func (s *Server) handleSocial(w http.ResponseWriter, r *http.Request) {
	anchors, err := s.Store.ListAnchors()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	topPosts, _ := s.Store.TopPostsByAnchor()

	var cards []anchorCard
	for _, a := range anchors {
		count, _ := s.Store.CountPostsByAnchor(a.ID)
		mass, _ := s.Store.SumDecayedMassByAnchor(a.ID)
		slug := ""
		if a.Slug != nil {
			slug = *a.Slug
		}
		tp := topPosts[a.ID]
		text := tp.Text
		if len(text) > 140 {
			text = text[:140] + "..."
		}
		cards = append(cards, anchorCard{
			Name:      a.Text,
			Slug:      slug,
			PostCount: count,
			Mass:      mass,
			TopPost:   text,
			TopLink:   tp.Link,
		})
	}

	sort.Slice(cards, func(i, j int) bool {
		return cards[i].Mass > cards[j].Mass
	})

	data := socialPageData{
		User:    s.sessionUser(r),
		Anchors: cards,
	}
	socialTmpl.ExecuteTemplate(w, "base", data)
}

func (s *Server) handleAnchor(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		http.NotFound(w, r)
		return
	}

	anchor, err := s.Store.GetSocialEmbeddingBySlug(slug)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if anchor == nil {
		http.NotFound(w, r)
		return
	}

	posts, err := s.Store.ListPostsByAnchor(anchor.ID, "best", 50)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := anchorPageData{
		User:   s.sessionUser(r),
		Anchor: anchor,
		Posts:  posts,
	}
	anchorTmpl.ExecuteTemplate(w, "base", data)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	data := loginPageData{
		User:      s.sessionUser(r),
		Sent:      r.URL.Query().Get("sent") == "1",
		HasGitHub: s.Config.GitHubClientID != "",
		HasGoogle: s.Config.GoogleClientID != "",
		HasSMTP:   s.Config.SMTPHost != "",
	}
	loginTmpl.ExecuteTemplate(w, "base", data)
}
