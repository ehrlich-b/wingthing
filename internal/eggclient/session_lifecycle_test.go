package eggclient

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestLifecycleSessionPrefersActiveLabel(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = old })
	for _, activeID := range []string{"a-active", "z-active"} {
		t.Run(activeID, func(t *testing.T) {
			cfg := &config.Config{Dir: t.TempDir()}
			for _, id := range []string{"m-archived", activeID, "work-prefix"} {
				dir := filepath.Join(cfg.Dir, "eggs", id)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := WriteSessionName(dir, "work"); err != nil {
					t.Fatal(err)
				}
				if id == activeID {
					if err := os.WriteFile(filepath.Join(dir, "egg.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, resolve := range []func() (LocalSession, error){
				func() (LocalSession, error) { return ResolveLifecycleSession(cfg, "work") },
				func() (LocalSession, error) { return ResolveActiveSession(context.Background(), cfg, "work") },
			} {
				if got, err := resolve(); err != nil || got.ID != activeID {
					t.Fatalf("active label resolved to %q: %v", got.ID, err)
				}
			}
			if got, err := ResolveLifecycleSession(cfg, "m-archived"); err != nil || got.ID != "m-archived" {
				t.Fatalf("archived ID resolved to %q: %v", got.ID, err)
			}
			duplicate := filepath.Join(cfg.Dir, "eggs", "duplicate")
			if err := os.MkdirAll(duplicate, 0700); err != nil {
				t.Fatal(err)
			}
			if err := WriteSessionName(duplicate, "work"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(duplicate, "egg.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
				t.Fatal(err)
			}
			for _, resolve := range []func() (LocalSession, error){
				func() (LocalSession, error) { return ResolveLifecycleSession(cfg, "work") },
				func() (LocalSession, error) { return ResolveActiveSession(context.Background(), cfg, "work") },
			} {
				if _, err := resolve(); err == nil || !strings.Contains(err.Error(), "ambiguous") {
					t.Fatalf("duplicate active labels were not ambiguous: %v", err)
				}
			}
			if err := os.Remove(filepath.Join(duplicate, "egg.pid")); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(cfg.Dir, "eggs", activeID, "egg.pid")); err != nil {
				t.Fatal(err)
			}
			if _, err := ResolveLifecycleSession(cfg, "work"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
				t.Fatalf("archived duplicate labels were not ambiguous: %v", err)
			}
		})
	}
}

func TestPreviewLifecycleHomeDoesNotFallBackToHost(t *testing.T) {
	previous := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = previous })
	cfg := &config.Config{Dir: t.TempDir()}
	expected := config.PreviewProviderHome(cfg.Dir)
	for _, recorded := range []string{"", expected} {
		home, err := LifecycleProviderHome(cfg, recorded)
		if err != nil || home != expected {
			t.Fatalf("preview provider home: %q, %v", home, err)
		}
	}
	if _, err := LifecycleProviderHome(cfg, t.TempDir()); err == nil {
		t.Fatal("preview accepted another provider home")
	}
	config.ReleaseChannel = "stable"
	legacy := t.TempDir()
	if home, err := LifecycleProviderHome(cfg, legacy); err != nil || home != legacy {
		t.Fatalf("stable recorded home changed: %q, %v", home, err)
	}
}
