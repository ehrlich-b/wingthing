package relay

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
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
	homeTmpl  = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/home.html"))
	feedTmpl  = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/feed.html"))
	loginTmpl = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/login.html"))
)

type pageData struct {
	User *SocialUser
}

type feedItem struct {
	PostID       string
	Title        string
	Link         string
	Domain       string
	Anchors      []string
	CommentCount int
	Age          time.Time
}

type feedPageData struct {
	User       *SocialUser
	Slug       string
	SlugName   string
	Items      []feedItem
	AllAnchors []string
}

type loginPageData struct {
	User      *SocialUser
	Sent      bool
	HasGitHub bool
	HasGoogle bool
	HasSMTP   bool
}

// sidebarCache holds pre-computed anchor masses and connectivity with a TTL.
type sidebarCache struct {
	mu           sync.Mutex
	masses       map[string]float64
	connectivity map[string]map[string]int
	computedAt   time.Time
}

const sidebarTTL = 5 * time.Minute

// refresh recomputes if stale. Returns masses and connectivity.
func (c *sidebarCache) refresh(store *RelayStore) (map[string]float64, map[string]map[string]int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if time.Since(c.computedAt) < sidebarTTL && c.masses != nil {
		return c.masses, c.connectivity
	}

	masses, err := store.AnchorMasses()
	if err != nil {
		if c.masses != nil {
			return c.masses, c.connectivity
		}
		return nil, nil
	}
	conn, err := store.AnchorConnectivity()
	if err != nil {
		conn = nil
	}

	c.masses = masses
	c.connectivity = conn
	c.computedAt = time.Now()
	return masses, conn
}

// sidebarSlugs returns sorted slugs for the sidebar.
// /w/all: by mass descending. /w/{slug}: by connectivity*mass descending.
func (c *sidebarCache) sidebarSlugs(store *RelayStore, currentSlug string) []string {
	masses, conn := c.refresh(store)
	if masses == nil {
		return nil
	}

	slugs := make([]string, 0, len(masses))
	for slug := range masses {
		slugs = append(slugs, slug)
	}

	if currentSlug == "all" {
		sort.Slice(slugs, func(i, j int) bool {
			return masses[slugs[i]] > masses[slugs[j]]
		})
		return slugs
	}

	// For a specific space, rank by connectivity * mass
	edges := conn[currentSlug]
	sort.Slice(slugs, func(i, j int) bool {
		si := float64(edges[slugs[i]]) * masses[slugs[i]]
		sj := float64(edges[slugs[j]]) * masses[slugs[j]]
		return si > sj
	})
	return slugs
}

// Package-level sidebar cache shared by all requests.
var sidebar = &sidebarCache{}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	data := pageData{User: s.sessionUser(r)}
	homeTmpl.ExecuteTemplate(w, "base", data)
}

func (s *Server) handleSocial(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/w/all", http.StatusFound)
}

func (s *Server) handleAnchor(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		http.NotFound(w, r)
		return
	}

	var posts []*SocialEmbedding
	var err error

	if slug == "all" {
		posts, err = s.Store.ListAllPosts("best", 100)
	} else {
		anchor, aerr := s.Store.GetSocialEmbeddingBySlug(slug)
		if aerr != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if anchor == nil {
			http.NotFound(w, r)
			return
		}
		posts, err = s.Store.ListPostsByAnchor(anchor.ID, "best", 100)
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := s.buildFeedData(slug, posts, r)
	feedTmpl.ExecuteTemplate(w, "base", data)
}

func (s *Server) buildFeedData(slug string, posts []*SocialEmbedding, r *http.Request) feedPageData {
	postIDs := make([]string, len(posts))
	for i, p := range posts {
		postIDs[i] = p.ID
	}

	anchorSlugs, _ := s.Store.AnchorSlugsForPosts(postIDs)
	commentCounts, _ := s.Store.CommentCountsForPosts(postIDs)

	var items []feedItem
	for _, p := range posts {
		title := p.Text
		if p.Title != nil && *p.Title != "" {
			title = *p.Title
		}
		if len(title) > 200 {
			title = title[:200] + "..."
		}
		link := ""
		domain := ""
		if p.Link != nil {
			link = *p.Link
			if u, err := url.Parse(link); err == nil {
				domain = strings.TrimPrefix(u.Hostname(), "www.")
			}
		}
		age := p.CreatedAt
		if p.PublishedAt != nil {
			age = *p.PublishedAt
		}
		items = append(items, feedItem{
			PostID:       p.ID,
			Title:        title,
			Link:         link,
			Domain:       domain,
			Anchors:      anchorSlugs[p.ID],
			CommentCount: commentCounts[p.ID],
			Age:          age,
		})
	}

	allSlugs := sidebar.sidebarSlugs(s.Store, slug)

	slugName := "all"
	if slug != "all" {
		slugName = strings.ReplaceAll(slug, "-", " ")
	}

	return feedPageData{
		User:       s.sessionUser(r),
		Slug:       slug,
		SlugName:   slugName,
		Items:      items,
		AllAnchors: allSlugs,
	}
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
