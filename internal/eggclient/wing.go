package eggclient

import (
	"context"

	"fmt"

	"log"

	"os"
	"os/exec"

	"path/filepath"

	"strconv"
	"strings"

	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"

	"github.com/ehrlich-b/wingthing/internal/egg"

	"github.com/ehrlich-b/wingthing/internal/procinfo"

	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// readEggOwner reads the creator user ID from an egg's owner file.
func ReadEggOwner(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "egg.owner"))
	if err != nil {
		return ""
	}
	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	return lines[0]
}

// readEggOwnerEmail reads the creator email from an egg's owner file (line 2).
func ReadEggOwnerEmail(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "egg.owner"))
	if err != nil {
		return ""
	}
	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	if len(lines) < 2 {
		return ""
	}
	return lines[1]
}

// readEggMeta reads agent/cwd from an egg's meta file.
func ReadEggMeta(dir string) (agent, cwd string) {
	data, err := os.ReadFile(filepath.Join(dir, "egg.meta"))
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "agent":
			agent = v
		case "cwd":
			cwd = v
		}
	}
	return agent, cwd
}

// reapDeadEggs removes egg directories for dead processes on startup.
func ReapDeadEggs(cfg *config.Config) {
	eggsDir := filepath.Join(cfg.Dir, "eggs")
	entries, err := os.ReadDir(eggsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(eggsDir, e.Name())
		pidPath := filepath.Join(dir, "egg.pid")
		data, err := os.ReadFile(pidPath)
		if err != nil {
			// No pid file — stale dir, clean up
			CleanEggDir(dir)
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			CleanEggDir(dir)
			continue
		}
		if !procinfo.OwnedProcessIsAlive(pid) {
			// Dead process
			log.Printf("egg: reaping dead egg %s (pid %d)", e.Name(), pid)
			CleanEggDir(dir)
		}
	}
}

// cleanEggDir removes the files in an egg session directory, then the directory itself.
// If recordings or lifecycle history exist, preserves metadata and data.
func CleanEggDir(dir string) {
	cmdutil.RemoveWithLog(filepath.Join(dir, "egg.sock"))
	cmdutil.RemoveWithLog(filepath.Join(dir, "egg.token"))
	cmdutil.RemoveWithLog(filepath.Join(dir, "egg.pid"))
	// Preserve egg.log — the parent process reads it via readEggCrashInfo
	// after this child exits. Deleting it here causes a race where the
	// crash message is lost ("egg process crashed (no log available)").
	// The log is small and the parent's cleanEggDir call cleans it up later.
	// Keep egg.meta, egg.owner, and dir if audit recordings or chat history exist
	if egg.HasRetainedSessionData(dir) {
		return
	}
	// The egg copies its diagnostic log to the persistent log directory before
	// normal shutdown. Remove the whole transient directory so crash logs and
	// partially-created files do not leave an unreapable directory forever.
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("egg: remove transient session directory %s: %v", dir, err)
	}
}

// eggPidMatchesSession reports whether pid's command line is the egg runner
// for sessionID. PIDs are recycled; a stale egg.pid can point at an unrelated
// process (another user's egg, or any roost process), so a PID whose argv
// cannot be confirmed is never signaled.
func EggPidMatchesSession(pid int, sessionID string) bool {
	if config.Channel() == "preview" {
		return previewEggProcessMatches(pid, sessionID)
	}
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		argv := strings.Split(string(data), "\x00")
		for i, a := range argv {
			if a == "--session-id" && i+1 < len(argv) && argv[i+1] == sessionID {
				return true
			}
		}
		return false
	}
	// No /proc (darwin): fall back to ps.
	out, psErr := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if psErr != nil {
		return false
	}
	return strings.Contains(string(out), "--session-id "+sessionID)
}

