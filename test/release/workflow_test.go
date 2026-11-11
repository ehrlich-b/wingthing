package release_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type step struct {
	Name string
	Uses string
	Run  string
	With map[string]string
	Env  map[string]string
}

type job struct {
	If          string
	Needs       any
	Permissions map[string]string
	Steps       []step
}

func TestReleaseWorkflow(t *testing.T) {
	var workflow struct {
		Name string
		On   struct {
			Push     struct{ Tags []string }
			Dispatch struct {
				Inputs map[string]struct {
					Type     string
					Default  bool
					Required bool
				}
			} `yaml:"workflow_dispatch"`
		}
		Concurrency struct {
			Group  string
			Cancel *bool `yaml:"cancel-in-progress"`
		}
		Permissions map[string]string
		Jobs        map[string]job
	}
	readYAML(t, "../../.github/workflows/release.yml", &workflow)
	if workflow.Name != "release" || !reflect.DeepEqual(workflow.On.Push.Tags, []string{"v*"}) {
		t.Fatal("release must run on v* tags")
	}
	input, ok := workflow.On.Dispatch.Inputs["dry_run"]
	if !ok || input.Type != "boolean" || !input.Default || !input.Required {
		t.Fatal("manual runs must default to a boolean dry run")
	}
	if workflow.Concurrency.Group != "release-${{ github.ref }}" || workflow.Concurrency.Cancel == nil || *workflow.Concurrency.Cancel {
		t.Fatal("runs for the same tag must serialize without cancellation")
	}
	if workflow.Permissions["contents"] != "read" {
		t.Fatal("build jobs must only read repository contents")
	}
	if len(workflow.Jobs) != 4 {
		t.Fatal("expected tag validation, deterministic, macOS and publishing jobs")
	}
	tagRuns := runs(workflow.Jobs["release-tag"])
	if !strings.Contains(tagRuns, `test "${{ github.ref_type }}" = tag`) ||
		!strings.Contains(tagRuns, `sh scripts/check-release-tag.sh "$VERSION"`) {
		t.Fatal("manual and push runs must validate a stable tag before building")
	}
	for _, name := range []string{"release-tag", "deterministic"} {
		found := false
		for _, s := range workflow.Jobs[name].Steps {
			if strings.Contains(s.Run, "check-release-tag.sh") || strings.Contains(s.Run, "make release") {
				found = true
				if s.Env["VERSION"] != "${{ github.ref_name }}" {
					t.Fatalf("%s must use the selected tag as VERSION", name)
				}
			}
		}
		if !found {
			t.Fatalf("%s is missing its versioned step", name)
		}
	}
	for _, name := range []string{"deterministic", "macos-sandbox"} {
		j := workflow.Jobs[name]
		if j.Needs != "release-tag" || j.If != "" {
			t.Fatalf("%s must build even during dry runs, after tag validation", name)
		}
		found := false
		for _, s := range j.Steps {
			if s.Uses == "actions/setup-go@v5" {
				found = true
				if s.With["go-version-file"] != "go.mod" || s.With["go-version"] != "" {
					t.Fatalf("%s must derive Go from go.mod", name)
				}
			}
		}
		if !found {
			t.Fatalf("%s is missing Go setup", name)
		}
	}
	deterministic := workflow.Jobs["deterministic"]
	commands := []string{
		"go test -p 2 ./test/release -count=1", "make check",
		"npm install -g @anthropic-ai/claude-code@2.1.260", "make gate GATE=claude",
		"make gate GATE=static RACE_PACKAGES=./...",
		"sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0",
		"make gate GATE=integration", "make gate GATE=compat",
		"make e2e-linux LINUX_DISTROS=debian", "make e2e-linux LINUX_DISTROS=ubuntu",
		"make e2e-web", "git diff --check", `make release CHANNEL=stable VERSION="$VERSION"`,
	}
	var actual []string
	for _, s := range deterministic.Steps {
		if s.Run != "" {
			actual = append(actual, s.Run)
		}
	}
	if !reflect.DeepEqual(actual, commands) || runs(workflow.Jobs["macos-sandbox"]) != "make e2e-mac" {
		t.Fatalf("historical gates, including integration, must precede packaging: %v", actual)
	}
	var uploaded string
	for _, s := range deterministic.Steps {
		if s.Uses == "actions/upload-artifact@v4" && s.With["path"] == "dist/" && s.With["if-no-files-found"] == "error" {
			uploaded = s.With["name"]
		}
	}
	publish := workflow.Jobs["release"]
	if publish.If != "${{ github.event_name == 'push' || !inputs.dry_run }}" || publish.Permissions["contents"] != "write" {
		t.Fatal("only publishing may write, and manual dry runs must skip publishing")
	}
	if !reflect.DeepEqual(publish.Needs, []any{"deterministic", "macos-sandbox"}) {
		t.Fatal("publishing must wait for both gate jobs")
	}
	downloaded := ""
	for _, s := range publish.Steps {
		if s.Uses == "actions/download-artifact@v4" && s.With["path"] == "dist/" {
			downloaded = s.With["name"]
		}
	}
	if uploaded != "wingthing-release-${{ github.sha }}" || downloaded != uploaded {
		t.Fatal("publish must consume the tested build artifacts")
	}
	wantPublish := `gh release create "${{ github.ref_name }}" dist/wt-linux-amd64 dist/wt-linux-arm64 dist/wt-darwin-amd64 dist/wt-darwin-arm64 dist/SHA256SUMS --generate-notes`
	if runs(publish) != wantPublish {
		t.Fatal("publish must attach exactly the four stable binaries and checksum manifest")
	}
	for name, j := range workflow.Jobs {
		if name != "release" && (strings.Contains(runs(j), "gh release") || j.Permissions["contents"] == "write") {
			t.Fatalf("%s must not publish during a dry run", name)
		}
	}
}

func readYAML(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}

func runs(j job) string {
	var result []string
	for _, s := range j.Steps {
		if s.Run != "" {
			result = append(result, s.Run)
		}
	}
	return strings.Join(result, "\n")
}
