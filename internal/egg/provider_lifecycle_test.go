package egg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The fake CLIs read the actual invocation config and execute its generated
// hooks/plugin. No installed provider, credentials or model service is used.
const fakeLifecycleCLI = `import {readFileSync} from 'node:fs';
import {spawnSync} from 'node:child_process';
import {createInterface} from 'node:readline';
const provider = process.argv[2];
let plugin, settings;
if (provider === 'gemini') settings = JSON.parse(readFileSync(process.env.GEMINI_CLI_SYSTEM_DEFAULTS_PATH));
else {
  settings = JSON.parse(process.env.OPENCODE_CONFIG_CONTENT);
  plugin = await (await import(settings.plugin.at(-1))).WingthingLifecycle();
}
console.log('ready');
for await (const line of createInterface({input:process.stdin})) {
  const input = JSON.parse(line);
  if (provider === 'gemini') {
    const hooks = settings.hooks[input.hook_event_name];
    if (settings.hooksConfig?.enabled !== false) for (const d of hooks) for (const h of d.hooks) {
      if (h.name !== 'wingthing-lifecycle') continue;
      const r = spawnSync('/bin/sh', ['-c', h.command], {input:JSON.stringify(input), encoding:'utf8'});
      if (r.status || r.stdout || r.stderr) throw Error('observational hook failed');
    }
  } else if (input.type === 'prompt') await plugin['chat.message']({sessionID:input.sessionID});
  else if (input.type === 'dispose') await plugin.dispose();
  else await plugin.event({event:input});
  console.log('ok');
}
`

func fakeLifecycleBinary(t *testing.T, provider, version string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-cli")
	body := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%s\\n' " + shellQuoteLifecycle(version) + "; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

type nativeLifecycleFixture struct {
	t        *testing.T
	provider string
	home     string
	dir      string
	input    io.WriteCloser
	output   *bufio.Reader
}

func startLifecycleFixture(t *testing.T, provider, resumeID string) *nativeLifecycleFixture {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("fake native plugin fixture requires Node")
	}
	home, dir := t.TempDir(), t.TempDir()
	binary := fakeLifecycleBinary(t, provider, providerLifecycleVersions[provider])
	script := filepath.Join(filepath.Dir(binary), "fixture.mjs")
	if err := os.WriteFile(script, []byte(fakeLifecycleCLI), 0600); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body, []byte("exit 1\n"), []byte("exec nice -n 15 "+shellQuoteLifecycle(node)+" "+shellQuoteLifecycle(script)+" "+shellQuoteLifecycle(provider)+"\n"), 1)
	if err := os.WriteFile(binary, body, 0700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HOME": home, "PATH": os.Getenv("PATH")}
	if provider == "gemini" {
		env["GEMINI_CLI_SYSTEM_DEFAULTS_PATH"] = filepath.Join(home, "missing-defaults")
	}
	if err := prepareProviderLifecycle(RunConfig{Agent: provider, ResumeSessionID: resumeID}, binary, dir, nil, env); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, binary)
	cmd.Dir = home
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("fake %s: %v: %s", provider, err, stderr.String())
		}
		cancel()
	})
	f := &nativeLifecycleFixture{t: t, provider: provider, home: home, dir: dir, input: input, output: bufio.NewReader(output)}
	if line, err := f.output.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("fake CLI startup: %q, %v", line, err)
	}
	return f
}

func (f *nativeLifecycleFixture) fire(event any) {
	f.t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.input.Write(append(data, '\n')); err != nil {
		f.t.Fatal(err)
	}
	if line, err := f.output.ReadString('\n'); err != nil || line != "ok\n" {
		f.t.Fatalf("fake CLI hook: %q, %v", line, err)
	}
}

