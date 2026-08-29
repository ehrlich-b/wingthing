package ntfy

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client sends push notifications via ntfy.sh (or a self-hosted ntfy server).
type Client struct {
	url    string // full URL: https://ntfy.sh/{topic}
	token  string // optional bearer token for reserved topics
	events map[string]bool
	client *http.Client
}

// New creates a new ntfy client. Topic can be a bare topic name (expanded to
// https://ntfy.sh/{topic}) or a full URL (https://ntfy.example.com/mytopic).
// Events is a comma-separated list of event types to send (e.g. "attention,exit").
func New(topic, token, events string) *Client {
	return newClient(topic, token, events, http.DefaultClient)
}

// NewHosted creates a client suitable for a multi-tenant hosted relay. It
// requires public HTTPS destinations, refuses redirects to unsafe endpoints,
// and resolves/dials only public IPs so notification configuration cannot be
// used to reach loopback, metadata, or private services. Operator-controlled
// local and roost deployments may continue to use New for private ntfy servers.
func NewHosted(topic, token, events string) (*Client, error) {
	endpoint := endpointURL(topic)
	if err := validateHostedEndpoint(endpoint); err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:       nil,
		DialContext: dialPublicContext,
	}
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return validateHostedEndpoint(req.URL.String())
		},
	}
	return newClient(topic, token, events, client), nil
}

func newClient(topic, token, events string, client *http.Client) *Client {
	endpoint := endpointURL(topic)
	evMap := make(map[string]bool)
	for _, e := range strings.Split(events, ",") {
		e = strings.TrimSpace(e)
		if e != "" {
			evMap[e] = true
		}
	}
	return &Client{url: endpoint, token: token, events: evMap, client: client}
}

func endpointURL(topic string) string {
	if !strings.HasPrefix(topic, "http://") && !strings.HasPrefix(topic, "https://") {
		return "https://ntfy.sh/" + topic
	}
	return topic
}

func validateHostedEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid ntfy endpoint: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return fmt.Errorf("hosted ntfy endpoint must be a public HTTPS URL without user info")
	}
	return nil
}

func dialPublicContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse ntfy destination: %w", err)
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve ntfy destination: %w", err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("ntfy destination resolved to no addresses")
	}
	for _, address := range addresses {
		if !publicDestinationIP(address.IP) {
			return nil, fmt.Errorf("ntfy destination resolved to a non-public address")
		}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var lastErr error
	for _, address := range addresses {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("dial ntfy destination: %w", lastErr)
}

func publicDestinationIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	// Go intentionally does not classify the shared carrier-grade NAT range as
	// private, but it is still not a public Internet destination.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 0x40 {
		return false
	}
	return true
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
	if err := c.post(title, body, "high", "bell", clickURL); err != nil {
		log.Printf("ntfy: attention notification failed: %v", err)
	}
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
	if err := c.post(title, body, priority, tags, clickURL); err != nil {
		log.Printf("ntfy: exit notification failed: %v", err)
	}
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
	resp, err := c.client.Do(req)
	if err != nil {
		log.Printf("ntfy: post failed: %v", err)
		return err
	}
	if err := resp.Body.Close(); err != nil {
		log.Printf("ntfy: close response: %v", err)
	}
	if resp.StatusCode >= 400 {
		err = fmt.Errorf("ntfy: HTTP %d", resp.StatusCode)
		log.Printf("%v", err)
		return err
	}
	return nil
}
