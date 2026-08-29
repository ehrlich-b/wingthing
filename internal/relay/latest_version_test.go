package relay

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestLatestVersionFetchIsCoalescedAndCached(t *testing.T) {
	s := NewServer(nil, ServerConfig{})
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	s.latestVersionFetch = func(context.Context) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return "1.2.3", nil
	}

	for range 100 {
		if got := s.getLatestVersion(); got != "" {
			t.Fatalf("uncached version = %q, want empty", got)
		}
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("version fetch did not start")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent fetch calls = %d, want 1", got)
	}

	close(release)
	waitForLatestVersion(t, s, "v1.2.3")
	for range 100 {
		if got := s.getLatestVersion(); got != "v1.2.3" {
			t.Fatalf("cached version = %q", got)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fresh cache triggered %d fetches, want 1", got)
	}
}

func TestLatestVersionFetchFailureBacksOff(t *testing.T) {
	s := NewServer(nil, ServerConfig{})
	done := make(chan struct{})
	var calls atomic.Int32
	s.latestVersionFetch = func(context.Context) (string, error) {
		calls.Add(1)
		close(done)
		return "", errors.New("offline")
	}

	if got := s.getLatestVersion(); got != "" {
		t.Fatalf("uncached version = %q, want empty", got)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("failed fetch did not complete")
	}
	for range 100 {
		if got := s.getLatestVersion(); got != "" {
			t.Fatalf("failed fetch populated version %q", got)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("failure backoff allowed %d fetches, want 1", got)
	}
}

func waitForLatestVersion(t *testing.T, s *Server, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.latestVersionMu.Lock()
		got := s.latestVersion
		s.latestVersionMu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("latest version never became %q", want)
}
