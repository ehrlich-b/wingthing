package eggclient

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	agentpkg "github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

type providerResumeRegistry struct {
	Mu     sync.Mutex
	Active map[string]string
}

var BrowserProviderResumes = providerResumeRegistry{Active: make(map[string]string)}

const ProviderResumeMetadataFile = "provider.resume"

func ProviderResumeKey(home, agent, providerSessionID string) string {
	hash := sha256.Sum256([]byte(wingpolicy.CanonicalPolicyPath(home)))
	return hex.EncodeToString(hash[:]) + "\x00" + agent + "\x00" + providerSessionID
}

func providerResumeMetadata(key, sourceSessionID string) []byte {
	homeHash, rest, _ := strings.Cut(key, "\x00")
	agent, providerSessionID, _ := strings.Cut(rest, "\x00")
	return []byte(fmt.Sprintf("home_hash=%s\nagent=%s\nprovider_session_id=%s\nsource_session_id=%s\n", homeHash, agent, providerSessionID, sourceSessionID))
}

func verifyProviderResumeReservation(cfg *config.Config, home, agent, providerSessionID, sourceSessionID, wingSessionID string) error {
	path := filepath.Join(cfg.Dir, "eggs", wingSessionID, ProviderResumeMetadataFile)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("provider resume reservation is unavailable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("provider resume reservation is unavailable")
	}
	values := egg.ParseChatMeta(string(data))
	homeHash, rest, _ := strings.Cut(ProviderResumeKey(home, agent, providerSessionID), "\x00")
	wantAgent, wantID, _ := strings.Cut(rest, "\x00")
	if values["home_hash"] != homeHash || values["agent"] != wantAgent || values["provider_session_id"] != wantID || values["source_session_id"] != sourceSessionID {
		return errors.New("provider resume reservation does not match the requested conversation")
	}
	return nil
}

func ActiveProviderResumeConflict(cfg *config.Config, key, exceptSessionID string, alive func(string) bool) bool {
	homeHash, rest, _ := strings.Cut(key, "\x00")
	agent, providerSessionID, _ := strings.Cut(rest, "\x00")
	entries, err := os.ReadDir(filepath.Join(cfg.Dir, "eggs"))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == exceptSessionID {
			continue
		}
		dir := filepath.Join(cfg.Dir, "eggs", entry.Name())
		metadata, err := os.ReadFile(filepath.Join(dir, ProviderResumeMetadataFile))
		if err != nil {
			continue
		}
		values := egg.ParseChatMeta(string(metadata))
		if values["home_hash"] == homeHash && values["agent"] == agent && values["provider_session_id"] == providerSessionID && alive(dir) {
			return true
		}
	}
	return false
}

func (r *providerResumeRegistry) Reserve(cfg *config.Config, home, agent, providerSessionID, sourceSessionID, wingSessionID string) (func(bool), error) {
	return r.reserveWithAlive(cfg, home, agent, providerSessionID, sourceSessionID, wingSessionID, func(dir string) bool {
		_, alive := ReadAliveEggPID(dir)
		return alive
	})
}

func (r *providerResumeRegistry) reserveWithAlive(cfg *config.Config, home, agent, providerSessionID, sourceSessionID, wingSessionID string, alive func(string) bool) (func(bool), error) {
	key := ProviderResumeKey(home, agent, providerSessionID)
	r.Mu.Lock()
	defer r.Mu.Unlock()
	if _, exists := r.Active[key]; exists {
		return nil, errors.New("provider conversation is already being resumed")
	}
	eggsDir := filepath.Join(cfg.Dir, "eggs")
	if err := os.MkdirAll(eggsDir, 0o700); err != nil {
		return nil, fmt.Errorf("create resume reservation directory: %w", err)
	}
	keyHash := sha256.Sum256([]byte(key))
	lockPath := filepath.Join(eggsDir, ".provider-resume-"+hex.EncodeToString(keyHash[:])+".lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open resume reservation lock: %w", err)
	}
	keepLock := false
	defer func() {
		if !keepLock {
			_ = lock.Close()
		}
	}()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("provider conversation is already being resumed")
		}
		return nil, fmt.Errorf("lock resume reservation: %w", err)
	}
	// Keep the descriptor locked through pending launch and PID publication.
	// Never unlink this file: contenders must always lock the same inode.
	if ActiveProviderResumeConflict(cfg, key, wingSessionID, alive) {
		return nil, errors.New("provider conversation is already running in another session")
	}
	sessionDir := filepath.Join(cfg.Dir, "eggs", wingSessionID)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return nil, fmt.Errorf("create resume reservation: %w", err)
	}
	metadataPath := filepath.Join(sessionDir, ProviderResumeMetadataFile)
	if err := daemonctl.WriteAtomicMetadataFile(metadataPath, providerResumeMetadata(key, sourceSessionID), 0o600); err != nil {
		return nil, fmt.Errorf("persist resume reservation: %w", err)
	}
	r.Active[key] = wingSessionID
	keepLock = true
	var once sync.Once
	return func(spawned bool) {
		once.Do(func() {
			r.Mu.Lock()
			if r.Active[key] == wingSessionID {
				delete(r.Active, key)
			}
			if !spawned {
				_ = os.Remove(metadataPath)
			}
			_ = lock.Close()
			r.Mu.Unlock()
		})
	}, nil
}

