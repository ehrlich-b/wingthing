package egg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func partnerLink(t *testing.T, target, link string) string {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link
}

func TestPreviewClaudeGuardRejectsSymlinkAliases(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	osHome := filepath.Join(root, "OS-user")
	if err := os.MkdirAll(filepath.Join(osHome, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(osHome, ".claude.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	homeLink := partnerLink(t, osHome, filepath.Join(root, "home-link"))
	cfgLink := partnerLink(t, filepath.Join(osHome, ".claude"), filepath.Join(root, "cfg-link"))
	jsonLink := partnerLink(t, filepath.Join(osHome, ".claude.json"), filepath.Join(root, "json-link"))
	for _, src := range []string{cfgLink, jsonLink, filepath.Join(cfgLink, "ide", "new"), filepath.Join(homeLink, ".claude", "missing"), filepath.Join(homeLink, ".claude.json")} {
		if _, err := GuardPreviewClaudeMounts([]sandbox.Mount{{Source: src, Target: "/x"}}, osHome); err == nil {
			t.Fatalf("aliased mount bypassed guard: %q", src)
		}
		if _, err := GuardPreviewClaudeMounts([]sandbox.Mount{{Source: src, Target: "/x"}}, homeLink); err == nil {
			t.Fatalf("aliased OS home bypassed guard: %q", src)
		}
	}
	broad := []sandbox.Mount{{Source: homeLink, Target: homeLink}, {Source: root, Target: root}, {Source: filepath.Join(osHome, ".claude-x")}, {Source: filepath.Join(osHome, ".claude.jsonx")}}
	protected, err := GuardPreviewClaudeMounts(broad, homeLink)
	if err != nil || len(protected) != 2 {
		t.Fatalf("broad or sibling mounts rejected: %v %v", protected, err)
	}
	if config.CanonicalProviderPath(protected[0]) != filepath.Join(osHome, ".claude") || config.CanonicalProviderPath(protected[1]) != filepath.Join(osHome, ".claude.json") {
		t.Fatalf("protected paths lost under broad alias mount: %v", protected)
	}
}

func TestPreviewClaudeContextRejectsAliasedForeignSelector(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dataHome := config.PreviewProviderHome(filepath.Join(root, "state"))
	foreign := partnerLink(t, filepath.Join(root, "elsewhere"), filepath.Join(root, "sel"))
	if _, err := ApplyPreviewClaudeOSContext(map[string]string{"CLAUDE_CONFIG_DIR": foreign}, dataHome); err == nil {
		t.Fatal("accepted symlinked foreign config selector")
	}
	if err := os.MkdirAll(filepath.Join(dataHome, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	good := partnerLink(t, filepath.Join(dataHome, ".claude"), filepath.Join(root, "good"))
	env := map[string]string{"CLAUDE_CONFIG_DIR": good}
	if _, err := ApplyPreviewClaudeOSContext(env, dataHome); err != nil || env["CLAUDE_CONFIG_DIR"] != config.CanonicalProviderPath(filepath.Join(dataHome, ".claude")) {
		t.Fatalf("canonical selector not final: %v %#v", err, env)
	}
}
