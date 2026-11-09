package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
	"github.com/ehrlich-b/wingthing/internal/updater"
)

func TestStableUpdateRejectsPreviewStateBeforeAnyWork(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = old })
	state := testsocket.Dir(t)
	t.Setenv("WINGTHING_DIR", state)
	files := map[string]string{
		".release-channel": "preview\n",
		"wing.pid":         "123456789\n",
		"wing.args":        "[\"wing\",\"start\",\"--foreground\"]",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(state, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Cancellation keeps a regressed command from contacting the release feed.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command := updateCmd()
	command.SetContext(ctx)
	if err := command.RunE(command, nil); err == nil || !strings.Contains(err.Error(), "state belongs to") {
		t.Fatalf("update did not reject the channel before other work: %v", err)
	}
	if lock, err := updater.AcquireUpdateLifecycleLock(); err == nil {
		_ = lock.Close()
		t.Fatal("update acquired a foreign channel's lifecycle lock")
	} else if !strings.Contains(err.Error(), "state belongs to") {
		t.Fatalf("update lock did not reject the channel: %v", err)
	}
	if _, err := updater.DaemonStateForUpdate(); err == nil || !strings.Contains(err.Error(), "state belongs to") {
		t.Fatalf("daemon inspection did not reject the channel: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "daemon.lock")); !os.IsNotExist(err) {
		t.Fatalf("update touched the foreign lifecycle lock: %v", err)
	}
	for name, want := range files {
		if data, err := os.ReadFile(filepath.Join(state, name)); err != nil || string(data) != want {
			t.Fatalf("update changed foreign metadata %s: %q %v", name, data, err)
		}
	}
}

type unavailableReleaseTransport struct{}

func (unavailableReleaseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("missing"))}, nil
}
func TestUpdateUnavailableReportsOnePlainSentence(t *testing.T) {
	root := testsocket.Dir(t)
	t.Setenv("HOME", root)
	t.Setenv("WINGTHING_DIR", filepath.Join(root, "state"))
	transport := http.DefaultTransport
	http.DefaultTransport = unavailableReleaseTransport{}
	t.Cleanup(func() { http.DefaultTransport = transport })
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = stdout; _ = reader.Close(); _ = writer.Close() })
	command := updateCmd()
	command.SetContext(context.Background())
	result := command.RunE(command, nil)
	os.Stdout = stdout
	_ = writer.Close()
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if result != updater.ErrReleaseUnavailable || len(output) != 0 {
		t.Fatalf("error=%v stdout=%q", result, output)
	}
}
