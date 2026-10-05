package egg

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCodexLifecycleInstalledHookTrust(t *testing.T) {
	binary, err := exec.LookPath("codex")
	if err != nil || !codexLifecycleSupported(binary) {
		t.Skip("installed Codex has no native hook trust support")
	}
	// Codex canonicalizes source paths (including macOS's /var symlink).
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	args, err := CodexLifecycleArgs([]string{"app-server"}, home, "trust-probe")
	if err != nil {
		t.Fatal(err)
	}
	// Seed a trusted user hook and an untrusted user hook. Neither provider
	// sessions, authentication, nor model requests are started by hooks/list.
	configPath := filepath.Join(home, ".codex", "config.toml")
	userKey := configPath + ":stop:0:0"
	// This hash is from Codex 0.159.3 for `true` with its default timeout.
	userHash := "sha256:2ba34c7a02841fada06b868a093d041fd3d9de81883c789a321916b0aa118851"
	config := "[[hooks.Stop]]\n[[hooks.Stop.hooks]]\ntype = 'command'\ncommand = 'true'\n" +
		"[[hooks.Interrupt]]\n[[hooks.Interrupt.hooks]]\ntype = 'command'\ncommand = 'true'\n" +
		"[hooks.state." + strconv.Quote(userKey) + "]\ntrusted_hash = " + strconv.Quote(userHash) + "\n"
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	baseline := listInstalledCodexHooks(t, binary, home, []string{"app-server"})
	assertInstalledCodexHookTrust(t, baseline, userKey, 0)
	hooks := listInstalledCodexHooks(t, binary, home, args)
	assertInstalledCodexHookTrust(t, hooks, userKey, len(codexLifecycleEvents))
	data, err := os.ReadFile(configPath)
	if err != nil || string(data) != config {
		t.Fatalf("user config changed: %s, %v", data, err)
	}
}

type installedCodexHook struct {
	Key, Source, TrustStatus string
}

func assertInstalledCodexHookTrust(t *testing.T, hooks []installedCodexHook, userKey string, generated int) {
	t.Helper()
	trusted, userTrusted, userUntrusted := 0, 0, 0
	for _, hook := range hooks {
		if hook.Source == "sessionFlags" && hook.TrustStatus == "trusted" {
			trusted++
		} else if hook.Source == "user" && hook.Key == userKey && hook.TrustStatus == "trusted" {
			userTrusted++
		} else if hook.Source == "user" && hook.TrustStatus == "untrusted" {
			userUntrusted++
		}
	}
	if trusted != generated || userTrusted != 1 || userUntrusted != 1 || len(hooks) != generated+2 {
		t.Fatalf("generated/existing hook trust changed: %+v", hooks)
	}
}

func listInstalledCodexHooks(t *testing.T, binary, home string, args []string) []installedCodexHook {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = home
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + filepath.Join(home, ".codex")}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); cancel(); _ = cmd.Wait() }()
	for _, request := range []string{
		`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"wt-hook-test","version":"1"},"capabilities":{"experimentalApi":true}}}`,
		`{"method":"initialized"}`,
		fmt.Sprintf(`{"id":2,"method":"hooks/list","params":{"cwd":%q}}`, home),
	} {
		if _, err := fmt.Fprintln(input, request); err != nil {
			t.Fatal(err)
		}
	}
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var response struct {
			ID     int
			Result struct {
				Data []struct{ Hooks []installedCodexHook }
			}
			Error json.RawMessage
		}
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil || response.ID != 2 {
			continue
		}
		if len(response.Error) > 0 {
			t.Fatalf("Codex hooks/list failed: %s", response.Error)
		}
		var hooks []installedCodexHook
		for _, data := range response.Result.Data {
			hooks = append(hooks, data.Hooks...)
		}
		return hooks
	}
	t.Fatalf("Codex did not list hooks: %v, %v", scanner.Err(), ctx.Err())
	return nil
}

