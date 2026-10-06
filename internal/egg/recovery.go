package egg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ehrlich-b/wingthing/internal/fsutil"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

const LaunchIntentFile = "launch.intent.json"
const DeliberateStopFile = "session.stopped"

// LaunchIntent is an allowlist of restart metadata. It deliberately contains
// neither argv nor rendered config: both can contain credentials or env values.
type LaunchIntent struct {
	Version              int    `json:"version"`
	Agent                string `json:"agent"`
	CWD                  string `json:"cwd"`
	Label                string `json:"label,omitempty"`
	ConversationID       string `json:"conversation_id,omitempty"`
	RootConversationID   string `json:"root_conversation_id,omitempty"`
	ParentConversationID string `json:"parent_conversation_id,omitempty"`
	ProviderSessionID    string `json:"provider_session_id,omitempty"`
	Model                string `json:"model,omitempty"`
	EggConfig            string `json:"egg_config,omitempty"`
	Started              bool   `json:"started"`
	RecoveredFrom        string `json:"recovered_from,omitempty"`
	RecoveredSession     string `json:"recovered_session,omitempty"`
	AutoBoot             string `json:"auto_boot,omitempty"`
	AutoFailures         int    `json:"auto_failures,omitempty"`
	RetryAfter           int64  `json:"retry_after,omitempty"`
	RecoveryError        string `json:"recovery_error,omitempty"`
}

// RecoveryRecord is host authority, kept outside the provider-writable egg
// directory. Only digests of policy are retained; rendered policy can contain
// credentials. Egg-directory metadata is never a source of admission policy.
type RecoveryRecord struct {
	Intent       LaunchIntent `json:"intent"`
	Principal    string       `json:"principal"`
	OwnerID      string       `json:"owner_id"`
	OwnerEmail   string       `json:"owner_email"`
	ProviderHome string       `json:"provider_home"`
	Sandboxed    bool         `json:"sandboxed"`
	ConfigSHA256 string       `json:"config_sha256"`
	PolicySHA256 string       `json:"policy_sha256"`
	Archived     bool         `json:"archived,omitempty"`
	Stopped      bool         `json:"stopped,omitempty"`
}

func RecoveryDir(eggDir string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(eggDir)), "recovery")
}

