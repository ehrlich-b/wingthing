package egg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/ehrlich-b/wingthing/internal/permissions"
)

// Start only for the real interactive Claude path, after ordinary preparation.
// Observers and supplied hooks retain their entries. disableAllHooks is honored.
func (s *Server) preparePermissions(ctx context.Context, home, settingsPath, provider string, env map[string]string, namespaced bool) (func(), error) {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil, err
	}
	var settings map[string]any
	if err = json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	if disabled, _ := settings["disableAllHooks"].(bool); disabled {
		return func() {}, nil
	}
	machine, _ := os.Hostname()
	account, err := user.Current()
	if err != nil {
		return nil, err
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if binary, err = filepath.EvalSymlinks(binary); err != nil {
		return nil, err
	}
	broker := permissions.New(filepath.Base(s.dir), provider, machine, account.Username)
	socket := permissions.Socket(home, broker.SessionID)
	if err = ValidateSocketPath(socket); err != nil {
		return nil, err
	}
	listener, err := permissions.Listen(socket)
	if err != nil {
		return nil, err
	}
	childCtx, cancel := context.WithCancel(ctx)
	started := make(chan int, 1)
	s.permissionStarted = started
	broker.Authenticate = func(c net.Conn, input *os.File) error {
		select {
		case root := <-started:
			started <- root
			return permissions.HookPeer(root, settingsPath, binary, c, input)
		case <-childCtx.Done():
			return childCtx.Err()
		}
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = broker.Serve(childCtx, listener) }()
	cleanup := func() { cancel(); listener.Close(); <-done; _ = os.Remove(socket); _ = os.Remove(filepath.Dir(socket)) }
	delete(env, "WT_PERMISSION_TOKEN")
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	brokerPID := os.Getpid()
	if namespaced && runtime.GOOS == "linux" {
		brokerPID = 0
	}
	entries, _ := hooks["PermissionRequest"].([]any)
	hooks["PermissionRequest"] = append(entries, map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": binary, "args": []string{"_permission_hook", "--socket", socket, "--session", broker.SessionID, "--generation", broker.Generation, "--provider", broker.ProviderID, "--broker-pid", strconv.Itoa(brokerPID)}, "timeout": permissions.HookTimeout}}})
	settings["hooks"] = hooks
	perms, _ := settings["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	ask, _ := perms["ask"].([]any)
	perms["ask"] = append(ask, "Bash")
	settings["permissions"] = perms
	data, err = json.Marshal(settings)
	if err == nil {
		err = os.WriteFile(settingsPath, data, 0600)
	}
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("prepare permission hook: %w", err)
	}
	s.permissions = broker
	return cleanup, nil
}

func (s *Server) permissionOperation(ctx context.Context, method string, r permissions.Response) (map[string]any, error) {
	if s.permissions == nil {
		return nil, errors.New("this session has no permission broker")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch method {
	case "List":
		return map[string]any{"requests": s.permissions.List()}, nil
	case "Check":
		return map[string]any{}, s.permissions.Check(r)
	case "Respond":
		out, err := s.permissions.Respond(r)
		return map[string]any{"outcome": out}, err
	}
	return nil, errors.New("unsupported permission method")
}

// The existing Claude profile exposes .claude for pathname IPC. macOS also
// needs its explicit socket exception; Linux uses the existing mount grant.
func permissionSocketForSandbox(home, session string) string {
	if runtime.GOOS == "darwin" {
		return permissions.Socket(home, session)
	}
	return ""
}