func TestCodexLifecycleArgsTrustOnlyGeneratedHooks(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("notify = ['existing-notifier']\n")
	if err := os.WriteFile(config, original, 0600); err != nil {
		t.Fatal(err)
	}
	args, err := CodexLifecycleArgs([]string{"resume", "thread-exact", "-m", "existing-model"}, home, "egg-exact")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(args[:4], " ") != "resume thread-exact -m existing-model" || len(args) != 4+2*len(codexLifecycleEvents)+2 {
		t.Fatalf("unexpected args: %v", args)
	}
	for _, arg := range args {
		if strings.Contains(arg, "bypass-hook-trust") || strings.Contains(arg, "notify=") || strings.Contains(arg, "features.hooks") {
			t.Fatalf("changed existing hook policy: %s", arg)
		}
	}
	data, err := os.ReadFile(config)
	if err != nil || string(data) != string(original) {
		t.Fatalf("user config changed: %s, %v", data, err)
	}
	// This hash is from the installed 0.159.3 app-server's hooks/list, rather
	// than an expectation generated by the implementation under test.
	hash, err := codexLifecycleHookHash("session_start", "true")
	if err != nil || hash != "sha256:e40688e342bc653567f4d9e8590f9b0ceb640df45804b65d9361fbe9f641edf1" {
		t.Fatalf("Codex trust identity changed: %q, %v", hash, err)
	}
	for _, supplied := range [][]string{{"--disable", "hooks"}, {"-c", "features.hooks=false"}, {"-c", "hooks.Stop=[]"}, {"-chooks.Stop=[]"}, {"--config=hooks.Stop=[]"}, {"-c", "features.hooks\t=\tfalse"}} {
		got, err := CodexLifecycleArgs(supplied, home, "egg-exact")
		if err != nil || strings.Join(got, " ") != strings.Join(supplied, " ") {
			t.Fatalf("explicit hook settings replaced: %v, %v", got, err)
		}
	}
}

func TestCodexLifecycleSupportedUsesBinaryCapability(t *testing.T) {
	for _, tc := range []struct {
		help string
		want bool
	}{
		{"Codex CLI with notify", false},
		{"Codex CLI --dangerously-bypass-hook-trust", true},
	} {
		path := filepath.Join(t.TempDir(), "codex")
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' "+shellQuoteLifecycle(tc.help)+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if got := codexLifecycleSupported(path); got != tc.want {
			t.Fatalf("capability for %q = %t, want %t", tc.help, got, tc.want)
		}
	}
}

func TestCodexLifecycleSupportedCachesByBinaryPathAndMtime(t *testing.T) {
	for _, supported := range []bool{true, false} {
		t.Run(strconv.FormatBool(supported), func(t *testing.T) {
			dir := t.TempDir()
			binary, counter := filepath.Join(dir, "codex"), filepath.Join(dir, "probes")
			writeBinary := func(supported bool, modTime time.Time) {
				t.Helper()
				help := "Codex CLI with notify"
				if supported {
					help += " --dangerously-bypass-hook-trust"
				}
				script := "#!/bin/sh\nprintf 'probe\\n' >> " + shellQuoteLifecycle(counter) +
					"\nprintf '%s\\n' " + shellQuoteLifecycle(help) + "\n"
				if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(binary, modTime, modTime); err != nil {
					t.Fatal(err)
				}
			}
			assertCached := func(want bool, probes int) {
				t.Helper()
				var wg sync.WaitGroup
				for range 6 {
					wg.Go(func() {
						if got := codexLifecycleSupported(binary); got != want {
							t.Errorf("capability = %t, want %t", got, want)
						}
					})
				}
				wg.Wait()
				data, err := os.ReadFile(counter)
				if err != nil || strings.Count(string(data), "probe\n") != probes {
					t.Fatalf("expected %d help probes, got %q: %v", probes, data, err)
				}
			}
			// Both binaries start at the same mtime; their paths keep the
			// supported and unsupported results separate.
			modTime := time.Unix(1700000000, 0)
			writeBinary(supported, modTime)
			assertCached(supported, 1)
			writeBinary(!supported, modTime.Add(time.Second))
			assertCached(!supported, 2)
			if err := os.Remove(binary); err != nil {
				t.Fatal(err)
			}
			if codexLifecycleSupported(binary) {
				t.Fatal("removed binary retained cached capability")
			}
		})
	}
}

