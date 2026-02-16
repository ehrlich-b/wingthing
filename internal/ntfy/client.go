package ntfy

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// Client sends push notifications via ntfy.sh (or a self-hosted ntfy server).
type Client struct {
	url    string // full URL: https://ntfy.sh/{topic}
	token  string // optional bearer token for reserved topics
	events map[string]bool
}

// New creates a new ntfy client. Topic can be a bare topic name (expanded to
// https://ntfy.sh/{topic}) or a full URL (https://ntfy.example.com/mytopic).
// Events is a comma-separated list of event types to send (e.g. "attention,exit").
func New(topic, token, events string) *Client {
	url := topic
	if !strings.HasPrefix(topic, "http://") && !strings.HasPrefix(topic, "https://") {
		url = "https://ntfy.sh/" + topic
	}
	evMap := make(map[string]bool)
	for _, e := range strings.Split(events, ",") {
		e = strings.TrimSpace(e)
		if e != "" {
			evMap[e] = true
		}
	}
	return &Client{url: url, token: token, events: evMap}
}

// SendAttention sends a "needs input" notification synchronously.
// Caller is responsible for running in a goroutine if fire-and-forget is desired.
func (c *Client) SendAttention(sessionID, agent, cwd, clickURL string) {
	if !c.events["attention"] {
		return
	}
	if agent == "" {
		agent = "Agent"
	}
	title := fmt.Sprintf("%s needs input", agent)
	body := fmt.Sprintf("session in %s", cwd)
	c.post(title, body, "high", "bell", clickURL)
}

// SendExit sends a session exit notification synchronously.
// Caller is responsible for running in a goroutine if fire-and-forget is desired.
func (c *Client) SendExit(sessionID, agent, cwd string, exitCode int, clickURL string) {
	if !c.events["exit"] {
		return
	}
	if agent == "" {
		agent = "Agent"
	}
	var title, priority, tags string
	if exitCode == 0 {
		title = fmt.Sprintf("%s finished", agent)
		priority = "default"
		tags = "white_check_mark"
	} else {
		title = fmt.Sprintf("%s crashed (%d)", agent, exitCode)
		priority = "high"
		tags = "x"
	}
	body := fmt.Sprintf("session in %s", cwd)
	c.post(title, body, priority, tags, clickURL)
}

// SendTest sends a test notification synchronously and returns any error.
func (c *Client) SendTest() error {
	return c.post("wingthing test", "Push notifications are working!", "default", "test_tube", "")
}

func (c *Client) post(title, body, priority, tags, clickURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewBufferString(body))
	if err != nil {
		log.Printf("ntfy: build request: %v", err)
		return err
	}
	req.Header.Set("Title", title)
	req.Header.Set("Priority", priority)
	req.Header.Set("Tags", tags)
	if clickURL != "" {
		req.Header.Set("Click", clickURL)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("ntfy: post failed: %v", err)
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		err = fmt.Errorf("ntfy: HTTP %d", resp.StatusCode)
		log.Printf("%v", err)
		return err
	}
	return nil
}
