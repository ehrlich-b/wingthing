package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type TokenStore struct {
	Dir string
}

func NewTokenStore(dir string) *TokenStore {
	return &TokenStore{Dir: dir}
}

func (s *TokenStore) tokenPath() string {
	return filepath.Join(s.Dir, "device_token.yaml")
}

func (s *TokenStore) Save(token *DeviceToken) error {
	data, err := yaml.Marshal(token)
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}
	if err := os.WriteFile(s.tokenPath(), data, 0600); err != nil {
		return fmt.Errorf("write token: %w", err)
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
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete token: %w", err)
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
