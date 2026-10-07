package wing

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
)

type reloadLog chan string

func (messages reloadLog) Write(data []byte) (int, error) {
	if line := string(data); strings.Contains(line, "loader reload refused") || strings.Contains(line, "config reloaded:") {
		messages <- line
	}
	return len(data), nil
}

func TestWingReloadRejectsUnlinkedLegacyWritableDescriptor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := filepath.Join(home, "state")
	t.Setenv("WINGTHING_DIR", state)
	oldChannel := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	defer func() { config.ReleaseChannel = oldChannel }()
	tools := filepath.Join(state, "tools")
	if err := os.MkdirAll(tools, 0700); err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(state, "wing.yaml")
	tool := filepath.Join(tools, "x.yaml")
	for path, data := range map[string]string{
		policy: "connection_mode: direct\nlabels: [safe]\n",
		tool:   "name: safe\nrun: /bin/true\n",
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Obtain descriptors as a pre-hardening egg could, then remove every alias.
	var descriptors []*os.File
	for _, path := range []string{policy, tool} {
		alias := filepath.Join(home, filepath.Base(path)+".alias")
		if err := os.Link(path, alias); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(alias, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		descriptors = append(descriptors, f)
		if err := os.Remove(alias); err != nil {
			t.Fatal(err)
		}
	}
	// A controlled, temporary wing uses a fixture relay that never registers
	// it, so it cannot reclaim or signal real sessions.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ready := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case ready <- struct{}{}:
		default:
		}
		http.Error(w, "fixture", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	messages := make(reloadLog, 10)
	previous := log.Writer()
	log.SetOutput(messages)
	defer log.SetOutput(previous)
	signals := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- RunWingWithContext(EntryOptions{Version: "test"}, ctx, signals, server.URL, "", "auto", "", "", nil, "", false, false, false, false, false, &auth.DeviceToken{Token: "fixture"})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("fixture wing did not stop")
		}
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("fixture wing failed to start: %v", err)
	case <-ctx.Done():
		t.Fatal("fixture wing startup timed out")
	}
	legacy := filepath.Join(state, "eggs", "legacy")
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(legacy, "egg.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	for index, content := range []string{"connection_mode: direct\nlabels: [injected]\n", "name: safe\nrun: injected-host-command\n"} {
		f := descriptors[index]
		if err := f.Truncate(0); err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteAt([]byte(content), 0); err != nil {
			t.Fatal(err)
		}
	}
	requestReload := func() string {
		t.Helper()
		signals <- syscall.SIGHUP
		select {
		case line := <-messages:
			return line
		case <-ctx.Done():
			t.Fatal("reload did not finish")
			return ""
		}
	}
	if line := requestReload(); !strings.Contains(line, "loader reload refused") || !strings.Contains(line, "stop these eggs") {
		t.Fatalf("accepted legacy descriptor injection: %s", line)
	}
	// The descriptor threat ends when the old process exits. The operator
	// reviews/restores the YAML before re-authorizing it with another SIGHUP.
	if err := os.Remove(pidPath); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{policy: "connection_mode: direct\nlabels: [restored]\n", tool: "name: safe\nrun: /bin/true\n"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if line := requestReload(); !strings.Contains(line, "config reloaded:") {
		t.Fatalf("reload remained blocked after legacy replacement: %s", line)
	}
}