func (f *nativeLifecycleFixture) read(status string) SessionView {
	f.t.Helper()
	v, err := ReadSessionLifecycle(f.dir, f.provider, "/fixture", f.home, "", true, 0, 100)
	if err != nil {
		f.t.Fatal(err)
	}
	if v.Status != status {
		f.t.Fatalf("%s: got %+v, want %s", f.provider, v, status)
	}
	if status != "unknown" && status != "exited" && (v.StateSource != f.provider+"_hook" || v.ProviderSessionID != "session-exact") {
		f.t.Fatalf("hook source/identity missing: %+v", v)
	}
	return v
}

func TestGeminiLifecycleFakeCLITransitions(t *testing.T) {
	f := startLifecycleFixture(t, "gemini", "")
	f.read("unknown")
	for _, step := range []struct{ event, notification, tool, reason, status string }{
		{event: "SessionStart", status: "idle"},
		{event: "BeforeAgent", status: "working"},
		{event: "Notification", notification: "ToolPermission", status: "blocked"},
		{event: "BeforeTool", tool: "run_shell_command", status: "unknown"},
		{event: "AfterTool", tool: "run_shell_command", status: "unknown"},
		{event: "BeforeModel", status: "working"},
		{event: "BeforeTool", tool: "ask_user", status: "blocked"},
		{event: "BeforeModel", status: "working"},
		{event: "AfterAgent", status: "idle"},
		{event: "SessionEnd", reason: "exit", status: "done"},
	} {
		f.fire(map[string]string{"hook_event_name": step.event, "session_id": "session-exact", "notification_type": step.notification, "tool_name": step.tool, "reason": step.reason})
		f.read(step.status)
	}
	f.fire(map[string]string{"hook_event_name": "BeforeAgent", "session_id": "foreign-child"})
	v := f.read("done")
	if replay, err := ReadSessionLifecycle(f.dir, f.provider, "", f.home, "", true, v.HeadCursor, 100); err != nil || len(replay.Events) != 0 {
		t.Fatalf("duplicate import: %+v, %v", replay, err)
	}
	if err := recordSessionProcessExit(f.dir, 137, false); err != nil {
		t.Fatal(err)
	}
	f.read("exited")
}

func TestGeminiLifecycleUnverifiedTransitionsStayUnknown(t *testing.T) {
	f := startLifecycleFixture(t, "gemini", "")
	f.fire(map[string]string{"hook_event_name": "SessionStart", "session_id": "session-exact"})
	f.fire(map[string]any{"hook_event_name": "BeforeTool", "session_id": "session-exact", "mcp_context": map[string]string{"server_name": "fixture"}})
	f.read("unknown")
	f.fire(map[string]string{"hook_event_name": "SessionEnd", "session_id": "session-exact", "reason": "clear"})
	f.read("unknown")
}

func TestOpenCodeLifecycleFakeCLITransitions(t *testing.T) {
	f := startLifecycleFixture(t, "opencode", "")
	f.read("unknown")
	f.fire(map[string]any{"type": "session.created", "properties": map[string]any{"sessionID": "foreign-child", "info": map[string]string{"id": "foreign-child", "parentID": "parent"}}})
	f.read("unknown")
	f.fire(map[string]any{"type": "session.created", "properties": map[string]any{"sessionID": "session-exact", "info": map[string]string{"id": "session-exact"}}})
	f.read("idle")
	f.fire(map[string]string{"type": "prompt", "sessionID": "session-exact"})
	f.read("working")
	for _, step := range []struct{ event, id, request, state, status string }{
		{event: "permission.asked", id: "permission-1", status: "blocked"},
		{event: "question.asked", id: "question-1", status: "blocked"},
		{event: "session.status", state: "busy", status: "blocked"},
		{event: "permission.replied", request: "permission-1", status: "blocked"},
		{event: "question.replied", request: "question-1", status: "working"},
		{event: "session.status", state: "retry", status: "working"},
		{event: "session.status", state: "idle", status: "idle"},
	} {
		f.fire(map[string]any{"type": step.event, "properties": map[string]any{"sessionID": "session-exact", "id": step.id, "requestID": step.request, "status": map[string]string{"type": step.state}}})
		f.read(step.status)
	}
	f.fire(map[string]string{"type": "prompt", "sessionID": "foreign-child"})
	f.read("idle")
	f.fire(map[string]string{"type": "dispose"})
	v := f.read("done")
	if replay, err := ReadSessionLifecycle(f.dir, f.provider, "", f.home, "", false, v.HeadCursor, 100); err != nil || len(replay.Events) != 0 || replay.Status != "done" {
		t.Fatalf("duplicate/end import: %+v, %v", replay, err)
	}
}

