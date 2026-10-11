//go:build !darwin

package sandbox

// The Linux final mount policy is constructed inside a native namespace. Its
// enforcement cannot run in a metadata mirror; callers must retain an unrun
// gate. Do not substitute a second filesystem-policy interpreter for it.
func ValidateLaunchPreparation(cfg Config) (bool, error) {
	if err := validateDeclaredCallbackDirs(cfg, "linux"); err != nil {
		return false, err
	}
	return false, refuseProtectedWriteTargets(cfg, "linux")
}

func checkSeatbeltDirectoryWrites(Config, string) error { return nil }
