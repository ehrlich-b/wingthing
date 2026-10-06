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

	"github.com/ehrlich-b/wingthing/internal/sandbox"
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
  else if (input.type === 'reload') plugin = await (await import(settings.plugin.at(-1) + '?reload=1')).WingthingLifecycle();
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
		env["GEMINI_CLI_SYSTEM_SETTINGS_PATH"] = filepath.Join(home, "settings.json")
	}
	if err := prepareProviderLifecycle(RunConfig{Agent: provider, ResumeSessionID: resumeID}, binary, dir, nil, env, lifecycleProbePolicy{}); err != nil {
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

func TestGeminiLifecycleExitRequiresCompletedTurn(t *testing.T) {
	for _, events := range [][]string{{"SessionStart"}, {"SessionStart", "BeforeAgent"}, {"SessionStart", "BeforeAgent", "AfterAgent", "BeforeAgent"}} {
		for _, reason := range []string{"exit", "prompt_input_exit"} {
			t.Run(strings.Join(events, "/")+"/"+reason, func(t *testing.T) {
				f := startLifecycleFixture(t, "gemini", "")
				for _, event := range events {
					f.fire(map[string]string{"hook_event_name": event, "session_id": "session-exact"})
					f.read(map[string]string{"SessionStart": "idle", "BeforeAgent": "working", "AfterAgent": "idle"}[event])
				}
				f.fire(map[string]string{"hook_event_name": "SessionEnd", "session_id": "session-exact", "reason": reason})
				if v := f.read("unknown"); v.Reason == "" {
					t.Fatal("unverified completion omitted its reason")
				}
				if err := recordSessionProcessExit(f.dir, 0, false); err != nil {
					t.Fatal(err)
				}
				f.read("exited")
			})
		}
	}
}

func TestGeminiLifecycleCompletedTurnSurvivesIdleNotification(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprint(replay), func(t *testing.T) {
			f := startLifecycleFixture(t, "gemini", "")
			for _, event := range []string{"SessionStart", "BeforeAgent", "AfterAgent", "Notification"} {
				f.fire(map[string]string{"hook_event_name": event, "session_id": "session-exact", "notification_type": "idle_prompt"})
			}
			if replay {
				f.read("idle")
			}
			f.fire(map[string]string{"hook_event_name": "SessionEnd", "session_id": "session-exact", "reason": "exit"})
			f.read("done")
		})
	}
}

func TestOpenCodeLifecycleDisposalAndReloadPreserveBinding(t *testing.T) {
	for _, resumeID := range []string{"", "session-exact"} {
		t.Run("resume="+resumeID, func(t *testing.T) {
			f := startLifecycleFixture(t, "opencode", resumeID)
			if resumeID == "" {
				f.fire(map[string]any{"type": "session.created", "properties": map[string]any{"info": map[string]string{"id": "session-exact"}}})
			}
			f.read("idle")
			f.fire(map[string]string{"type": "dispose"})
			f.read("unknown")
			f.fire(map[string]string{"type": "reload"})
			f.read("unknown")
			f.fire(map[string]any{"type": "session.created", "properties": map[string]any{"info": map[string]string{"id": "foreign-root"}}})
			f.fire(map[string]string{"type": "prompt", "sessionID": "foreign-root"})
			f.read("unknown")
			f.fire(map[string]string{"type": "prompt", "sessionID": "session-exact"})
			f.read("working")
		})
	}
}