func TestCodexLifecycleSkipsOversizedHooksBeforeThreadBinding(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	spool := filepath.Join(home, ".codex", "wingthing-events", filepath.Base(dir))
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	oversized := strings.Repeat("x", 3*maxLifecycleRecord)
	lifecycleWrite(t, filepath.Join(spool, "seq.00000000000000000001.json"), oversized)
	lifecycleWrite(t, filepath.Join(spool, "seq.00000000000000000002.json"), `{"session_id":"thread-exact","hook_event_name":"SessionStart"}`)
	lifecycleWrite(t, filepath.Join(spool, "seq.00000000000000000003.json"), `{"session_id":"thread-exact","hook_event_name":"PreToolUse","tool_name":"request_user_input"}`)
	v, err := ReadSessionLifecycle(dir, "codex", "", home, "", true, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if v.ProviderSessionID != "thread-exact" || v.Status != "blocked" || len(v.Events) != 3 || v.Events[0].Type != "provider_warning" || !v.Events[0].Truncated || v.Events[0].OriginalBytes != int64(len(oversized)) {
		t.Fatalf("oversized hook prevented thread binding or status: %+v", v)
	}
	for _, event := range v.Events {
		if event.Source != "codex_hook" {
			t.Fatalf("Codex event has wrong source: %+v", event)
		}
	}
	again, err := ReadSessionLifecycle(dir, "codex", "", home, "", true, v.Cursor, 10)
	if err != nil || again.ProviderSessionID != "thread-exact" || again.Status != "blocked" || len(again.Events) != 0 || again.HeadCursor != v.HeadCursor {
		t.Fatalf("replay lost thread binding or retried skipped hook: %+v, %v", again, err)
	}
}

func TestCodexLifecycleNativeHooksBindThreadAndReportStatus(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	args, err := CodexLifecycleArgs(nil, home, filepath.Base(dir))
	if err != nil {
		t.Fatal(err)
	}
	commands := map[string]string{}
	for i, event := range codexLifecycleEvents {
		definition := args[i*2+1]
		quoted := strings.TrimSuffix(strings.SplitN(definition, "command=", 2)[1], ",timeout=3}]}]")
		command, err := strconv.Unquote(quoted)
		if err != nil {
			t.Fatal(err)
		}
		commands[event.name] = command
	}
	drive := func(event, id, tool string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{"hook_event_name": event, "session_id": id, "tool_name": tool})
		cmd := exec.Command("/bin/sh", "-c", commands[event])
		cmd.Stdin = strings.NewReader(string(payload))
		if output, err := cmd.CombinedOutput(); err != nil || len(output) != 0 {
			t.Fatalf("hook: %s, %v", output, err)
		}
	}
	read := func(alive bool, cursor int64) SessionView {
		t.Helper()
		v, err := ReadSessionLifecycle(dir, "codex", "/fixture", home, "", alive, cursor, 100)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := read(true, 0); v.Status != "unknown" {
		t.Fatalf("missing hooks guessed status: %+v", v)
	}
	drive("SessionStart", "thread-exact", "")
	for _, tc := range []struct{ event, tool, status string }{
		{"UserPromptSubmit", "", "working"},
		{"PermissionRequest", "Bash", "blocked"},
		{"PostToolUse", "Bash", "working"},
		{"PreToolUse", "request_user_input", "blocked"},
		{"PostToolUse", "request_user_input", "working"},
		{"PreToolUse", "mcp__fixture__tool", "unknown"},
		{"PostToolUse", "mcp__fixture__tool", "working"},
		{"Stop", "", "idle"},
		{"PreCompact", "", "working"},
		{"Interrupt", "", "idle"},
		{"SessionEnd", "", "done"},
	} {
		drive(tc.event, "thread-exact", tc.tool)
		v := read(true, 0)
		if v.Status != tc.status || v.ProviderSessionID != "thread-exact" || v.StateSource != "codex_hook" {
			t.Fatalf("%s: %+v", tc.event, v)
		}
	}
	drive("UserPromptSubmit", "foreign-child", "")
	v := read(false, 0)
	if v.Status != "done" || v.Ready || v.ProviderSessionID != "thread-exact" {
		t.Fatalf("foreign thread regressed end: %+v", v)
	}
	if replay := read(false, v.HeadCursor); len(replay.Events) != 0 {
		t.Fatalf("duplicate import: %+v", replay)
	}
	if err := RecordSessionProcessEvent(dir, "session_exit", "failed", "killed"); err != nil {
		t.Fatal(err)
	}
	if v = read(false, 0); v.Status != "exited" {
		t.Fatal(fmt.Sprintf("failed process reported done: %+v", v))
	}
}
