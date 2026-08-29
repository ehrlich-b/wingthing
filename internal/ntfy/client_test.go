package ntfy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestNewBareTopic(t *testing.T) {
	c := New("my-secret-topic", "", "attention,exit")
	if c.url != "https://ntfy.sh/my-secret-topic" {
		t.Fatalf("got %q", c.url)
	}
}

func TestNewFullURL(t *testing.T) {
	c := New("https://ntfy.example.com/mytopic", "tok123", "attention")
	if c.url != "https://ntfy.example.com/mytopic" {
		t.Fatalf("got %q", c.url)
	}
	if c.token != "tok123" {
		t.Fatalf("got token %q", c.token)
	}
}

func TestNewHostedRequiresSafeHTTPSEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"http://ntfy.example.com/topic",
		"https://user:password@ntfy.example.com/topic",
		"https:///missing-host",
	} {
		if _, err := NewHosted(endpoint, "", "attention"); err == nil {
			t.Errorf("NewHosted(%q) succeeded", endpoint)
		}
	}
	client, err := NewHosted("reserved-topic", "token", "attention")
	if err != nil {
		t.Fatal(err)
	}
	if client.url != "https://ntfy.sh/reserved-topic" {
		t.Fatalf("hosted bare topic URL = %q", client.url)
	}
}

func TestHostedDialRejectsNonPublicAddresses(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1:443",
		"[::1]:443",
		"10.0.0.1:443",
		"169.254.169.254:443",
		"100.64.0.1:443",
	} {
		if conn, err := dialPublicContext(context.Background(), "tcp", address); err == nil {
			_ = conn.Close()
			t.Errorf("hosted dial accepted %s", address)
		}
	}
}

func TestPublicDestinationIP(t *testing.T) {
	for _, address := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !publicDestinationIP(net.ParseIP(address)) {
			t.Errorf("publicDestinationIP(%s) = false", address)
		}
	}
}

func TestEventFiltering(t *testing.T) {
	c := New("t", "", "attention")
	if !c.events["attention"] {
		t.Fatal("attention should be enabled")
	}
	if c.events["exit"] {
		t.Fatal("exit should not be enabled")
	}
}

func TestEventFilteringWhitespace(t *testing.T) {
	c := New("t", "", " attention , exit ")
	if !c.events["attention"] || !c.events["exit"] {
		t.Fatal("both should be enabled")
	}
}

func TestEmptyEvents(t *testing.T) {
	c := New("t", "", "")
	if len(c.events) != 0 {
		t.Fatalf("expected no events, got %v", c.events)
	}
}

func TestSendAttentionFiltered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not have been called")
	}))
	defer srv.Close()
	c := New(srv.URL, "", "exit") // attention NOT enabled
	c.SendAttention("s1", "claude", "/home", "")
}

func TestSendExitFiltered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not have been called")
	}))
	defer srv.Close()
	c := New(srv.URL, "", "attention") // exit NOT enabled
	c.SendExit("s1", "claude", "/home", 0, "")
}

func TestSendAttentionPost(t *testing.T) {
	var mu sync.Mutex
	var gotTitle, gotBody, gotPriority, gotTags, gotClick, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotTitle = r.Header.Get("Title")
		gotPriority = r.Header.Get("Priority")
		gotTags = r.Header.Get("Tags")
		gotClick = r.Header.Get("Click")
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := New(srv.URL, "mytoken", "attention")
	c.SendAttention("s1", "claude", "/proj", "https://app.wingthing.ai/#s/s1")

	mu.Lock()
	defer mu.Unlock()
	if gotTitle != "claude needs input" {
		t.Fatalf("title = %q", gotTitle)
	}
	if gotBody != "session in /proj" {
		t.Fatalf("body = %q", gotBody)
	}
	if gotPriority != "high" {
		t.Fatalf("priority = %q", gotPriority)
	}
	if gotTags != "bell" {
		t.Fatalf("tags = %q", gotTags)
	}
	if gotClick != "https://app.wingthing.ai/#s/s1" {
		t.Fatalf("click = %q", gotClick)
	}
	if gotAuth != "Bearer mytoken" {
		t.Fatalf("auth = %q", gotAuth)
	}
}

func TestSendExitSuccess(t *testing.T) {
	var gotTitle, gotTags, gotPriority string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTitle = r.Header.Get("Title")
		gotTags = r.Header.Get("Tags")
		gotPriority = r.Header.Get("Priority")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := New(srv.URL, "", "exit")
	c.SendExit("s1", "claude", "/proj", 0, "")

	if gotTitle != "claude finished" {
		t.Fatalf("title = %q", gotTitle)
	}
	if gotTags != "white_check_mark" {
		t.Fatalf("tags = %q", gotTags)
	}
	if gotPriority != "default" {
		t.Fatalf("priority = %q", gotPriority)
	}
}

func TestSendExitCrash(t *testing.T) {
	var gotTitle, gotTags, gotPriority string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTitle = r.Header.Get("Title")
		gotTags = r.Header.Get("Tags")
		gotPriority = r.Header.Get("Priority")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := New(srv.URL, "", "exit")
	c.SendExit("s1", "claude", "/proj", 1, "")

	if gotTitle != "claude crashed (1)" {
		t.Fatalf("title = %q", gotTitle)
	}
	if gotTags != "x" {
		t.Fatalf("tags = %q", gotTags)
	}
	if gotPriority != "high" {
		t.Fatalf("priority = %q", gotPriority)
	}
}

func TestSendExitEmptyAgent(t *testing.T) {
	var gotTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTitle = r.Header.Get("Title")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := New(srv.URL, "", "exit")
	c.SendExit("s1", "", "/proj", 0, "")

	if gotTitle != "Agent finished" {
		t.Fatalf("title = %q, want 'Agent finished'", gotTitle)
	}
}

func TestSendTestSync(t *testing.T) {
	var gotTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTitle = r.Header.Get("Title")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := New(srv.URL, "", "attention,exit")
	err := c.SendTest()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTitle != "wingthing test" {
		t.Fatalf("title = %q", gotTitle)
	}
}

func TestSendTestHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
	}))
	defer srv.Close()

	c := New(srv.URL, "", "attention,exit")
	err := c.SendTest()
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error = %q", err)
	}
}

func TestNoAuthHeaderWithoutToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := New(srv.URL, "", "attention,exit")
	if err := c.SendTest(); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Fatalf("expected no auth header, got %q", gotAuth)
	}
}

func TestGenerateTopic(t *testing.T) {
	topic := GenerateTopic()
	if !strings.HasPrefix(topic, "wing-") {
		t.Fatalf("should start with wing-, got %q", topic)
	}
	parts := strings.Split(topic, "-")
	if len(parts) != 5 { // wing + 4 words
		t.Fatalf("expected 5 parts, got %d: %q", len(parts), topic)
	}
	// Should be unique
	topic2 := GenerateTopic()
	if topic == topic2 {
		t.Fatal("two topics should not be identical")
	}
}
