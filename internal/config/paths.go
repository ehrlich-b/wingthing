package config

import (
	"os"
	"path/filepath"
)

func GetUserConfigDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(homeDir, ".wingthing"), nil
}

func GetProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	// Walk up directory tree to find .git or .wingthing directory
	dir := wd
	for {
		// Check for .wingthing directory
		wingthingDir := filepath.Join(dir, ".wingthing")
		if _, err := os.Stat(wingthingDir); err == nil {
			return dir, nil
		}

		// Check for .git directory (project root)
		gitDir := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			return dir, nil
		}

		// Move to parent directory
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root directory, use current working directory
			return wd, nil
		}
		dir = parent
	}
}

func EnsureConfigDirs(userConfigDir, projectDir string) error {
	// Ensure user config directory exists
	if err := os.MkdirAll(userConfigDir, 0755); err != nil {
		return err
	}

	// Ensure project .wingthing directory exists
	projectConfigDir := filepath.Join(projectDir, ".wingthing")
	if err := os.MkdirAll(projectConfigDir, 0755); err != nil {
		return err
	}

	return nil
}
