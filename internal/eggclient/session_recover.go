package eggclient

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	agentpkg "github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

type RecoveryClass string

const (
	RecoveryAlive    RecoveryClass = "alive"
	RecoveryEligible RecoveryClass = "eligible"
	RecoveryArchived RecoveryClass = "archived"
	RecoveryStopped  RecoveryClass = "stopped"
)

type RecoverySession struct {
	LocalSession
	Intent egg.LaunchIntent `json:"intent"`
	Class  RecoveryClass    `json:"class"`
}

func LaunchModel(args []string) string {
	model := ""
	for i, arg := range args {
		if (arg == "--model" || arg == "-m") && i+1 < len(args) {
			model = args[i+1]
		} else if value, ok := strings.CutPrefix(arg, "--model="); ok {
			model = value
		}
	}
	if len(model) > 128 || strings.ContainsAny(model, "\x00\r\n") {
		return ""
	}
	return model
}

// ClassifyEgg never migrates legacy directories or guesses a provider identity
// from another session's transcript. Invalid/missing intent is archived.
func ClassifyEgg(cfg *config.Config, id string) RecoverySession {
	dir := filepath.Join(cfg.Dir, "eggs", id)
	meta := ReadEggMetaValues(dir)
	pid, alive := ReadAliveEggPID(dir)
	intent, err := egg.ReadLaunchIntent(dir)
	r := RecoverySession{LocalSession: LocalSession{ID: id, Name: ReadSessionName(dir), Principal: ReadSessionPrincipal(dir), Agent: meta["agent"], CWD: meta["cwd"], Kind: meta["kind"], PID: pid}, Intent: intent, Class: RecoveryArchived}
	if alive {
		r.Class = RecoveryAlive
		return r
	}
	if _, stopErr := os.Lstat(filepath.Join(dir, egg.DeliberateStopFile)); !errors.Is(stopErr, os.ErrNotExist) {
		r.Class = RecoveryStopped
		return r
	}
	if err != nil || !intent.Started || intent.RecoveredSession != "" || intent.Agent != r.Agent || wingpolicy.CanonicalSessionPath(intent.CWD) != wingpolicy.CanonicalSessionPath(r.CWD) || !ValidProviderSessionID(intent.ProviderSessionID) {
		return r
	}
	definition, ok := agentpkg.LookupDefinition(intent.Agent)
	if !ok || definition.ResumeFlag == "" {
		return r
	}
	r.ConversationLink = SessionConversationLink(cfg, id)
	if intent.ConversationID != "" && r.ConversationID != intent.ConversationID {
		return r
	}
	r.Class, r.Status, r.Recoverable = RecoveryEligible, "exited", true
	return r
}

func DiscoverRecoverableSessions(cfg *config.Config) ([]RecoverySession, error) {
	entries, err := os.ReadDir(filepath.Join(cfg.Dir, "eggs"))
	if errors.Is(err, os.ErrNotExist) {
		return []RecoverySession{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := []RecoverySession{}
	for _, entry := range entries {
		if entry.IsDir() {
			session := ClassifyEgg(cfg, entry.Name())
			if session.Class == RecoveryEligible {
				result = append(result, session)
			}
		}
	}
	return result, nil
}

// AcquireRecoveryLock serializes all CLI, MCP and startup recovery attempts for
// one source, including publication of the replacement execution.
func AcquireRecoveryLock(cfg *config.Config, id string) (*os.File, error) {
	if err := ValidateSessionID(id); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(cfg.Dir, "eggs", id, "recovery.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, errors.New("session recovery is already in progress")
	}
	return lock, nil
}

func RecoveryBootID() (string, error) {
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
		return strings.TrimSpace(string(data)), err
	}
	if runtime.GOOS == "darwin" {
		data, err := exec.Command("sysctl", "-n", "kern.boottime").Output()
		return strings.TrimSpace(string(data)), err
	}
	return "", errors.New("boot identity unavailable on this platform")
}

// ClaimAutoRecovery is called while holding the source recovery lock. Persist
// the boot before launching, so a daemon restart cannot repeat an attempt.
func ClaimAutoRecovery(dir, boot string, now time.Time) (bool, error) {
	claimed := false
	err := egg.UpdateLaunchIntent(dir, func(intent *egg.LaunchIntent) {
		if boot != "" && intent.AutoBoot != boot && intent.RetryAfter <= now.Unix() {
			intent.AutoBoot = boot
			claimed = true
		}
	})
	return claimed, err
}

func RecordRecoveryFailure(dir string, failure error, automatic bool, now time.Time) error {
	return egg.UpdateLaunchIntent(dir, func(intent *egg.LaunchIntent) {
		intent.RecoveryError = failure.Error()
		if automatic {
			intent.AutoFailures++
			backoff := time.Minute << min(intent.AutoFailures-1, 6)
			intent.RetryAfter = now.Add(backoff).Unix()
		}
	})
}
