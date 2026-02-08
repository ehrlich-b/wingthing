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
	Summary      string
	Link         string
	Domain       string
	Anchors      []string
	CommentCount int
	Age          time.Time
	Voted        bool
}

type feedPageData struct {
	User       *SocialUser
	LoggedIn   bool
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

// sidebarCache holds pre-computed anchor masses and connectivity.
// Refreshed by a background goroutine, never on page load.
type sidebarCache struct {
	mu           sync.RWMutex
	masses       map[string]float64
	connectivity map[string]map[string]int
}

// get returns a snapshot of the current cache under a read lock.
func (c *sidebarCache) get() (map[string]float64, map[string]map[string]int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.masses, c.connectivity
}

// update replaces the cache contents under a write lock.
func (c *sidebarCache) update(masses map[string]float64, conn map[string]map[string]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.masses = masses
	c.connectivity = conn
}

// sidebarSlugs returns sorted slugs for the sidebar.
// /w/all: by mass descending. /w/{slug}: by connectivity*mass descending.
func (c *sidebarCache) sidebarSlugs(currentSlug string) []string {
	masses, conn := c.get()
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

	// For a specific space: current slug first, then neighbors by connectivity * mass
	var rest []string
	for _, s := range slugs {
		if s != currentSlug {
			rest = append(rest, s)
		}
	}
	edges := conn[currentSlug]
	sort.Slice(rest, func(i, j int) bool {
		si := float64(edges[rest[i]]) * masses[rest[i]]
		sj := float64(edges[rest[j]]) * masses[rest[j]]
		return si > sj
	})
	return append([]string{currentSlug}, rest...)
}

// Package-level sidebar cache shared by all requests.
var sidebar = &sidebarCache{}

// StartSidebarRefresh launches a background goroutine that recomputes
// the sidebar cache every interval. Does an initial compute synchronously.
func StartSidebarRefresh(store *RelayStore, interval time.Duration) {
	refresh := func() {
		masses, _ := store.AnchorMasses()
		conn, _ := store.AnchorConnectivity()
		if masses != nil {
			sidebar.update(masses, conn)
		}
	}
	refresh() // initial sync compute
	go func() {
		for {
			time.Sleep(interval)
			refresh()
		}
	}()
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	data := pageData{User: s.sessionUser(r)}
	homeTmpl.ExecuteTemplate(w, "base", data)
}

// rankPostAnchors ranks anchor slugs for a post, pinning currentSlug first
// if present, then sorting rest by occurrence/connectivity. Returns at most limit slugs.
func rankPostAnchors(anchors []string, currentSlug string, limit int) []string {
	if len(anchors) == 0 {
		return nil
	}
	if len(anchors) <= limit {
		// If current slug is in the list, move it to front
		for i, a := range anchors {
			if a == currentSlug {
				result := make([]string, len(anchors))
				result[0] = currentSlug
				copy(result[1:], anchors[:i])
				copy(result[i+1:], anchors[i+1:])
				return result
			}
		}
		return anchors
	}

	// More anchors than limit: pin current if present, fill rest with first N-1
	var pinned bool
	var rest []string
	for _, a := range anchors {
		if a == currentSlug {
			pinned = true
		} else {
			rest = append(rest, a)
		}
	}

	if pinned {
		if len(rest) > limit-1 {
			rest = rest[:limit-1]
		}
		return append([]string{currentSlug}, rest...)
	}
	if len(rest) > limit {
		rest = rest[:limit]
	}
	return rest
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
	user := s.sessionUser(r)

	postIDs := make([]string, len(posts))
	for i, p := range posts {
		postIDs[i] = p.ID
	}

	anchorSlugs, _ := s.Store.AnchorSlugsForPosts(postIDs)
	commentCounts, _ := s.Store.CommentCountsForPosts(postIDs)

	var userVotes map[string]bool
	if user != nil {
		userVotes, _ = s.Store.UserUpvotesForPosts(user.ID, postIDs)
	}

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
		summary := p.Text
		if p.Title != nil && *p.Title != "" {
			// Summary is the compressed text, distinct from the title
			if len(summary) > 500 {
				summary = summary[:500] + "..."
			}
		} else {
			summary = ""
		}
		postAnchors := rankPostAnchors(anchorSlugs[p.ID], slug, 3)
		items = append(items, feedItem{
			PostID:       p.ID,
			Title:        title,
			Summary:      summary,
			Link:         link,
			Domain:       domain,
			Anchors:      postAnchors,
			CommentCount: commentCounts[p.ID],
			Age:          age,
			Voted:        userVotes[p.ID],
		})
	}

	allSlugs := sidebar.sidebarSlugs(slug)

	slugName := "all"
	if slug != "all" {
		slugName = strings.ReplaceAll(slug, "-", " ")
	}

	return feedPageData{
		User:       user,
		LoggedIn:   user != nil,
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
