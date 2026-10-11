//go:build darwin

package sandbox

// ValidateLaunchPreparation runs the exact final policy builder without creating
// a sandbox or starting a native process. New rechecks the same requirements.
func ValidateLaunchPreparation(cfg Config) (bool, error) {
	_, err := buildCheckedProfile(cfg)
	return true, err
}

func checkSeatbeltDirectoryWrites(cfg Config, path string) error {
	return checkDirectoryWrites(buildProfile(cfg), []string{path}, false)
}