func protectRecoveryStorage(cfg *sandbox.Config, eggDir string) error {
	dir := RecoveryDir(eggDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	cfg.RecoveryDir, err = filepath.Abs(resolved)
	return err
}

func recoveryRecordPath(eggDir string) (string, error) {
	if filepath.Base(filepath.Dir(eggDir)) != "eggs" || !validLifecycleID(filepath.Base(eggDir)) {
		return "", errors.New("invalid recovery egg directory")
	}
	return filepath.Join(RecoveryDir(eggDir), filepath.Base(eggDir)+".json"), nil
}

func recoveryDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func RecoveryPolicyDigest(policy *EggConfig, agent, cwd, home string) (string, error) {
	// Profile changes and host provider routing can also add filesystem or
	// network authority without changing the egg.yaml file.
	resolved := *policy
	resolved.FS = make([]string, 0, len(policy.FS))
	for _, entry := range policy.FS {
		mode, path, ok := strings.Cut(entry, ":")
		if !ok {
			mode, path = "rw", entry
		}
		path = expandTilde(path, home)
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		resolved.FS = append(resolved.FS, mode+":"+wingpolicy.CanonicalPolicyPath(path))
	}
	effective := ResolvePolicy(&resolved, agent, home)
	for i := range effective.Mounts {
		effective.Mounts[i].Source = wingpolicy.CanonicalPolicyPath(effective.Mounts[i].Source)
		effective.Mounts[i].Target = wingpolicy.CanonicalPolicyPath(effective.Mounts[i].Target)
	}
	data, err := json.Marshal(struct {
		Policy      *EggConfig
		Profile     AgentProfile
		Effective   EffectivePolicy
		TempDir     string
		ProviderURL string
	}{policy, Profile(agent), effective, wingpolicy.CanonicalPolicyPath(os.TempDir()), os.Getenv("WT_PROVIDER_BASE_URL")})
	return recoveryDigest(data), err
}

func RecoveryConfigDigest(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	return recoveryDigest(data), err
}

func NewRecoveryRecord(intent LaunchIntent, policy *EggConfig, home string) (RecoveryRecord, error) {
	r := RecoveryRecord{Intent: intent, Sandboxed: RequiresSandbox(policy, intent.Agent), ProviderHome: wingpolicy.CanonicalPolicyPath(home)}
	r.Intent.CWD = wingpolicy.CanonicalPolicyPath(intent.CWD)
	r.Intent.EggConfig = ""
	if policy.SourcePath != "" {
		path, err := filepath.EvalSymlinks(policy.SourcePath)
		if err != nil {
			return r, err
		}
		r.Intent.EggConfig, err = filepath.Abs(path)
		if err != nil {
			return r, err
		}
	}
	copy := *policy
	copy.SourcePath = r.Intent.EggConfig
	var err error
	r.ConfigSHA256, err = RecoveryConfigDigest(r.Intent.EggConfig)
	if err != nil {
		return r, err
	}
	r.PolicySHA256, err = RecoveryPolicyDigest(&copy, intent.Agent, r.Intent.CWD, r.ProviderHome)
	return r, err
}

func ReadRecoveryRecord(eggDir string) (RecoveryRecord, error) {
	var r RecoveryRecord
	path, err := recoveryRecordPath(eggDir)
	if err != nil {
		return r, err
	}
	file, err := openBoundRegularFile(path)
	if err != nil {
		return r, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil {
		return r, err
	}
	if len(data) > 64<<10 {
		return r, errors.New("recovery record too large")
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if r.Intent.Version != 1 || r.PolicySHA256 == "" {
		return r, errors.New("invalid recovery record")
	}
	return r, nil
}

func WriteRecoveryRecord(eggDir string, r RecoveryRecord) error {
	path, err := recoveryRecordPath(eggDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	r.ProviderHome = wingpolicy.CanonicalPolicyPath(r.ProviderHome)
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := atomicWritePrivate(path, data); err != nil {
		return err
	}
	return fsutil.SyncDirectory(filepath.Dir(path))
}

func UpdateRecoveryRecord(eggDir string, update func(*RecoveryRecord)) error {
	path, err := recoveryRecordPath(eggDir)
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	r, err := ReadRecoveryRecord(eggDir)
	if err != nil {
		return err
	}
	update(&r)
	return WriteRecoveryRecord(eggDir, r)
}

func ReadLaunchIntent(dir string) (LaunchIntent, error) {
	var intent LaunchIntent
	file, err := openBoundRegularFile(filepath.Join(dir, LaunchIntentFile))
	if err != nil {
		return intent, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil {
		return intent, err
	}
	if len(data) > 64<<10 {
		return intent, errors.New("launch intent too large")
	}
	if err := json.Unmarshal(data, &intent); err != nil {
		return intent, err
	}
	if intent.Version != 1 {
		return intent, errors.New("unsupported launch intent version")
	}
	return intent, nil
}

func WriteLaunchIntent(dir string, intent LaunchIntent) error {
	data, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	if err := atomicWritePrivate(filepath.Join(dir, LaunchIntentFile), data); err != nil {
		return err
	}
	return fsutil.SyncDirectory(dir)
}

func UpdateLaunchIntent(dir string, update func(*LaunchIntent)) error {
	if _, err := ReadRecoveryRecord(dir); err == nil {
		mismatch := false
		err := UpdateRecoveryRecord(dir, func(r *RecoveryRecord) {
			provider := r.Intent.ProviderSessionID
			update(&r.Intent)
			if provider != "" && r.Intent.ProviderSessionID != provider {
				r.Intent.ProviderSessionID = provider
				mismatch = true
			}
		})
		if err != nil {
			return err
		}
		if mismatch {
			return errors.New("provider identity does not match recovery record")
		}
		r, err := ReadRecoveryRecord(dir)
		if err != nil {
			return err
		}
		return WriteLaunchIntent(dir, r.Intent)
	} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) && filepath.Base(filepath.Dir(dir)) == "eggs" {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "launch.intent.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	intent, err := ReadLaunchIntent(dir)
	if errors.Is(err, os.ErrNotExist) { // legacy eggs need no migration
		return nil
	}
	if err != nil {
		return err
	}
	update(&intent)
	return WriteLaunchIntent(dir, intent)
}

func MarkDeliberateStop(dir, reason string) error {
	if _, err := ReadRecoveryRecord(dir); err == nil {
		if err := UpdateRecoveryRecord(dir, func(r *RecoveryRecord) { r.Stopped = true }); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) && filepath.Base(filepath.Dir(dir)) == "eggs" {
		return err
	}
	if err := atomicWritePrivate(filepath.Join(dir, DeliberateStopFile), []byte(reason+"\n")); err != nil {
		return err
	}
	return fsutil.SyncDirectory(dir)
}
