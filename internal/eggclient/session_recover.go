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
	Intent egg.LaunchIntent   `json:"intent"`
	Class  RecoveryClass      `json:"class"`
	Record egg.RecoveryRecord `json:"-"`
}

// LoadRecoveryEggConfig uses only the protected admission record. An exact
// resolved-policy digest also covers changes to inherited bases and defaults.
func LoadRecoveryEggConfig(record egg.RecoveryRecord) (*egg.EggConfig, error) {
	digest, err := egg.RecoveryConfigDigest(record.Intent.EggConfig)
	if err != nil || digest != record.ConfigSHA256 {
		return nil, errors.New("recovery egg config changed")
	}
	var policy *egg.EggConfig
	if record.Intent.EggConfig != "" {
		policy, err = egg.ResolveEggConfig(record.Intent.EggConfig)
	} else if record.Sandboxed {
		policy = egg.DefaultEggConfig()
	} else {
		policy = egg.UnsandboxedEggConfig()
	}
	if err != nil {
		return nil, err
	}
	digest, err = egg.RecoveryPolicyDigest(policy, record.Intent.Agent, record.Intent.CWD, record.ProviderHome)
	if err != nil || digest != record.PolicySHA256 || egg.RequiresSandbox(policy, record.Intent.Agent) != record.Sandboxed {
		return nil, errors.New("recovery policy exceeds its recorded ceiling")
	}
	return policy, nil
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
	record, err := egg.ReadRecoveryRecord(dir)
	intent := record.Intent
	r := RecoverySession{LocalSession: LocalSession{ID: id, Name: ReadSessionName(dir), Principal: ReadSessionPrincipal(dir), Agent: meta["agent"], CWD: meta["cwd"], Kind: meta["kind"], PID: pid}, Intent: intent, Class: RecoveryArchived}
	if r.Kind == "" && r.Agent != "" {
		r.Kind = "agent"
	}
	if r.Name == "" && ValidateSessionName(intent.Label) == nil {
		r.Name = intent.Label
	}
	if alive {
		r.Class = RecoveryAlive
		return r
	}
	if _, stopErr := os.Lstat(filepath.Join(dir, egg.DeliberateStopFile)); !errors.Is(stopErr, os.ErrNotExist) {
		r.Class = RecoveryStopped
		return r
	}
	if record.Stopped {
		r.Class = RecoveryStopped
		return r
	}
	if err != nil || record.Archived || !intent.Started || intent.RecoveredSession != "" || intent.Agent != r.Agent || wingpolicy.CanonicalSessionPath(intent.CWD) != wingpolicy.CanonicalSessionPath(r.CWD) || record.Principal != r.Principal || record.OwnerID != ReadEggOwner(dir) || record.OwnerEmail != ReadEggOwnerEmail(dir) || wingpolicy.CanonicalSessionPath(record.ProviderHome) != wingpolicy.CanonicalSessionPath(meta["provider_home"]) {
		return r
	}
	// Reject forged policy/identity hints before importing native lifecycle data.
	hint, hintErr := egg.ReadLaunchIntent(dir)
	if hintErr == nil && (hint.Agent != intent.Agent || wingpolicy.CanonicalSessionPath(hint.CWD) != wingpolicy.CanonicalSessionPath(intent.CWD) || hint.EggConfig != intent.EggConfig || hint.ProviderSessionID != "" && intent.ProviderSessionID != "" && hint.ProviderSessionID != intent.ProviderSessionID) {
		return r
	}
	if _, err := LoadRecoveryEggConfig(record); err != nil {
		return r
	}
	if intent.ProviderSessionID == "" {
		switch intent.Agent {
		case "claude", "codex", "gemini", "opencode":
			if home, homeErr := LifecycleProviderHome(cfg, record.ProviderHome); homeErr == nil {
				if _, readErr := egg.TryReadSessionLifecycle(dir, intent.Agent, intent.CWD, home, "", alive, 0, 1); readErr == nil {
					record, err = egg.ReadRecoveryRecord(dir)
					intent = record.Intent
				}
			}
		}
	}
	if err != nil || !ValidProviderSessionID(intent.ProviderSessionID) || hintErr == nil && hint.ProviderSessionID != "" && hint.ProviderSessionID != intent.ProviderSessionID {
		return r
	}
	r.Intent, r.Record = intent, record
	r.CWD, r.Agent, r.Principal = intent.CWD, intent.Agent, record.Principal
	definition, ok := agentpkg.LookupDefinition(intent.Agent)
	if !ok || definition.ResumeFlag == "" {
		return r
	}
	r.ConversationLink = SessionConversationLink(cfg, id)
	if intent.ConversationID != "" && (r.ConversationID != intent.ConversationID || !IsCurrentConversationExecution(cfg, id)) {
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
	lock, err := os.OpenFile(filepath.Join(cfg.Dir, "recovery", id+".lock"), os.O_CREATE|os.O_RDWR, 0600)
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
	err := egg.UpdateRecoveryRecord(dir, func(r *egg.RecoveryRecord) {
		intent := &r.Intent
		if boot != "" && intent.AutoBoot != boot && intent.RetryAfter <= now.Unix() {
			intent.AutoBoot = boot
			claimed = true
		}
	})
	return claimed, err
}

func RecordRecoveryFailure(dir string, failure error, automatic bool, now time.Time) error {
	return egg.UpdateRecoveryRecord(dir, func(r *egg.RecoveryRecord) {
		intent := &r.Intent
		// Provider/config errors can contain environment values. Keep only the
		// failure state here; the caller receives the detailed error directly.
		intent.RecoveryError = "recovery launch failed"
		if automatic {
			intent.AutoFailures++
			backoff := time.Minute << min(intent.AutoFailures-1, 6)
			intent.RetryAfter = now.Add(backoff).Unix()
		}
	})
}