func TestGeminiLifecycleDefaultsStayPrivate(t *testing.T) {
	for _, mode := range []string{"explicit", "explicit-missing", "discovered", "absent"} {
		t.Run(mode, func(t *testing.T) {
			home, dir := t.TempDir(), t.TempDir()
			defaults := filepath.Join(home, "system-defaults.json")
			content := `{"mcpServers":{"private":{"token":"must-stay-private"}}}`
			if mode != "absent" && mode != "explicit-missing" {
				if err := os.WriteFile(defaults, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			env := map[string]string{"HOME": home, "GEMINI_CLI_SYSTEM_SETTINGS_PATH": filepath.Join(home, "settings.json")}
			if strings.HasPrefix(mode, "explicit") {
				env["GEMINI_CLI_SYSTEM_DEFAULTS_PATH"] = defaults
			}
			before := make(map[string]string)
			for k, v := range env {
				before[k] = v
			}
			binary := fakeLifecycleBinary(t, "gemini", providerLifecycleVersions["gemini"])
			if err := prepareProviderLifecycle(RunConfig{Agent: "gemini"}, binary, dir, nil, env, lifecycleProbePolicy{}); err != nil {
				t.Fatal(err)
			}
			if mode != "absent" {
				if !reflect.DeepEqual(env, before) {
					t.Fatalf("defaults override changed: %+v", env)
				}
				if mode != "explicit-missing" {
					data, err := os.ReadFile(defaults)
					if err != nil || string(data) != content {
						t.Fatalf("original defaults changed: %s, %v", data, err)
					}
				}
				if _, err := os.Stat(providerLifecycleHookDir("gemini", home, filepath.Base(dir))); !os.IsNotExist(err) {
					t.Fatalf("private defaults produced a spool: %v", err)
				}
				v, err := ReadSessionLifecycle(dir, "gemini", "", home, "", true, 0, 100)
				if err != nil || v.Status != "unknown" || !strings.Contains(v.Reason, "system defaults") {
					t.Fatalf("missing skip reason: %+v, %v", v, err)
				}
			} else {
				data, err := os.ReadFile(env["GEMINI_CLI_SYSTEM_DEFAULTS_PATH"])
				var settings map[string]json.RawMessage
				if err != nil || json.Unmarshal(data, &settings) != nil || len(settings) != 1 || settings["hooks"] == nil {
					t.Fatalf("expected hooks-only defaults: %s, %v", data, err)
				}
			}
		})
	}
}

func TestOpenCodeLifecycleJSONCAndInvalidConfig(t *testing.T) {
	for _, content := range []string{
		`{/* defaults */"model":"provider/model", "plugin":["file:///keep.mjs",], "permission":{"*":"ask",},}`,
		"{\"model\":\"provider/model\", // keep URL and comma\n\"plugin\":[\"file:///keep.mjs\",],\"permission\":{\"*\":\"ask\",},}",
		`{"model":`, `{"plugin":{}}`, `{"model":true,,}`, `null`,
	} {
		t.Run(content, func(t *testing.T) {
			home, dir := t.TempDir(), t.TempDir()
			env := map[string]string{"HOME": home, "OPENCODE_CONFIG_CONTENT": content}
			binary := fakeLifecycleBinary(t, "opencode", providerLifecycleVersions["opencode"])
			if err := prepareProviderLifecycle(RunConfig{Agent: "opencode"}, binary, dir, nil, env, lifecycleProbePolicy{}); err != nil {
				t.Fatalf("hook injection blocked launch: %v", err)
			}
			if strings.Contains(content, "provider/model") {
				var config struct {
					Model      string
					Plugin     []string
					Permission map[string]string
				}
				if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil || config.Model != "provider/model" || len(config.Plugin) != 2 || config.Plugin[0] != "file:///keep.mjs" || config.Permission["*"] != "ask" {
					t.Fatalf("JSONC invocation config lost: %+v, %v", config, err)
				}
			} else {
				if env["OPENCODE_CONFIG_CONTENT"] != content {
					t.Fatal("invalid config changed")
				}
				if _, err := os.Stat(providerLifecycleHookDir("opencode", home, filepath.Base(dir))); !os.IsNotExist(err) {
					t.Fatalf("invalid config produced a spool: %v", err)
				}
			}
		})
	}
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
	v := f.read("unknown")
	if replay, err := ReadSessionLifecycle(f.dir, f.provider, "", f.home, "", false, v.HeadCursor, 100); err != nil || len(replay.Events) != 0 || replay.Status != "exited" {
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
				if got, reason := providerLifecycleSupported(provider, binary, lifecycleProbePolicy{}); got != want || reason != "" {
					t.Fatalf("version %q: supported=%v, want %v", version, got, want)
				}
				env := map[string]string{"HOME": t.TempDir()}
				env["GEMINI_CLI_SYSTEM_SETTINGS_PATH"] = filepath.Join(env["HOME"], "settings.json")
				before := env["GEMINI_CLI_SYSTEM_DEFAULTS_PATH"]
				if err := prepareProviderLifecycle(RunConfig{Agent: provider}, binary, t.TempDir(), nil, env, lifecycleProbePolicy{}); err != nil {
					t.Fatal(err)
				}
				installed := env["GEMINI_CLI_SYSTEM_DEFAULTS_PATH"] != before || env["OPENCODE_CONFIG_CONTENT"] != ""
				if installed != want {
					t.Fatalf("unverified version injected config: %v, want %v", installed, want)
				}
			})
		}
	}
	if supported, _ := providerLifecycleSupported("cursor", fakeLifecycleBinary(t, "cursor", "2026.02.13-41ac335"), lifecycleProbePolicy{}); supported {
		t.Fatal("Cursor has no invocation hook contract")
	}
}