func SessionResumeStatus(sessionDir, agent, cwd string) (bool, string) {
	definition, ok := agentpkg.LookupDefinition(agent)
	if !ok || definition.ResumeFlag == "" || egg.Profile(agent).SessionDir == "" {
		return false, "agent does not support provider resume"
	}
	metaPath := filepath.Join(sessionDir, "chat.meta")
	metaInfo, err := os.Lstat(metaPath)
	if err != nil || !metaInfo.Mode().IsRegular() {
		return false, "provider conversation was not captured"
	}
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return false, "provider conversation was not captured"
	}
	meta := egg.ParseChatMeta(string(metaData))
	if !ValidProviderSessionID(meta["agent_session_id"]) || meta["agent"] != agent || wingpolicy.CanonicalSessionPath(meta["cwd"]) != wingpolicy.CanonicalSessionPath(cwd) {
		return false, "provider conversation metadata is invalid"
	}
	reservationInfo, err := os.Lstat(filepath.Join(sessionDir, ProviderResumeMetadataFile))
	if err != nil || !reservationInfo.Mode().IsRegular() {
		return false, "provider conversation identity was not verified"
	}
	reservationData, err := os.ReadFile(filepath.Join(sessionDir, ProviderResumeMetadataFile))
	if err != nil {
		return false, "provider conversation identity was not verified"
	}
	reservation := egg.ParseChatMeta(string(reservationData))
	if reservation["agent"] != agent || reservation["provider_session_id"] != meta["agent_session_id"] {
		return false, "provider conversation identity was not verified"
	}
	info, err := os.Lstat(filepath.Join(sessionDir, "chat.jsonl.gz"))
	if err != nil || !info.Mode().IsRegular() {
		return false, "provider conversation was not captured"
	}
	return true, ""
}

func ValidProviderSessionID(id string) bool {
	return id != "" && filepath.Base(id) == id && id != "." && id != ".." && !strings.ContainsAny(id, "\x00\r\n") && len(id) <= 240
}

func PrepareBrowserResume(cfg *config.Config, wingCfg *config.WingConfig, start ws.PTYStart, userPaths []string, sharedHost bool) (providerSessionID, cwd string, release func(bool), err error) {
	userPaths = wingpolicy.CanonicalPaths(userPaths)
	if err := ValidateSessionID(start.ResumeSessionID); err != nil {
		return "", "", nil, errors.New("invalid resume session ID")
	}
	if start.ResumeSessionID == start.SessionID {
		return "", "", nil, errors.New("resume source must differ from the new session")
	}
	sourceDir := filepath.Join(cfg.Dir, "eggs", start.ResumeSessionID)
	if ReadEggOwner(sourceDir) == "" || ReadEggOwner(sourceDir) != start.UserID {
		return "", "", nil, errors.New("resume session not found or not owned by caller")
	}
	if _, alive := ReadAliveEggPID(sourceDir); alive {
		return "", "", nil, errors.New("resume source is still active; attach to it instead")
	}
	agent, cwd := ReadEggMeta(sourceDir)
	if agent == "" || cwd == "" || agent != start.Agent {
		return "", "", nil, errors.New("resume source does not match the requested agent")
	}
	cwd = wingpolicy.CanonicalSessionPath(cwd)
	if start.CWD != "" && wingpolicy.CanonicalSessionPath(start.CWD) != cwd {
		return "", "", nil, errors.New("resume source does not match the requested working directory")
	}
	if wingpolicy.IsMemberRole(start.OrgRole) && len(userPaths) == 0 || len(userPaths) > 0 && !wingpolicy.IsUnderPaths(cwd, userPaths) {
		return "", "", nil, errors.New("resume source is outside current path policy")
	}
	if ok, reason := SessionResumeStatus(sourceDir, agent, cwd); !ok {
		return "", "", nil, errors.New(reason)
	}
	home := EffectiveSessionHome(cfg, EggIdentity{UserID: start.UserID, OrgWing: wingCfg.Org != "", SharedHost: sharedHost})
	metaData, err := os.ReadFile(filepath.Join(sourceDir, "chat.meta"))
	if err != nil {
		return "", "", nil, errors.New("provider conversation metadata is unavailable")
	}
	providerSessionID = egg.ParseChatMeta(string(metaData))["agent_session_id"]
	if !ValidProviderSessionID(providerSessionID) {
		return "", "", nil, errors.New("provider conversation metadata is invalid")
	}
	release, err = BrowserProviderResumes.Reserve(cfg, home, agent, providerSessionID, start.ResumeSessionID, start.SessionID)
	if err != nil {
		return "", "", nil, err
	}
	providerSessionID, err = egg.RestoreSessionHistory(agent, cwd, sourceDir, home)
	if err != nil {
		release(false)
		return "", "", nil, fmt.Errorf("restore provider conversation: %w", err)
	}
	return providerSessionID, cwd, release, nil
}
