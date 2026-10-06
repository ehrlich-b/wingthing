package egg

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ehrlich-b/wingthing/internal/fsutil"
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
	if err := atomicWritePrivate(filepath.Join(dir, DeliberateStopFile), []byte(reason+"\n")); err != nil {
		return err
	}
	return fsutil.SyncDirectory(dir)
}