func TestLifecycleVersionProbesRespectWritableRoots(t *testing.T) {
	for _, provider := range []string{"gemini", "opencode", "codex"} {
		for _, mode := range []string{"writable", "PATH", "binary-symlink", "root-symlink", "cwd", "relative", "regex-prefix"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				root, cwd, home, dir := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
				binary, counter := filepath.Join(root, "cli"), filepath.Join(home, "probes")
				version := providerLifecycleVersions[provider]
				if provider == "codex" {
					version = "codex-cli 0.159.3"
				}
				body := "#!/bin/sh\nprintf 'probe\\n' >> " + shellQuoteLifecycle(counter) + "\nprintf '%s\\n' " + shellQuoteLifecycle(version) + "\n"
				if err := os.WriteFile(binary, []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
				policy := lifecycleProbePolicy{cwd: cwd, mounts: []sandbox.Mount{{Source: root}}}
				switch mode {
				case "PATH":
					t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
					binary = "cli"
				case "binary-symlink":
					link := filepath.Join(cwd, "cli")
					if err := os.Symlink(binary, link); err != nil {
						t.Fatal(err)
					}
					binary = link
				case "root-symlink":
					link := filepath.Join(cwd, "writable")
					if err := os.Symlink(root, link); err != nil {
						t.Fatal(err)
					}
					policy.mounts[0].Source = link
				case "cwd":
					policy.cwd, policy.mounts = root, nil
				case "relative":
					t.Chdir(root)
					binary, policy.mounts = "./cli", nil
				case "regex-prefix":
					policy.mounts[0].Source = strings.TrimSuffix(root, filepath.Base(root))
					policy.mounts[0].UseRegex = true
				}
				supported, reason := lifecycleTestSupported(provider, binary, policy)
				if supported || reason == "" {
					t.Fatalf("unsafe probe accepted: %v, %q", supported, reason)
				}
				env := map[string]string{"HOME": home}
				args := []string{"--model", "keep"}
				if provider == "codex" {
					got, err := prepareCodexLifecycle(RunConfig{Agent: provider}, binary, dir, args, home, policy)
					if err != nil || !reflect.DeepEqual(got, args) {
						t.Fatalf("skip changed invocation: %v, %v", got, err)
					}
				} else if err := prepareProviderLifecycle(RunConfig{Agent: provider}, binary, dir, args, env, policy); err != nil {
					t.Fatal(err)
				}
				if len(env) != 1 {
					t.Fatalf("skip injected config: %+v", env)
				}
				if _, err := os.Stat(counter); !os.IsNotExist(err) {
					t.Fatalf("unsafe binary was executed: %v", err)
				}
				v, err := ReadSessionLifecycle(dir, provider, cwd, home, "", true, 0, 100)
				if err != nil || v.Status != "unknown" || v.Ready || v.Reason != reason {
					t.Fatalf("skip status/reason lost: %+v, %v", v, err)
				}
			})
		}
	}
}

