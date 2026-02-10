package relay

import (
	"embed"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
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
	homeTmpl   = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/home.html"))
	feedTmpl   = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/feed.html"))
	postTmpl   = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/post.html"))
	loginTmpl  = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/login.html"))
	skillsTmpl     = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/skills.html"))
	skillDetailTmpl = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/skill_detail.html"))
	selfhostTmpl    = template.Must(template.New("base.html").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/selfhost.html"))
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
	User        *SocialUser
	LoggedIn    bool
	Slug        string
	SlugName    string
	Description string
	Sort        string
	Items       []feedItem
	AllAnchors  []string
}

type postComment struct {
	Content   string
	IsBot     bool
	CreatedAt time.Time
}

type postPageData struct {
	User     *SocialUser
	LoggedIn bool
	PostID   string
	Title    string
	Summary  string
	Link     string
	Domain   string
	Anchors  []string
	Age      time.Time
	Voted    bool
	Comments []postComment
}

type loginPageData struct {
	User      *SocialUser
	Sent      bool
	HasGitHub bool
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
	s.template(homeTmpl, "base.html", "home.html").ExecuteTemplate(w, "base", data)
}

func (s *Server) handleSelfHost(w http.ResponseWriter, r *http.Request) {
	data := pageData{User: s.sessionUser(r)}
	s.template(selfhostTmpl, "base.html", "selfhost.html").ExecuteTemplate(w, "base", data)
}

func (s *Server) handleInstallPage(w http.ResponseWriter, r *http.Request) {
	data := pageData{User: s.sessionUser(r)}
	s.template(installTmpl, "base.html", "install.html").ExecuteTemplate(w, "base", data)
}

func (s *Server) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(installScript)
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

// validSort normalizes the sort query param.
func validSort(s string) string {
	switch s {
	case "new", "week", "month", "year":
		return s
	default:
		return "hot"
	}
}

func (s *Server) handleAnchor(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		http.NotFound(w, r)
		return
	}

	sort := validSort(r.URL.Query().Get("sort"))

	var posts []*SocialEmbedding
	var err error

	var description string
	if slug == "all" {
		posts, err = s.Store.ListAllPosts(sort, 100)
		description = "Front page of the agentic internet"
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
		posts, err = s.Store.ListPostsByAnchor(anchor.ID, sort, 100)
		description = anchor.Text
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := s.buildFeedData(slug, posts, r)
	data.Sort = sort
	data.Description = description
	s.template(feedTmpl, "base.html", "feed.html").ExecuteTemplate(w, "base", data)
}

func (s *Server) handlePostPage(w http.ResponseWriter, r *http.Request) {
	postID := r.PathValue("postID")
	if postID == "" {
		http.NotFound(w, r)
		return
	}

	post, err := s.Store.GetSocialEmbedding(postID)
	if err != nil || post == nil {
		http.NotFound(w, r)
		return
	}

	title, summary := extractTitleSummary(post)
	link := ""
	domain := ""
	if post.Link != nil {
		link = *post.Link
		if u, err := url.Parse(link); err == nil {
			domain = strings.TrimPrefix(u.Hostname(), "www.")
		}
	}
	age := post.CreatedAt
	if post.PublishedAt != nil {
		age = *post.PublishedAt
	}

	anchors, _ := s.Store.AnchorSlugsForPosts([]string{postID})
	comments, _ := s.Store.ListCommentsByPost(postID)

	user := s.sessionUser(r)
	var voted bool
	if user != nil {
		votes, _ := s.Store.UserUpvotesForPosts(user.ID, []string{postID})
		voted = votes[postID]
	}

	var postComments []postComment
	for _, c := range comments {
		postComments = append(postComments, postComment{
			Content:   c.Content,
			IsBot:     c.IsBot,
			CreatedAt: c.CreatedAt,
		})
	}

	data := postPageData{
		User:     user,
		LoggedIn: user != nil,
		PostID:   postID,
		Title:    title,
		Summary:  summary,
		Link:     link,
		Domain:   domain,
		Anchors:  anchors[postID],
		Age:      age,
		Voted:    voted,
		Comments: postComments,
	}
	s.template(postTmpl, "base.html", "post.html").ExecuteTemplate(w, "base", data)
}

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// stripHTMLTitle removes HTML tags and decodes entities from RSS titles.
func stripHTMLTitle(s string) string {
	s = htmlTagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.TrimSpace(s)
}

// extractTitleSummary derives a title and summary from a post.
// Priority: explicit Title field > [Bracketed Title] in text > first sentence of text.
func extractTitleSummary(p *SocialEmbedding) (title, summary string) {
	if p.Title != nil && *p.Title != "" && len(*p.Title) < 300 {
		title = stripHTMLTitle(*p.Title)
		summary = p.Text
		// Strip bracketed header from summary if present
		if strings.HasPrefix(summary, "[") {
			if idx := strings.Index(summary, "]\n"); idx != -1 {
				summary = strings.TrimSpace(summary[idx+2:])
			} else if idx := strings.Index(summary, "] "); idx != -1 {
				summary = strings.TrimSpace(summary[idx+2:])
			}
		}
		// Strip leading ** bold markdown
		summary = strings.TrimPrefix(summary, "**")
		if idx := strings.Index(summary, "**"); idx != -1 && idx < 100 {
			summary = strings.TrimSpace(summary[idx+2:])
		}
		if len(summary) > 500 {
			summary = summary[:500] + "..."
		}
		if len(title) > 200 {
			title = title[:200] + "..."
		}
		return
	}

	text := p.Text

	// Try extracting [Title — Source] or [Title] from start of text
	if strings.HasPrefix(text, "[") || strings.HasPrefix(text, "**[") {
		clean := strings.TrimPrefix(text, "**")
		if end := strings.Index(clean, "]"); end != -1 && end < 300 {
			bracket := clean[1:end]
			// Strip source suffix like " — Julia Evans"
			if dash := strings.LastIndex(bracket, " — "); dash != -1 {
				title = bracket[:dash]
			} else if dash := strings.LastIndex(bracket, " - "); dash != -1 {
				title = bracket[:dash]
			} else {
				title = bracket
			}
			rest := strings.TrimSpace(clean[end+1:])
			rest = strings.TrimPrefix(rest, "**")
			rest = strings.TrimPrefix(rest, "\n")
			rest = strings.TrimSpace(rest)
			if len(rest) > 500 {
				rest = rest[:500] + "..."
			}
			summary = rest
			return
		}
	}

	// Fallback: first sentence as title
	if idx := strings.Index(text, ". "); idx != -1 && idx < 200 {
		title = text[:idx]
	} else if len(text) > 120 {
		title = text[:120] + "..."
	} else {
		title = text
	}
	summary = ""
	return
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
		title, summary := extractTitleSummary(p)
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

type skillsPageItem struct {
	Name        string
	Description string
	Category    string
	Publisher   string
	SourceURL   string
	Weight      int
}

type skillsPageData struct {
	User   *SocialUser
	Skills []skillsPageItem
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
		User:   s.sessionUser(r),
		Skills: items,
	}
	s.template(skillsTmpl, "base.html", "skills.html").ExecuteTemplate(w, "base", data)
}

type skillDetailPageData struct {
	User      *SocialUser
	Name      string
	Description string
	Category  string
	Publisher string
	SourceURL string
	Content   string
	Tags      string
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

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.LocalMode {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	// Store next redirect in cookie so it survives OAuth round-trip
	if next := r.URL.Query().Get("next"); next != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "oauth_next",
			Value:    next,
			Path:     "/auth",
			Domain:   s.cookieDomain(),
			MaxAge:   600,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	}
	data := loginPageData{
		User:      s.sessionUser(r),
		Sent:      r.URL.Query().Get("sent") == "1",
		HasGitHub: s.Config.GitHubClientID != "",
		HasSMTP:   s.Config.SMTPHost != "",
	}
	s.template(loginTmpl, "base.html", "login.html").ExecuteTemplate(w, "base", data)
}
