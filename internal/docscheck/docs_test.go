package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var markdownLink = regexp.MustCompile(`\[[^]]+\]\(([^)\s]+)`) // local links in current docs use no spaces

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func TestLocalMarkdownLinksResolve(t *testing.T) {
	root := repositoryRoot(t)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == "dist" || entry.Name() == "out" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, match := range markdownLink.FindAllSubmatch(data, -1) {
			target := string(match[1])
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
				strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") ||
				strings.HasPrefix(target, "/") {
				continue
			}
			if i := strings.IndexByte(target, '#'); i >= 0 {
				target = target[:i]
			}
			if target == "" {
				continue
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(target)))
			if _, statErr := os.Stat(resolved); statErr != nil {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s links to missing %q", rel, target)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPublicDocumentationContractsStayAligned(t *testing.T) {
	root := repositoryRoot(t)
	publicFiles := []string{
		"README.md",
		"SKILL.md",
		"docs/fly-ops.md",
		"docs/sandbox.md",
		"docs/security.md",
		"docs/skills/create-egg/SKILL.md",
		"web/index.html",
		"internal/relay/templates/docs.html",
		"internal/relay/templates/patterns.html",
		"internal/relay/templates/privacy.html",
		"internal/relay/templates/terms.html",
		"internal/relay/templates/install.html",
	}
	err := filepath.WalkDir(filepath.Join(root, "patterns"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".md") {
			rel, _ := filepath.Rel(root, path)
			publicFiles = append(publicFiles, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, rel := range publicFiles {
		data, readErr := os.ReadFile(filepath.Join(root, rel))
		if readErr != nil {
			t.Fatal(readErr)
		}
		lower := strings.ToLower(string(data))
		for _, banned := range []string{"grandfather", "state = client-side", "workflows people are asking for"} {
			if strings.Contains(lower, banned) {
				t.Errorf("public documentation %s contains internal implementation/migration wording %q", rel, banned)
			}
		}
	}

	mustContain := map[string][]string{
		"README.md": {"WT_ROOST_ALLOWED_EMAILS", "never silently falls back", "first log\nin and start Wingthing on every execution machine"},
		"SKILL.md":  {"every execution machine must run `wt login` and `wt start`", "both account-level hosted-relay access", "does not require a wingthing.ai hosted-relay entitlement"},
		"patterns/shared-web-roost/INSTRUCTIONS.md":    {"WT_ROOST_ALLOWED_EMAILS", "OAuth by itself proves"},
		"patterns/hosted-browser-wing/INSTRUCTIONS.md": {"Account access and wing policy are independent", "wt login", "wt start", "self-hosted roost", "application-encrypted"},
		"internal/relay/templates/docs.html":           {"WT_BASE_URL=https://roost.example.com", "WT_ROOST_ALLOWED_EMAILS", "wt roost start --addr :8080", "wt serve --addr :8080", "never silently changes", "gateway database contains", "does not let an account grant itself relay access", "no request happens until you choose load or open", "200,000 serialized terminal characters", "a wing binary from before", "stop or isolate the wing", "On every execution machine", "wt login", "wt start"},
		"internal/relay/templates/patterns.html":       {"/patterns/hosted-browser-wing/INSTRUCTIONS.md", "account with hosted-relay access", "hosted browser -> encrypted relay -> selected wing"},
		"internal/relay/templates/privacy.html":        {"gateway database contains", "embedded wing separately keeps", "200,000 serialized terminal characters", "Clearing the site's browser data removes"},
		"fly.toml":                                     {`WT_RELAY_POLICY = "direct-free"`, "WT_RELAY_MIGRATION_BEFORE"},
		"docs/security.md":                             {"not a downgrade-compatible security policy", "stop the wing or isolate it"},
		"docs/fly-ops.md":                              {"matching GitHub release", "it does not", "publish a GitHub release", "not a completed security boundary until every", "active `fly.toml` is deliberately login-only", "Two edits", `processes = ["login", "edge"]`, "does not exercise a mixed Fly login/edge fleet"},
	}
	for rel, phrases := range mustContain {
		data, readErr := os.ReadFile(filepath.Join(root, rel))
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, phrase := range phrases {
			if !strings.Contains(string(data), phrase) {
				t.Errorf("%s must contain %q", rel, phrase)
			}
		}
	}

	terms, err := os.ReadFile(filepath.Join(root, "internal/relay/templates/terms.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(terms)), "sandbox is permissive by default") {
		t.Fatal("public terms contradict the restrictive default sandbox policy")
	}
	index, err := os.ReadFile(filepath.Join(root, "web/index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(index), "Hosted relay fallback") {
		t.Fatal("hosted app implies a native relay fallback that is not implemented")
	}
	if strings.Count(string(index), `referrerpolicy="no-referrer"`) != 2 {
		t.Fatal("preview iframe and open link must both suppress referrers")
	}
	docs, err := os.ReadFile(filepath.Join(root, "internal/relay/templates/docs.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(docs)), "sqlite database contains only auth state") {
		t.Fatal("public docs omit gateway metadata and the embedded wing's local durable state")
	}
	if strings.Contains(string(docs), "the only place it can send them is the agent's own API") {
		t.Fatal("public docs overstate credential protection when operators can broaden the network allowlist")
	}
	install, err := os.ReadFile(filepath.Join(root, "internal/relay/templates/install.html"))
	if err != nil {
		t.Fatal(err)
	}
	installText := string(install)
	for _, phrase := range []string{"wt roost start --https", "wt mcp connect", "hosted browser terminal and control relay require relay access", "certutil"} {
		if !strings.Contains(installText, phrase) {
			t.Errorf("public install flow must contain %q", phrase)
		}
	}
	if strings.Contains(strings.ToLower(installText), "single binary, no dependencies") {
		t.Fatal("public install flow hides optional HTTPS and agent CLI dependencies")
	}
}

func TestFlyOperationsDocumentationMatchesActiveConfiguration(t *testing.T) {
	root := repositoryRoot(t)
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	fly := read("fly.toml")
	if !regexp.MustCompile(`(?m)^\s*login\s*=`).MatchString(fly) {
		t.Fatal("fly.toml must define the active login process")
	}
	if regexp.MustCompile(`(?m)^\s*edge\s*=`).MatchString(fly) {
		t.Fatal("the checked-in Fly contract expects the optional edge process to remain disabled")
	}

	httpStart := strings.Index(fly, "[http_service]")
	if httpStart < 0 {
		t.Fatal("fly.toml has no http_service section")
	}
	httpEnd := strings.Index(fly[httpStart+1:], "\n[")
	if httpEnd < 0 {
		httpEnd = len(fly) - httpStart - 1
	}
	httpService := fly[httpStart : httpStart+1+httpEnd]
	if !strings.Contains(httpService, `processes = ["login"]`) || strings.Contains(httpService, `"edge"`) {
		t.Fatalf("checked-in http_service must route only to login:\n%s", httpService)
	}
	for _, phrase := range []string{
		`processes = ["login"]`,
		`processes = ["login", "edge"]`,
		"ordinary `fly deploy` of the checked-in file does not create or route",
		"before creating edge machines",
	} {
		if !strings.Contains(read("docs/fly-ops.md"), phrase) {
			t.Errorf("docs/fly-ops.md must contain %q", phrase)
		}
	}

	makefile := read("Makefile")
	for _, phrase := range []string{"edge process is disabled in fly.toml", "edge is not attached to http_service.processes"} {
		if !strings.Contains(makefile, phrase) {
			t.Errorf("deploy-edge must guard the inactive Fly topology with %q", phrase)
		}
	}

	serve := read("cmd/wt/serve.go")
	detect := strings.Index(serve, `if runtime.flyMachineID != "" && runtime.nodeRole == ""`)
	load := strings.Index(serve, "cfg, err := config.Load()")
	if detect < 0 || load < 0 || detect > load {
		t.Fatal("Fly role detection must remain before config.Load so config initialization cannot fabricate /data")
	}
}

func TestCompatibilityDocumentationNamesTheConfiguredBaseline(t *testing.T) {
	root := repositoryRoot(t)
	checks := []struct {
		path        string
		mustHave    string
		mustNotHave []string
	}{
		{path: "Makefile", mustHave: "configured historical", mustNotHave: []string{"against the last published"}},
		{path: "docs/fly-ops.md", mustHave: "configured historical-baseline", mustNotHave: []string{"real N-1/current"}},
		{path: "docs/testing.md", mustHave: "configured-baseline and candidate", mustNotHave: []string{"real last-release", "live N-1/N gateway-wing"}},
		{path: "docs/direct-agent-manager-design.md", mustHave: "configured historical baseline", mustNotHave: []string{"runs real N-1 and candidate"}},
		{path: "docs/bryan-wingthing-direct-control-field-report.md", mustHave: "configured historical-baseline/candidate", mustNotHave: []string{"real N-1/candidate"}},
	}
	for _, check := range checks {
		data, err := os.ReadFile(filepath.Join(root, check.path))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if !strings.Contains(text, check.mustHave) {
			t.Errorf("%s must name %q", check.path, check.mustHave)
		}
		for _, stale := range check.mustNotHave {
			if strings.Contains(text, stale) {
				t.Errorf("%s overstates the pinned compatibility gate with %q", check.path, stale)
			}
		}
	}

	script, err := os.ReadFile(filepath.Join(root, "scripts/test-backward-compat.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), `BASELINE_REF="${WT_COMPAT_BASELINE_REF:-`) {
		t.Fatal("compatibility script must keep an explicit overridable baseline")
	}
}

func TestCurrentDesignDocsDoNotHardCodeMCPToolCounts(t *testing.T) {
	root := repositoryRoot(t)
	// Tool membership is a tested contract in internal/control. Numeric prose
	// counts drift without explaining which adapter or version they describe.
	countClaim := regexp.MustCompile(`(?i)(?:all\s+)?\d+\s+(?:local\s+)?(?:mcp\s+)?tool(?:s|\s+schemas)\b`)
	for _, rel := range []string{
		"docs/agent-manager-product-brief.md",
		"docs/agent-meta-layer.md",
		"docs/ai-api-surface.md",
		"docs/testing.md",
	} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if claim := countClaim.Find(data); claim != nil {
			t.Errorf("%s hard-codes an MCP tool count %q; name the registry-defined operation set instead", rel, claim)
		}
	}
}

func TestSandboxDocumentationMatchesImplementedBoundary(t *testing.T) {
	root := repositoryRoot(t)
	checks := []struct {
		path        string
		mustHave    []string
		mustNotHave []string
	}{
		{
			path:        "docs/skills/create-egg/SKILL.md",
			mustHave:    []string{"HOME write isolation", "not a filesystem allowlist", "locations outside HOME", "one uniform raw-networking mode", "route-less namespace", "no Seatbelt network deny"},
			mustNotHave: []string{"root filesystem read-only", "read-only root mount", "Unrestricted network defeats"},
		},
		{
			path:        "docs/egg-inheritance-design.md",
			mustHave:    []string{"broadest platform policy", "any CONNECT destination on Linux without a general route", "macOS emits no Seatbelt network deny"},
			mustNotHave: []string{`"*" in any layer = full network`},
		},
		{
			path:        "docs/container-mode.md",
			mustHave:    []string{"fresh mount namespace starts with a cloned view"},
			mustNotHave: []string{"inherits the parent's mount namespace"},
		},
		{
			path:        "docs/remote-tmux.md",
			mustHave:    []string{"Free native MCP is separate", "browser-direct terminal transport has not shipped"},
			mustNotHave: []string{"Today, all bytes flow through the roost", "The roost transits all traffic today"},
		},
		{
			path:     "docs/sandbox.md",
			mustHave: []string{"filesystem-denial and Unix-socket", "`network-outbound` rules", "overlapping mandatory deny still wins", "listing only `localhost` does not forward every host"},
		},
	}
	for _, check := range checks {
		data, err := os.ReadFile(filepath.Join(root, check.path))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, phrase := range check.mustHave {
			if !strings.Contains(text, phrase) {
				t.Errorf("%s must contain %q", check.path, phrase)
			}
		}
		for _, phrase := range check.mustNotHave {
			if strings.Contains(text, phrase) {
				t.Errorf("%s contains stale sandbox claim %q", check.path, phrase)
			}
		}
	}
}

func TestArchitectureDocumentationDistinguishesHistoryTargetsAndShippedBehavior(t *testing.T) {
	root := repositoryRoot(t)
	checks := []struct {
		path     string
		mustHave []string
	}{
		{
			path:     "docs/vt_design.md",
			mustHave: []string{"Status: historical implementation design", "VTerm snapshots and bounded scrollback by default"},
		},
		{
			path:     "docs/egg-sandbox-design.md",
			mustHave: []string{"permissive default below did not ship", "[sandbox reference](sandbox.md) for current behavior"},
		},
		{
			path:     "docs/egg-inheritance-design.md",
			mustHave: []string{"Deferred; this syntax is not implemented", "`domain:port` and `*:port` are not accepted policy forms"},
		},
		{
			path:     "docs/local-first-architecture.md",
			mustHave: []string{"not an automatic fallback claim", "never changes to the hosted relay"},
		},
		{
			path:     "docs/sandboxed-ai-vm.md",
			mustHave: []string{"A free account uses that connection", "wt mcp connect", "An account with hosted-relay access"},
		},
	}

	for _, check := range checks {
		data, err := os.ReadFile(filepath.Join(root, check.path))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, phrase := range check.mustHave {
			if !strings.Contains(text, phrase) {
				t.Errorf("%s must contain %q", check.path, phrase)
			}
		}
	}
}

func TestReleasePublishesArtifactsFromTheDeterministicGate(t *testing.T) {
	root := repositoryRoot(t)
	data, err := os.ReadFile(filepath.Join(root, ".github/workflows/release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	for _, phrase := range []string{
		"actions/upload-artifact@v4",
		"actions/download-artifact@v4",
		"wingthing-release-${{ github.sha }}",
		"if-no-files-found: error",
	} {
		if !strings.Contains(workflow, phrase) {
			t.Errorf("release workflow must contain %q", phrase)
		}
	}
	if count := strings.Count(workflow, "make release"); count != 1 {
		t.Fatalf("release workflow builds artifacts %d times, want exactly once in the gated job", count)
	}
}

func TestContainerDefaultPreservesStateAndLocalBoundary(t *testing.T) {
	root := repositoryRoot(t)
	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	image := string(dockerfile)
	for _, phrase := range []string{"ENV HOME=/data", "umask 077", "exec ./wt serve"} {
		if !strings.Contains(image, phrase) {
			t.Errorf("Dockerfile must contain %q", phrase)
		}
	}
	if strings.Contains(image, "exec ./wt serve --addr :8080") {
		t.Fatal("standalone image bypasses wt serve's implicit loopback-only local listener")
	}

	docs, err := os.ReadFile(filepath.Join(root, "internal/relay/templates/docs.html"))
	if err != nil {
		t.Fatal(err)
	}
	public := string(docs)
	for _, phrase := range []string{
		"docker run --network host -v wt-data:/data wingthing",
		"The image runs the gateway only",
		"Do not publish a no-login container",
	} {
		if !strings.Contains(public, phrase) {
			t.Errorf("container documentation must contain %q", phrase)
		}
	}
}
