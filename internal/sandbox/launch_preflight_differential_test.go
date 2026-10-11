//go:build darwin

package sandbox_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/launchplan"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
}
func putJSON(t *testing.T, path string, body any) {
	t.Helper()
	b, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	put(t, path, string(b))
}
func fixture(t *testing.T) (launchplan.Request, launchplan.Capabilities, string) {
	t.Helper()
	// Production canonicalizes launch paths. Resolve macOS's temporary-parent
	// aliases before deriving the oracle's paths, while individual symlink
	// cases still create their own deliberate aliases below this root.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	work := filepath.Join(home, "work")
	tmp := filepath.Join(dir, "tmp")
	for _, p := range []string{home, work, tmp} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", tmp)
	t.Setenv("WINGTHING_DIR", filepath.Join(dir, "unused-state"))
	oldChannel := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = oldChannel })
	bin := filepath.Join(dir, "codex")
	body := "DO NOT RUN THIS SENTINEL EXECUTABLE"
	put(t, bin, body)
	sum := sha256.Sum256([]byte(body))
	req := launchplan.Request{Route: "agent_run", Agent: "codex", CWD: work, Home: home, MergeHome: home, AllowedHomeRoot: dir, Executable: bin, Unattended: true, PolicyPath: filepath.Join(dir, "egg.yaml"), CapabilitiesPath: filepath.Join(dir, "facts.json"), EvidencePath: filepath.Join(dir, "evidence.json")}
	caps := launchplan.Capabilities{OS: "darwin", Network: "full", WorkspaceWritable: launchplan.Supported, ProviderHomeWritable: launchplan.Supported, NativeSandbox: launchplan.Supported, ParentSandbox: "none"}
	putJSON(t, req.CapabilitiesPath, caps)
	putJSON(t, req.EvidencePath, launchplan.ProviderEvidence{BinarySHA256: hex.EncodeToString(sum[:]), VersionOutput: "codex-cli 0.159.3", HelpOutput: " --no-daemon\n --dangerously-bypass-approvals-and-sandbox\n -c, --config\n", CompleteHelp: true})
	put(t, req.PolicyPath, "fs: [rw:./]\n")
	return req, caps, dir
}
func inspect(t *testing.T, req launchplan.Request) launchplan.Report {
	t.Helper()
	r, e := launchplan.Inspect(req)
	if e != nil {
		t.Fatal(e)
	}
	putJSON(t, filepath.Join(filepath.Dir(req.CapabilitiesPath), "report.json"), r)
	return r
}
func find(t *testing.T, r launchplan.Report, code string) launchplan.Gate {
	t.Helper()
	for _, g := range r.Gates {
		if g.Code == code {
			return g
		}
	}
	t.Fatalf("missing gate %s", code)
	return launchplan.Gate{}
}
func absentFails(t *testing.T, r launchplan.Report) {
	t.Helper()
	for _, g := range r.Gates {
		if g.Status == launchplan.Fail {
			t.Fatalf("unexpected fail: %+v", g)
		}
	}
}
func runtimeConfig(t *testing.T, req launchplan.Request) *egg.EggConfig {
	t.Helper()
	c, e := egg.ResolveEggConfig(req.PolicyPath)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func rooted(cfg *egg.EggConfig, cwd string) *egg.EggConfig {
	c := *cfg
	c.FS = append([]string(nil), cfg.FS...)
	for i, r := range c.FS {
		m, p, ok := strings.Cut(r, ":")
		if !ok {
			m, p = "rw", r
		}
		if !filepath.IsAbs(p) && !strings.HasPrefix(p, "~") {
			p = filepath.Join(cwd, p)
		}
		c.FS[i] = m + ":" + p
	}
	return &c
}
func profileConfig(t *testing.T, cfg *egg.EggConfig, req launchplan.Request) sandbox.Config {
	t.Helper()
	sb := rooted(cfg, req.CWD).ToSandboxConfig(req.Home)
	if len(sb.Mounts) > 0 {
		p := egg.Profile(req.Agent)
		for _, d := range p.WriteRegex {
			v := filepath.Join(req.Home, d)
			sb.Mounts = append(sb.Mounts, sandbox.Mount{Source: v, Target: v, UseRegex: true})
		}
		for _, d := range p.WriteDirs {
			v := filepath.Join(req.Home, d)
			sb.Mounts = append(sb.Mounts, sandbox.Mount{Source: v, Target: v})
		}
	}
	return sb
}

func TestReviewDifferentMergeAndProviderHomes(t *testing.T) {
	req, _, dir := fixture(t)
	host := filepath.Join(dir, "host-home")
	if e := os.Mkdir(host, 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("HOME", host)
	req.MergeHome = host
	put(t, filepath.Join(dir, "parent.yaml"), "base: none\nfs: [deny:~/.codex]\n")
	put(t, req.PolicyPath, "base: ./parent.yaml\nfs: [rw:./, rw:"+filepath.Join(req.Home, ".codex")+"]\n")
	report := inspect(t, req)
	live := runtimeConfig(t, req)
	got := find(t, report, "provider_home.write")
	if got.Status != launchplan.Fail {
		t.Fatal(got)
	}
	if !strings.Contains(got.Source, filepath.Join(dir, "parent.yaml")) {
		t.Fatal("inherited denial lost its source")
	}
	sb := profileConfig(t, live, req)
	expected := filepath.Join(req.Home, ".codex")
	if !reflect.DeepEqual(sb.Deny, []string{expected}) {
		t.Fatalf("runtime denial lost: %v", sb.Deny)
	}
	put(t, filepath.Join(dir, "runtime.sbpl"), sandbox.ReviewProfile(sb))
	t.Logf("preflight provider_home.write=%s; production resolver keeps deny:~/.codex; final profile denies %s", got.Status, expected)
}

func TestReviewFilteredElanHome(t *testing.T) {
	req, caps, dir := fixture(t)
	foreign := filepath.Join(dir, "foreign-elan")
	if e := os.Mkdir(foreign, 0700); e != nil {
		t.Fatal(e)
	}
	caps.ElanHome = foreign
	caps.ElanHomeWritable = launchplan.Unsupported
	putJSON(t, req.CapabilitiesPath, caps)
	t.Setenv("ELAN_HOME", foreign)
	put(t, req.PolicyPath, "fs: [rw:./, deny:"+foreign+"]\n")
	report := inspect(t, req)
	live := runtimeConfig(t, req)
	env := live.BuildEnvMap(req.Home)
	if _, ok := env["ELAN_HOME"]; ok {
		t.Fatal("runtime unexpectedly passes ELAN_HOME")
	}
	for _, k := range egg.Profile(req.Agent).EnvVars {
		if k == "ELAN_HOME" {
			t.Fatal("profile injects ELAN_HOME")
		}
	}
	got := find(t, report, "environment.elan_home.filtered")
	if got.Status != launchplan.Pass {
		t.Fatal(got)
	}
	absentFails(t, report)
	t.Logf("preflight readiness=%s, environment.elan_home.filtered=%s; production BuildEnvMap filters ELAN_HOME and Codex profile does not restore it", report.Readiness, got.Status)
}

func TestReviewUnrelatedNetworkAllowlist(t *testing.T) {
	req, _, _ := fixture(t)
	put(t, req.PolicyPath, "network: {domains: [unrelated.example], agent_domains: none}\n")
	report := inspect(t, req)
	if find(t, report, "network.provider").Status != launchplan.Fail {
		t.Fatal("preflight missed enforcing proxy refusal")
	}
	live, err := egg.ResolvePolicyWithProvider(runtimeConfig(t, req), "codex", req.Home, "")
	if err != nil {
		t.Fatal(err)
	}
	status := sandbox.ReviewProxyStatus(live.Domains, "api.openai.com:443")
	if status != 403 {
		t.Fatalf("expected refusal, got %d", status)
	}
	t.Logf("preflight readiness=%s, network=%s; actual production proxy CONNECT api.openai.com:443=%d", report.Readiness, report.Network, status)
}

func TestReviewConfiguredRootRelativePolicy(t *testing.T) {
	req, _, dir := fixture(t)
	req.Route = "terminal_start"
	req.Agent = ""
	req.EvidencePath = ""
	req.Unattended = false
	req.Executable = "/bin/sh"
	root := req.CWD
	req.PolicyBase = root
	req.CWD = filepath.Join(root, "private")
	if e := os.Mkdir(req.CWD, 0700); e != nil {
		t.Fatal(e)
	}
	req.PolicyPath = filepath.Join(root, "egg.yaml")
	put(t, req.PolicyPath, "fs: [rw:./, deny:./private]\n")
	report := inspect(t, req)
	got := find(t, report, "workspace.write")
	if got.Status != launchplan.Fail || report.Readiness != "blocked" {
		t.Fatalf("unexpected verdict: %s %+v", report.Readiness, got)
	}
	wc := &config.WingConfig{Paths: config.PathList{{Path: root}}}
	start := ws.PTYStart{CWD: req.CWD, OrgRole: "owner"}
	live, _, err := eggclient.PrepareMCPLaunch(wc, &start, req.Home, false, egg.DefaultEggConfig())
	if err != nil {
		t.Fatal(err)
	}
	sb := profileConfig(t, live, req)
	expected := req.CWD
	found := false
	for _, d := range sb.Deny {
		if d == expected {
			found = true
		}
	}
	if !found {
		t.Fatalf("runtime root denial missing: %v", sb.Deny)
	}
	put(t, filepath.Join(dir, "runtime.sbpl"), sandbox.ReviewProfile(sb))
	t.Logf("preflight readiness="+report.Readiness+", workspace.write=%s; actual PrepareMCPLaunch roots deny:./private at configured %s and denies cwd %s", got.Status, root, expected)
}

func TestReviewCodexStateDirectoryIsFile(t *testing.T) {
	req, _, _ := fixture(t)
	put(t, filepath.Join(req.Home, ".codex"), "existing regular file")
	report := inspect(t, req)
	got := find(t, report, "provider_home.write")
	if got.Status != launchplan.Fail {
		t.Fatal(got)
	}
	if find(t, report, "provider.preparation").Status != launchplan.Fail {
		t.Fatal("missed runtime preparation failure")
	}
	_, _, err := egg.CodexRunArgs([]string{"--dangerously-bypass-approvals-and-sandbox"}, req.Home, "launch-plan")
	if err == nil {
		t.Fatal("runtime unexpectedly prepares a directory beneath a regular file")
	}
	t.Logf("preflight provider_home.write=%s and provider.preparation=%s; actual CodexRunArgs fails before launch: %v", got.Status, find(t, report, "provider.preparation").Status, err)
}

func TestReviewMacPinnedProviderDirectory(t *testing.T) {
	req, _, dir := fixture(t)
	state := filepath.Join(req.Home, ".codex")
	if e := os.Mkdir(state, 0700); e != nil {
		t.Fatal(e)
	}
	put(t, req.PolicyPath, "fs: [deny-write:~/.codex]\n")
	report := inspect(t, req)
	got := find(t, report, "provider_home.write")
	if got.Status != launchplan.Pass {
		t.Fatal(got)
	}
	absentFails(t, report)
	_, native, err := egg.CodexRunArgs([]string{"--dangerously-bypass-approvals-and-sandbox"}, req.Home, "launch-plan")
	if err != nil || !native {
		t.Fatalf("prepare: %v", err)
	}
	profile := sandbox.ReviewProfile(profileConfig(t, runtimeConfig(t, req), req))
	put(t, filepath.Join(dir, "runtime.sbpl"), profile)
	if !strings.Contains(profile, "(allow file-write* (subpath "+`"`+state+`"`+"))") || !strings.Contains(profile, "(deny file-write* (literal "+`"`+state+`"`+"))") || strings.Contains(profile, "(deny file-write* (subpath "+`"`+state+`"`+"))") {
		t.Fatalf("unexpected state profile: %s", profile)
	}
	t.Logf("preflight readiness=%s, provider_home.write=%s; actual CodexRunArgs succeeds and production SBPL permits state descendants, pinning only directory entry", report.Readiness, got.Status)
}

func TestReviewProviderDirectorySymlink(t *testing.T) {
	req, _, dir := fixture(t)
	actual := filepath.Join(req.Home, "actual-codex")
	if e := os.Mkdir(actual, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(actual, filepath.Join(req.Home, ".codex")); e != nil {
		t.Fatal(e)
	}
	report := inspect(t, req)
	got := find(t, report, "provider_home.write")
	if got.Status != launchplan.Pass {
		t.Fatal(got)
	}
	profile := sandbox.ReviewProfile(profileConfig(t, runtimeConfig(t, req), req))
	put(t, filepath.Join(dir, "runtime.sbpl"), profile)
	if !strings.Contains(profile, "(allow file-write* (subpath "+`"`+actual+`"`+"))") {
		t.Fatalf("runtime canonical grant missing: %s", profile)
	}
	t.Logf("preflight provider_home.write=%s: %s; actual profile grants canonical symlink target %s", got.Status, got.Reason, actual)
}

func TestReviewSymlinkedParentParity(t *testing.T) {
	req, _, dir := fixture(t)
	alias := filepath.Join(dir, "parent-alias")
	if e := os.Symlink(req.Home, alias); e != nil {
		t.Fatal(e)
	}
	req.Home = alias
	req.MergeHome = alias
	req.CWD = filepath.Join(alias, "work")
	req.PolicyPath = filepath.Join(req.CWD, "egg.yaml")
	put(t, req.PolicyPath, "fs: [deny:./]\n")
	report := inspect(t, req)
	got := find(t, report, "workspace.write")
	if got.Status != launchplan.Fail {
		t.Fatal(got)
	}
	start := ws.PTYStart{CWD: req.CWD, OrgRole: "owner"}
	live, _, err := eggclient.PrepareMCPLaunch(&config.WingConfig{}, &start, req.Home, false, egg.DefaultEggConfig())
	if err != nil {
		t.Fatal(err)
	}
	if start.CWD != report.CWD.Resolved {
		t.Fatalf("canonical cwd differs: runtime=%s plan=%s", start.CWD, report.CWD.Resolved)
	}
	sb := rooted(live, start.CWD).ToSandboxConfig(req.Home)
	found := false
	for _, d := range sb.Deny {
		if d == report.CWD.Resolved {
			found = true
		}
	}
	if !found {
		t.Fatalf("runtime cwd denial differs: %v", sb.Deny)
	}
	t.Logf("PASS control: symlinked parent created inside test; runtime and preflight canonical cwd=%s, both retain cwd deny", start.CWD)
}

func TestReviewSelectedElanHomeMatchesEnv(t *testing.T) {
	req, caps, dir := fixture(t)
	foreign := filepath.Join(dir, "foreign-elan")
	if e := os.Mkdir(foreign, 0700); e != nil {
		t.Fatal(e)
	}
	caps.ElanHome = foreign
	caps.ElanHomeWritable = launchplan.Unsupported
	putJSON(t, req.CapabilitiesPath, caps)
	t.Setenv("ELAN_HOME", foreign)
	put(t, req.PolicyPath, "fs: [rw:./, deny:"+foreign+"]\nenv: [ELAN_HOME]\n")
	report := inspect(t, req)
	live := runtimeConfig(t, req)
	env := live.BuildEnvMap(req.Home)
	if env["ELAN_HOME"] != foreign {
		t.Fatal("runtime drops selected ELAN_HOME")
	}
	if find(t, report, "environment.elan_home").Status != launchplan.Fail {
		t.Fatal("preflight misses selected denied ELAN_HOME")
	}
	t.Log("PASS control: runtime inherits explicitly selected ELAN_HOME; preflight correctly reports its denied path")
}

func TestReviewMissingBinaryMatchesRuntimeLookPath(t *testing.T) {
	req, _, dir := fixture(t)
	req.Executable = filepath.Join(dir, "missing-codex")
	report := inspect(t, req)
	if find(t, report, "executable").Status != launchplan.Fail {
		t.Fatal("preflight misses missing executable")
	}
	if _, err := exec.LookPath(req.Executable); err == nil {
		t.Fatal("runtime accepts missing absolute executable")
	}
	t.Log("PASS control: preflight executable FAIL agrees with production exec.LookPath for explicit missing binary")
}

func inventory(t *testing.T, root string) map[string]string {
	t.Helper()
	entries := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		i, e := d.Info()
		if e != nil {
			return e
		}
		content := ""
		if i.Mode().IsRegular() {
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			sum := sha256.Sum256(b)
			content = hex.EncodeToString(sum[:])
		}
		if i.Mode()&os.ModeSymlink != 0 {
			content, e = os.Readlink(path)
			if e != nil {
				return e
			}
		}
		entries[path] = fmt.Sprintf("%d %d %d %s", i.Mode(), i.Size(), i.ModTime().UnixNano(), content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
func TestReviewPreflightPreservesInputsAndAbsentState(t *testing.T) {
	req, _, dir := fixture(t)
	put(t, filepath.Join(req.Home, "auth.json"), "SENTINEL_CREDENTIAL_CONTENT")
	req.CredentialPaths = []string{filepath.Join(req.Home, "auth.json")}
	absent := filepath.Join(dir, "absent-wing")
	t.Setenv("WINGTHING_DIR", absent)
	t.Setenv("CODEX_HOME", filepath.Join(dir, "ambient-provider"))
	t.Setenv("ELAN_HOME", filepath.Join(dir, "ambient-elan"))
	before := inventory(t, dir)
	first, err := launchplan.Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := launchplan.Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, inventory(t, dir)) {
		t.Fatal("preflight changed input inventory")
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("unstable identical inputs")
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatal("preflight created ambient state")
	}
	data, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "SENTINEL_CREDENTIAL_CONTENT") {
		t.Fatal("preflight leaked credential contents")
	}
	t.Log("PASS control: two Inspect calls leave modes, sizes, mtimes, hashes and absent ambient state unchanged")
}
