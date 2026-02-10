//go:build ignore

// Pipeline: fetch RSS feeds from feeds.md, extract articles, output as TSV.
// Usage: go run pipeline.go /path/to/feeds.md
// TSV format: SOURCE\tTITLE\tLINK\tDATE\tTEXT
package main

import (
	"bufio"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type Feed struct {
	Channel struct {
		Items []RSSItem `xml:"item"`
	} `xml:"channel"`
	Entries []AtomEntry `xml:"entry"`
}

type RSSItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
}

type AtomEntry struct {
	Title     string     `xml:"title"`
	Links     []AtomLink `xml:"link"`
	Content   string     `xml:"content"`
	Summary   string     `xml:"summary"`
	Published string     `xml:"published"`
	Updated   string     `xml:"updated"`
}

type AtomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

type FeedSource struct {
	URL     string
	Source  string // space slug from ### header
	Comment string
}

func parseFeedsMD(path string) ([]FeedSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var feeds []FeedSource
	var currentSpace string

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "### ") {
			currentSpace = strings.TrimPrefix(line, "### ")
		} else if strings.HasPrefix(line, "- http") {
			url := strings.TrimPrefix(line, "- ")
			comment := ""
			if idx := strings.Index(url, "  #"); idx > 0 {
				comment = strings.TrimSpace(url[idx+3:])
				url = strings.TrimSpace(url[:idx])
			}
			// Use comment before first " — " as source name, or space slug
			source := currentSpace
			if comment != "" {
				if dash := strings.Index(comment, " — "); dash > 0 {
					source = comment[:dash]
				} else {
					source = comment
				}
			}
			feeds = append(feeds, FeedSource{URL: url, Source: source, Comment: comment})
		}
	}
	return feeds, scanner.Err()
}

func parseDate(s string) string {
	if s == "" {
		return ""
	}
	formats := []string{
		time.RFC3339,
		time.RFC1123Z,
		time.RFC1123,
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 MST",
		"2006-01-02T15:04:05-07:00",
		"2006-01-02T15:04:05Z",
		"2006-01-02",
	}
	for _, f := range formats {
		t, err := time.Parse(f, s)
		if err == nil {
			return t.Format(time.RFC3339)
		}
	}
	return ""
}

func fetchFeed(url string) ([]Article, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var feed Feed
	xml.Unmarshal(data, &feed)

	var articles []Article

	for _, item := range feed.Channel.Items {
		if item.Title == "" || item.Link == "" {
			continue
		}
		desc := stripTags(item.Description)
		if len(desc) > 800 {
			desc = desc[:800]
		}
		articles = append(articles, Article{
			Title: item.Title, Link: item.Link, Text: desc,
			Date: parseDate(item.PubDate),
		})
	}

	for _, entry := range feed.Entries {
		if entry.Title == "" {
			continue
		}
		link := ""
		for _, l := range entry.Links {
			if l.Rel == "" || l.Rel == "alternate" {
				link = l.Href
				break
			}
		}
		if link == "" && len(entry.Links) > 0 {
			link = entry.Links[0].Href
		}
		text := entry.Content
		if text == "" {
			text = entry.Summary
		}
		text = stripTags(text)
		if len(text) > 800 {
			text = text[:800]
		}
		date := entry.Published
		if date == "" {
			date = entry.Updated
		}
		articles = append(articles, Article{
			Title: entry.Title, Link: link, Text: text,
			Date: parseDate(date),
		})
	}

	return articles, nil
}

type Article struct {
	Title  string
	Link   string
	Text   string
	Date   string
	Source string
}

func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			b.WriteRune(r)
		}
	}
	out := b.String()
	out = strings.ReplaceAll(out, "&amp;", "&")
	out = strings.ReplaceAll(out, "&lt;", "<")
	out = strings.ReplaceAll(out, "&gt;", ">")
	out = strings.ReplaceAll(out, "&quot;", "\"")
	out = strings.ReplaceAll(out, "&#39;", "'")
	out = strings.ReplaceAll(out, "&#8217;", "'")
	out = strings.ReplaceAll(out, "&#8220;", "\"")
	out = strings.ReplaceAll(out, "&#8221;", "\"")
	out = strings.ReplaceAll(out, "&rsquo;", "'")
	out = strings.ReplaceAll(out, "&ldquo;", "\"")
	out = strings.ReplaceAll(out, "&rdquo;", "\"")
	out = strings.ReplaceAll(out, "&mdash;", "—")
	out = strings.ReplaceAll(out, "&ndash;", "–")
	out = strings.ReplaceAll(out, "\n", " ")
	out = strings.ReplaceAll(out, "\t", " ")
	for strings.Contains(out, "  ") {
		out = strings.ReplaceAll(out, "  ", " ")
	}
	return strings.TrimSpace(out)
}

func urlSlug(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return u.Host + u.Path
}

func main() {
	feedsPath := "skills/feeds.md"
	if len(os.Args) > 1 {
		feedsPath = os.Args[1]
	}

	feeds, err := parseFeedsMD(feedsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading feeds: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "loaded %d feeds from %s\n", len(feeds), feedsPath)

	// Fetch concurrently (10 at a time)
	type result struct {
		source   string
		articles []Article
	}
	results := make(chan result, len(feeds))
	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup

	for _, f := range feeds {
		wg.Add(1)
		go func(f FeedSource) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			articles, err := fetchFeed(f.URL)
			if err != nil {
				fmt.Fprintf(os.Stderr, "SKIP %s (%s): %v\n", f.Source, f.URL, err)
				return
			}
			// Max 10 articles per feed
			n := 10
			if n > len(articles) {
				n = len(articles)
			}
			for i := range articles[:n] {
				articles[i].Source = f.Source
			}
			results <- result{source: f.Source, articles: articles[:n]}
		}(f)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// Dedup by URL path (slug), filter out articles older than 30 days
	cutoff := time.Now().AddDate(0, 0, -30)
	seen := make(map[string]bool)
	count := 0
	dupes := 0
	old := 0
	for r := range results {
		for _, a := range r.articles {
			// Skip articles older than 30 days
			if a.Date != "" {
				t, err := time.Parse(time.RFC3339, a.Date)
				if err == nil && t.Before(cutoff) {
					old++
					continue
				}
			}
			slug := urlSlug(a.Link)
			if slug != "" && seen[slug] {
				dupes++
				continue
			}
			if slug != "" {
				seen[slug] = true
			}
			fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\t%s\n", a.Source, a.Title, a.Link, a.Date, a.Text)
			count++
		}
	}
	fmt.Fprintf(os.Stderr, "fetched %d articles (%d dupes, %d older than 30d removed) from %d feeds\n", count, dupes, old, len(feeds))
}
