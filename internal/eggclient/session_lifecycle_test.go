package eggclient

import (
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

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
