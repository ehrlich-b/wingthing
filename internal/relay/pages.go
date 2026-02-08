package relay

import (
	"embed"
	"html/template"
	"net/http"
)

//go:embed templates
var templateFS embed.FS

var (
	homeTmpl   = template.Must(template.ParseFS(templateFS, "templates/base.html", "templates/home.html"))
	socialTmpl = template.Must(template.ParseFS(templateFS, "templates/base.html", "templates/social.html"))
	anchorTmpl = template.Must(template.ParseFS(templateFS, "templates/base.html", "templates/anchor.html"))
	loginTmpl  = template.Must(template.ParseFS(templateFS, "templates/base.html", "templates/login.html"))
)

type pageData struct {
	User *SocialUser
}

type anchorCard struct {
	Name      string
	Slug      string
	PostCount int
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

	var cards []anchorCard
	for _, a := range anchors {
		count, _ := s.Store.CountPostsByAnchor(a.ID)
		slug := ""
		if a.Slug != nil {
			slug = *a.Slug
		}
		cards = append(cards, anchorCard{
			Name:      a.Text,
			Slug:      slug,
			PostCount: count,
		})
	}

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

	posts, err := s.Store.ListPostsByAnchor(anchor.ID, "new", 50)
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
