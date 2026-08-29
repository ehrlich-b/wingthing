package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ehrlich-b/wingthing/internal/fsutil"
	"gopkg.in/yaml.v3"
)

type TokenStore struct {
	Dir      string
	filename string
}

func NewTokenStore(dir string) *TokenStore {
	return &TokenStore{Dir: dir, filename: "device_token.yaml"}
}

// NewLocalTokenStore keeps the credential minted by `wt serve --local`
// separate from the profile's ordinary hosted/private-roost login. Local serve
// is a second authority, not a reason to destroy the user's existing login.
func NewLocalTokenStore(dir string) *TokenStore {
	return &TokenStore{Dir: dir, filename: "local_device_token.yaml"}
}

func (s *TokenStore) tokenPath() string {
	filename := s.filename
	if filename == "" { // Preserve zero-value compatibility inside the package.
		filename = "device_token.yaml"
	}
	return filepath.Join(s.Dir, filename)
}

func (s *TokenStore) Save(token *DeviceToken) error {
	data, err := yaml.Marshal(token)
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}
	temporary, err := os.CreateTemp(s.Dir, ".device-token-*")
	if err != nil {
		return fmt.Errorf("create temporary token: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return fmt.Errorf("protect temporary token: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary token: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary token: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary token: %w", err)
	}
	if err := os.Rename(temporaryPath, s.tokenPath()); err != nil {
		return fmt.Errorf("replace token: %w", err)
	}
	committed = true
	if err := fsutil.SyncDirectory(s.Dir); err != nil {
		return fmt.Errorf("persist token replacement: %w", err)
	}
	return nil
}

func (s *TokenStore) Load() (*DeviceToken, error) {
	data, err := os.ReadFile(s.tokenPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read token: %w", err)
	}

	var token DeviceToken
	if err := yaml.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}
	return &token, nil
}

func (s *TokenStore) Delete() error {
	err := os.Remove(s.tokenPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("delete token: %w", err)
	}
	if err := fsutil.SyncDirectory(s.Dir); err != nil {
		return fmt.Errorf("persist token deletion: %w", err)
	}
	return nil
}

func (s *TokenStore) IsValid(token *DeviceToken) bool {
	if token == nil {
		return false
	}
	if token.ExpiresAt == 0 {
		return true
	}
	return time.Now().Unix() < token.ExpiresAt
}
