//go:build linux

package sandbox

// Admission checks declaration conflicts, while the private namespace verifies
// the actual recursive mounts and installs Landlock before provider exec.
func ValidateLaunchPreparation(cfg Config) (bool, error) {
	if err := validateDeclaredCallbackDirs(cfg, "linux"); err != nil {
		return false, err
	}
	return false, validateLinuxProtectedTargets(cfg)
}
func checkSeatbeltDirectoryWrites(Config, string) error { return nil }
func checkSeatbeltFileReads(Config, string) DirectoryWriteCheck {
	return DirectoryWriteCheck{Reason: "final Mac read policy requires the Mac policy builder"}
}