func TestOpenCodeLifecycleResumeAndInterruptedExit(t *testing.T) {
	f := startLifecycleFixture(t, "opencode", "session-exact")
	f.read("idle")
	f.fire(map[string]string{"type": "prompt", "sessionID": "session-exact"})
	f.read("working")
	f.fire(map[string]string{"type": "dispose"})
	if err := recordSessionProcessExit(f.dir, 0, false); err != nil {
		t.Fatal(err)
	}
	f.read("exited")
}

func TestOpenCodeLifecycleErrorDoesNotBecomeCompletion(t *testing.T) {
	f := startLifecycleFixture(t, "opencode", "session-exact")
	f.fire(map[string]string{"type": "prompt", "sessionID": "session-exact"})
	f.fire(map[string]any{"type": "session.error", "properties": map[string]string{"sessionID": "session-exact"}})
	f.read("unknown")
	f.fire(map[string]any{"type": "session.status", "properties": map[string]any{"sessionID": "session-exact", "status": map[string]string{"type": "idle"}}})
	f.read("unknown")
	f.fire(map[string]string{"type": "dispose"})
	if err := recordSessionProcessExit(f.dir, 0, false); err != nil {
		t.Fatal(err)
	}
	f.read("exited")
}

func TestProviderLifecycleVersionGating(t *testing.T) {
	for provider, verified := range providerLifecycleVersions {
		for _, version := range []string{verified, "0.1.0", "99.0.0", verified + "-preview", "unknown", ""} {
			t.Run(provider+"/"+version, func(t *testing.T) {
				binary := fakeLifecycleBinary(t, provider, version)
				want := version == verified
				if got := providerLifecycleSupported(provider, binary); got != want {
					t.Fatalf("version %q: supported=%v, want %v", version, got, want)
				}
				env := map[string]string{"HOME": t.TempDir()}
				env["GEMINI_CLI_SYSTEM_DEFAULTS_PATH"] = filepath.Join(env["HOME"], "missing")
				before := env["GEMINI_CLI_SYSTEM_DEFAULTS_PATH"]
				if err := prepareProviderLifecycle(RunConfig{Agent: provider}, binary, t.TempDir(), nil, env); err != nil {
					t.Fatal(err)
				}
				installed := env["GEMINI_CLI_SYSTEM_DEFAULTS_PATH"] != before || env["OPENCODE_CONFIG_CONTENT"] != ""
				if installed != want {
					t.Fatalf("unverified version injected config: %v, want %v", installed, want)
				}
			})
		}
	}
	if providerLifecycleSupported("cursor", fakeLifecycleBinary(t, "cursor", "2026.02.13-41ac335")) {
		t.Fatal("Cursor has no invocation hook contract")
	}
}

