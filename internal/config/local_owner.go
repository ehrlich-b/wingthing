package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/fsutil"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// LocalOwner is the personal wing's durable authority. Relay enrollment adds an
// explicit binding; it never replaces ID or rewrites existing egg ownership.
type LocalOwner struct {
	ID       string              `yaml:"id"`
	Bindings []RelayOwnerBinding `yaml:"relay_bindings,omitempty"`
}

type RelayOwnerBinding struct {
	RelayURL string `yaml:"relay_url"`
	UserID   string `yaml:"user_id"`
}

// EnsureLocalOwner seeds an older account-backed wing with its existing owner,
// or creates an account-free identity. An existing identity always wins.
func EnsureLocalOwner(dir, legacyUserID string) (*LocalOwner, error) {
	return updateLocalOwner(dir, legacyUserID, nil)
}

// BindLocalOwnerRelay is an explicit, locally administered enrollment step.
// Merely loading a device credential does not grant its account this binding.
func BindLocalOwnerRelay(dir, relayURL, userID string) (*LocalOwner, error) {
	relayURL, err := ownerRelayURL(relayURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("relay owner identity is required")
	}
	return updateLocalOwner(dir, "", func(owner *LocalOwner) error {
		for _, binding := range owner.Bindings {
			if binding.RelayURL == relayURL {
				if binding.UserID != userID {
					return errors.New("relay already bound to another owner")
				}
				return nil
			}
		}
		owner.Bindings = append(owner.Bindings, RelayOwnerBinding{RelayURL: relayURL, UserID: userID})
		return nil
	})
}

// OwnerForRelay normalizes authenticated authority at the relay adapter only.
// Unbound accounts retain their own identity and cannot inherit local eggs.
func (o *LocalOwner) OwnerForRelay(relayURL, userID string) string {
	if o == nil || userID == "" {
		return userID
	}
	relayURL, err := ownerRelayURL(relayURL)
	if err != nil {
		return userID
	}
	for _, binding := range o.Bindings {
		if binding.RelayURL == relayURL && binding.UserID == userID {
			return o.ID
		}
	}
	return userID
}

func ownerRelayURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse owner relay URL: %w", err)
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("owner binding requires an HTTP relay URL without credentials, query or fragment")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func updateLocalOwner(dir, seed string, change func(*LocalOwner) error) (*LocalOwner, error) {
	if err := ensureStateDirectory(dir); err != nil {
		return nil, err
	}
	lock, err := acquireConfigLock(dir, ".local-owner.lock")
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	path := filepath.Join(dir, "local-owner.yaml")
	owner := &LocalOwner{}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("local-owner.yaml must be a private regular file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		decoder := yaml.NewDecoder(strings.NewReader(string(data)))
		decoder.KnownFields(true)
		if err := decoder.Decode(owner); err != nil {
			return nil, fmt.Errorf("read local owner: %w", err)
		}
		if owner.ID == "" || strings.TrimSpace(owner.ID) != owner.ID {
			return nil, errors.New("invalid persisted local owner identity")
		}
		if change == nil {
			return owner, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else {
		owner.ID = seed
		if owner.ID == "" {
			owner.ID = "local-" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
	}
	if change != nil {
		if err := change(owner); err != nil {
			return nil, err
		}
	}
	data, err := yaml.Marshal(owner)
	if err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(dir, ".local-owner-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return nil, err
	}
	if err := fsutil.SyncDirectory(dir); err != nil {
		return nil, err
	}
	return owner, nil
}