func lifecycleTestSupported(provider, binary string, policy lifecycleProbePolicy) (bool, string) {
	if provider == "codex" {
		return codexLifecycleSupported(binary, policy)
	}
	return providerLifecycleSupported(provider, binary, policy)
}

func TestLifecycleVersionProbeCachesRecheckPolicy(t *testing.T) {
	for _, provider := range []string{"gemini", "opencode", "codex"} {
		t.Run(provider, func(t *testing.T) {
			root, cwd := t.TempDir(), t.TempDir()
			binary, counter := filepath.Join(root, "cli"), filepath.Join(root, "probes")
			version := providerLifecycleVersions[provider]
			if provider == "codex" {
				version = "codex-cli 0.159.3"
			}
			body := "#!/bin/sh\nprintf 'probe\\n' >> " + shellQuoteLifecycle(counter) + "\nprintf '%s\\n' " + shellQuoteLifecycle(version) + "\n"
			if err := os.WriteFile(binary, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			policy := lifecycleProbePolicy{cwd: cwd, mounts: []sandbox.Mount{{Source: root, ReadOnly: true}}}
			for range 2 {
				if supported, reason := lifecycleTestSupported(provider, binary, policy); !supported || reason != "" {
					t.Fatalf("trusted probe rejected: %v, %q", supported, reason)
				}
			}
			// An unrelated writable mount does not invalidate the cached binary.
			policy.mounts[0] = sandbox.Mount{Source: cwd}
			if supported, _ := lifecycleTestSupported(provider, binary, policy); !supported {
				t.Fatal("unrelated mount rejected")
			}
			policy.mounts[0] = sandbox.Mount{Source: root}
			if supported, reason := lifecycleTestSupported(provider, binary, policy); supported || reason == "" {
				t.Fatal("cached capability bypassed the current policy")
			}
			data, err := os.ReadFile(counter)
			if err != nil || string(data) != "probe\n" {
				t.Fatalf("cache did not avoid repeated probes: %q, %v", data, err)
			}
			info, err := os.Stat(binary)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(binary, info.ModTime().Add(time.Second), info.ModTime().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			policy.mounts = nil
			if supported, _ := lifecycleTestSupported(provider, binary, policy); !supported {
				t.Fatal("new binary mtime was not probed")
			}
			data, err = os.ReadFile(counter)
			if err != nil || string(data) != "probe\nprobe\n" {
				t.Fatalf("mtime did not invalidate cache: %q, %v", data, err)
			}
		})
	}
}

func TestLifecycleProbeIncludesImplicitWritableRoots(t *testing.T) {
	cwd := t.TempDir()
	for _, sandboxed := range []bool{true, false} {
		binary := fakeLifecycleBinary(t, "gemini", providerLifecycleVersions["gemini"])
		policy := sessionLifecycleProbePolicy(cwd, []sandbox.Mount{{Source: cwd}}, sandboxed)
		if path, reason := lifecycleProbePath(binary, policy); path != "" || reason == "" {
			t.Fatalf("implicit writable binary accepted: %s, %s", path, reason)
		}
	}
}

func TestProviderLifecyclePreservesInvocationConfig(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"OPENCODE_CONFIG": "/keep/opencode.json", "OPENCODE_CONFIG_CONTENT": `{"model":"keep/model", "plugin":["file:///keep.mjs"], "permission":{"*":"ask"}}`}
	if reason, err := prepareOpenCodeLifecycleEnv(home, "fixture", "", env); err != nil || reason != "" {
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
		if err := prepareProviderLifecycle(RunConfig{Agent: tc.provider, Command: tc.command}, binary, t.TempDir(), tc.args, env, lifecycleProbePolicy{}); err != nil || !reflect.DeepEqual(env, before) {
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