func TestProviderLifecyclePreservesInvocationConfig(t *testing.T) {
	home := t.TempDir()
	defaults := filepath.Join(home, "system-defaults.json")
	content := `{"model":{"name":"keep"}, /* retained settings */ "hooksConfig":{"enabled":false}, "hooks":{"AfterAgent":[{"hooks":[{"type":"command","command":"echo keep"}]}]}}`
	if err := os.WriteFile(defaults, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"GEMINI_CLI_SYSTEM_DEFAULTS_PATH": defaults, "GEMINI_CLI_SYSTEM_SETTINGS_PATH": "/keep/settings.json"}
	if err := prepareGeminiLifecycleEnv(home, "fixture", env); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(defaults)
	if err != nil || string(data) != content {
		t.Fatalf("original defaults changed: %q, %v", data, err)
	}
	data, err = os.ReadFile(env["GEMINI_CLI_SYSTEM_DEFAULTS_PATH"])
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Model       map[string]string
		HooksConfig map[string]bool
		Hooks       map[string][]json.RawMessage
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Model["name"] != "keep" || settings.HooksConfig["enabled"] || len(settings.Hooks["AfterAgent"]) != 2 || env["GEMINI_CLI_SYSTEM_SETTINGS_PATH"] != "/keep/settings.json" {
		t.Fatalf("defaults/policy/hooks lost: %+v", settings)
	}
	env = map[string]string{"OPENCODE_CONFIG": "/keep/opencode.json", "OPENCODE_CONFIG_CONTENT": `{"model":"keep/model", "plugin":["file:///keep.mjs"], "permission":{"*":"ask"}}`}
	if err := prepareOpenCodeLifecycleEnv(home, "fixture", "", env); err != nil {
		t.Fatal(err)
	}
	var config struct {
		Model      string
		Plugin     []string
		Permission map[string]string
	}
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatal(err)
	}
	if config.Model != "keep/model" || config.Permission["*"] != "ask" || len(config.Plugin) != 2 || config.Plugin[0] != "file:///keep.mjs" || env["OPENCODE_CONFIG"] != "/keep/opencode.json" {
		t.Fatalf("OpenCode invocation config lost: %+v", config)
	}
}

func TestProviderLifecycleRespectsExplicitCommandAndPure(t *testing.T) {
	for _, tc := range []struct {
		provider string
		command  []string
		args     []string
	}{
		{provider: "gemini", command: []string{"sh"}},
		{provider: "opencode", command: []string{"sh"}},
		{provider: "opencode", args: []string{"--pure"}},
		{provider: "opencode", args: []string{"--pure=true"}},
	} {
		binary := fakeLifecycleBinary(t, tc.provider, providerLifecycleVersions[tc.provider])
		env := map[string]string{"HOME": t.TempDir(), "OPENCODE_CONFIG_CONTENT": `{"model":"keep"}`}
		before := map[string]string{}
		for k, v := range env {
			before[k] = v
		}
		if err := prepareProviderLifecycle(RunConfig{Agent: tc.provider, Command: tc.command}, binary, t.TempDir(), tc.args, env); err != nil || !reflect.DeepEqual(env, before) {
			t.Fatalf("explicit opt-out changed invocation: %v, %+v", err, env)
		}
	}
}

func TestUnsupportedProviderLifecycleReason(t *testing.T) {
	for _, provider := range []string{"cursor", "ollama", "hermes"} {
		v, err := ReadSessionLifecycle(t.TempDir(), provider, "", t.TempDir(), "", true, 0, 100)
		if err != nil || v.Status != "unknown" || v.Ready || v.StateSource != "unsupported" || v.Reason == "" {
			t.Fatalf("%s guessed status or omitted reason: %+v, %v", provider, v, err)
		}
		if provider == "cursor" && !strings.Contains(v.Reason, "no permission-prompt hook or invocation override") {
			t.Fatalf("imprecise Cursor reason: %s", v.Reason)
		}
	}
}

func TestLifecycleConfigCommentsPreserveStrings(t *testing.T) {
	input := []byte(`{/*comment*/"url":"https://example.test/a/*b*/", "command":"echo \\\"//keep", // end
"nested":{"value":true}}`)
	got, err := lifecycleConfigObject(input)
	if err != nil || string(got["url"]) != `"https://example.test/a/*b*/"` || string(got["nested"]) != `{"value":true}` {
		t.Fatalf("comment/string parsing: %v, %s", err, got)
	}
	for _, bad := range []string{"null", "[]", "{", "{/* never closed"} {
		if _, err := lifecycleConfigObject([]byte(bad)); err == nil {
			t.Fatal(fmt.Sprintf("invalid config accepted: %s", bad))
		}
	}
}
