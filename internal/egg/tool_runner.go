package egg

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
)

const maxToolOutputBytes = 1 << 20

var inheritedToolEnv = map[string]bool{
	"HOME": true, "LANG": true, "LOGNAME": true, "PATH": true, "TMPDIR": true,
	"TZ": true, "USER": true, "SSL_CERT_DIR": true, "SSL_CERT_FILE": true,
}

// ToolRunner owns the privileged tool registry and executes tools. It is the shared
// execution core behind both the egg tool socket (ToolListener) and the remote MCP
// server — so a tool behaves identically whether an in-egg agent or a remote MCP client
// invokes it, and credentials stay in one place.
type ToolRunner struct {
	mu    sync.RWMutex
	tools map[string]*config.ToolConfig
	sema  map[string]chan struct{} // per-tool concurrency semaphores
}

// NewToolRunner builds a runner from tool configs.
func NewToolRunner(tools []*config.ToolConfig) *ToolRunner {
	r := &ToolRunner{
		tools: make(map[string]*config.ToolConfig, len(tools)),
		sema:  make(map[string]chan struct{}),
	}
	for _, t := range tools {
		r.tools[t.Name] = t
		if t.MaxConcurrent > 0 {
			r.sema[t.Name] = make(chan struct{}, t.MaxConcurrent)
		}
	}
	return r
}

// Reload replaces the tool configs atomically, reusing compatible semaphores.
func (r *ToolRunner) Reload(tools []*config.ToolConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	newMap := make(map[string]*config.ToolConfig, len(tools))
	newSema := make(map[string]chan struct{})
	for _, t := range tools {
		newMap[t.Name] = t
		if t.MaxConcurrent > 0 {
			if old, ok := r.sema[t.Name]; ok && cap(old) == t.MaxConcurrent {
				newSema[t.Name] = old
			} else {
				newSema[t.Name] = make(chan struct{}, t.MaxConcurrent)
			}
		}
	}
	r.tools = newMap
	r.sema = newSema
}

// List returns all registered tools (name + description), unfiltered.
func (r *ToolRunner) List() []ToolListEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var entries []ToolListEntry
	for _, t := range r.tools {
		entries = append(entries, ToolListEntry{Name: t.Name, Description: t.Description, Params: t.Params})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

// ParamsFor returns a tool's optional ordered parameter metadata.
func (r *ToolRunner) ParamsFor(name string) ([]config.ToolParam, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tc, ok := r.tools[name]
	if !ok {
		return nil, false
	}
	return tc.Params, true
}

// Has reports whether a tool is registered.
func (r *ToolRunner) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

// TimeoutFor returns a tool's configured timeout, or 0 if unknown.
func (r *ToolRunner) TimeoutFor(name string) time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if tc, ok := r.tools[name]; ok {
		return tc.TimeoutDuration()
	}
	return 0
}

// Call executes a tool by name with positional args, enforcing per-tool concurrency.
func (r *ToolRunner) Call(name string, args []string) ToolResponse {
	return r.CallWithEnv(name, args, nil)
}

// CallWithEnv executes a tool with additional per-call identity metadata. Extra environment
// values are appended last, so a static tool config cannot spoof caller identity.
func (r *ToolRunner) CallWithEnv(name string, args []string, extraEnv map[string]string) ToolResponse {
	r.mu.RLock()
	tc, ok := r.tools[name]
	var sema chan struct{}
	if ok {
		sema = r.sema[name]
	}
	r.mu.RUnlock()
	if !ok {
		return ToolResponse{Error: "unknown tool: " + name}
	}
	if sema != nil {
		select {
		case sema <- struct{}{}:
			defer func() { <-sema }()
		default:
			return ToolResponse{Error: fmt.Sprintf("tool %s: max concurrent limit reached", name)}
		}
	}
	return r.executeTool(tc, args, extraEnv)
}

func (r *ToolRunner) executeTool(tc *config.ToolConfig, args []string, extraEnv map[string]string) ToolResponse {
	timeout := tc.TimeoutDuration()
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// sh -c 'script' tool arg1 arg2 ...
	// "tool" is $0, args become $1, $2, etc.
	cmdArgs := append([]string{"-c", tc.Run, "tool"}, args...)
	cmd := exec.CommandContext(ctx, "sh", cmdArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// Do not leak the roost's OAuth, JWT, SMTP, or model credentials into every tool.
	// Tools receive a small process environment plus only their explicitly configured
	// credentials and trusted per-request identity.
	cmd.Env = toolProcessEnv(tc.Env, extraEnv)
	var stdout, stderr cappedToolOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return ToolResponse{ExitCode: 124, Stderr: "tool execution timed out"}
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return ToolResponse{ExitCode: 1, Stderr: err.Error()}
		}
	}
	return ToolResponse{ExitCode: exitCode, Stdout: stdout.String(), Stderr: stderr.String()}
}

func toolProcessEnv(toolEnv, extraEnv map[string]string) []string {
	values := make(map[string]string, len(toolEnv)+len(extraEnv)+len(inheritedToolEnv))
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if ok && (inheritedToolEnv[name] || strings.HasPrefix(name, "LC_")) {
			values[name] = value
		}
	}
	for name, value := range toolEnv {
		values[name] = value
	}
	for name, value := range extraEnv {
		values[name] = value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	env := make([]string, 0, len(names))
	for _, name := range names {
		env = append(env, name+"="+values[name])
	}
	return env
}

type cappedToolOutput struct {
	b         strings.Builder
	truncated bool
}

func (w *cappedToolOutput) Write(p []byte) (int, error) {
	original := len(p)
	remaining := maxToolOutputBytes - w.b.Len()
	if remaining <= 0 {
		w.truncated = true
		return original, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		w.truncated = true
	}
	if _, err := w.b.Write(p); err != nil {
		return 0, err
	}
	return original, nil
}

func (w *cappedToolOutput) String() string {
	if !w.truncated {
		return w.b.String()
	}
	return w.b.String() + "\n[wingthing: tool output truncated]\n"
}

var _ io.Writer = (*cappedToolOutput)(nil)
