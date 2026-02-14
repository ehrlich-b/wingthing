package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type FileEntry struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	ModTime   int64  `json:"mod_time"`
	WingID string `json:"wing_id"`
}

type Manifest struct {
	WingID string      `json:"wing_id"`
	Files     []FileEntry `json:"files"`
	CreatedAt int64       `json:"created_at"`
}

func BuildManifest(dir string, wingID string) (*Manifest, error) {
	m := &Manifest{
		WingID: wingID,
		CreatedAt: time.Now().UTC().Unix(),
	}

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip .conflicts directory
			if info.Name() == ".conflicts" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		hash := sha256.Sum256(data)

		m.Files = append(m.Files, FileEntry{
			Path:      rel,
			SHA256:    hex.EncodeToString(hash[:]),
			Size:      info.Size(),
			ModTime:   info.ModTime().UTC().Unix(),
			WingID: wingID,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	return m, nil
}

func (m *Manifest) MarshalJSON() ([]byte, error) {
	type Alias Manifest
	return json.Marshal((*Alias)(m))
}

func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
