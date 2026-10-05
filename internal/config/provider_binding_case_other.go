//go:build !darwin

package config

// Other platforms keep exact, case-sensitive binding comparison.
const providerHomeFoldsCase = false

func requireExactProviderHomeSpelling(string) error { return nil }