// killOrphanEgg kills an egg session that has no active goroutine managing it.
// This handles the case where a pty.kill arrives but the session was never reclaimed.
func KillOrphanEgg(cfg *config.Config, sessionID string) {
	if err := ValidateSessionID(sessionID); err != nil {
		log.Printf("refuse to kill invalid egg session: %v", err)
		return
	}
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)
	if err := egg.MarkDeliberateStop(dir, "kill"); err != nil {
		log.Printf("pty session %s: persist orphan stop: %v", sessionID, err)
		return
	}
	sockPath := filepath.Join(dir, "egg.sock")
	tokenPath := filepath.Join(dir, "egg.token")

	ec, err := egg.Dial(sockPath, tokenPath)
	if err != nil {
		// Can't reach egg — try to kill by PID, but only after confirming the
		// PID still belongs to this session's egg runner.
		pidPath := filepath.Join(dir, "egg.pid")
		terminationRequested := false
		data, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && EggPidMatchesSession(pid, sessionID) {
				if proc, findErr := os.FindProcess(pid); findErr != nil {
					log.Printf("pty session %s: find orphan egg process %d: %v", sessionID, pid, findErr)
				} else if signalErr := proc.Signal(syscall.SIGTERM); signalErr != nil {
					log.Printf("pty session %s: terminate orphan egg process %d: %v", sessionID, pid, signalErr)
				} else {
					terminationRequested = true
				}
			}
		}
		if terminationRequested {
			// Leave runtime files in place until the egg exits and performs its
			// own cleanup. Reaping them now can break an in-flight shutdown.
			log.Printf("pty session %s: orphan termination requested (pid)", sessionID)
		} else {
			CleanEggDir(dir)
			log.Printf("pty session %s: stale orphan metadata cleaned", sessionID)
		}
		return
	}
	if killErr := ec.Kill(context.Background(), sessionID); killErr != nil {
		log.Printf("pty session %s: terminate orphan over gRPC: %v", sessionID, killErr)
	} else {
		log.Printf("pty session %s: orphan termination requested (gRPC)", sessionID)
	}
	cmdutil.CloseWithLog("orphan egg client", ec)
}

func ResizeEgg(cfg *config.Config, sessionID string, rows, cols uint32) (result error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return err
	}
	if rows == 0 || cols == 0 || rows > 1000 || cols > 1000 {
		return fmt.Errorf("invalid terminal dimensions")
	}
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)
	ec, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	if err != nil {
		return fmt.Errorf("open session: %w", err)
	}
	defer func() { result = cmdutil.CloseAndJoin("egg resize client", ec, result) }()
	resizeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ec.Resize(resizeCtx, sessionID, rows, cols); err != nil {
		return fmt.Errorf("resize session: %w", err)
	}
	return nil
}

// readEggCrashInfo reads the last lines of an egg's log looking for panic/crash info.
func ReadEggCrashInfo(dir string) string {
	logPath := filepath.Join(dir, "egg.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		return "egg process crashed (no log available)"
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		return "egg process crashed (empty log)"
	}

	// Find the last panic
	lastPanic := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "panic") || strings.Contains(lines[i], "PANIC") || strings.Contains(lines[i], "fatal error") {
			lastPanic = i
			break
		}
	}

	if lastPanic != -1 {
		// Extract up to 20 lines from the panic point
		end := lastPanic + 20
		if end > len(lines) {
			end = len(lines)
		}
		excerpt := strings.Join(lines[lastPanic:end], "\n")
		return fmt.Sprintf("egg crashed: %s", strings.TrimSpace(excerpt))
	}

	// No panic found — return the last line (cobra prints "Error: ..." there)
	// plus any "Error:" lines from the log.
	last := lines[len(lines)-1]
	if strings.Contains(last, "Error:") || strings.Contains(last, "error") {
		return strings.TrimSpace(last)
	}

	// Fall back to last 5 lines for context
	start := len(lines) - 5
	if start < 0 {
		start = 0
	}
	return strings.TrimSpace(strings.Join(lines[start:], "\n"))
}

// canAccessSessionArtifact applies the same current owner-and-workspace policy
// used by session listings before exposing a persisted audit or chat artifact.
// Missing legacy metadata fails closed for members; owners and admins retain
// the historical oversight access.
func CanAccessSessionArtifact(req ws.TunnelRequest, sessionDir string, userPaths []string) bool {
	if !wingpolicy.IsMemberFiltered(req) {
		return true
	}
	_, sessionPath := ReadEggMeta(sessionDir)
	return wingpolicy.CanSeeSession(req, ReadEggOwner(sessionDir)) && wingpolicy.CanAccessSessionPath(req, sessionPath, userPaths)
}
