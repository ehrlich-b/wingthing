//go:build windows

package config

import "errors"

// Preview provider bindings require no-follow reads and owner checks that are
// only implemented for Unix. A present binding fails closed on Windows.
func readProviderHomeBinding(path string, limit int64) ([]byte, error) {
	return nil, errors.New("provider-home bindings are unsupported on Windows")
}

func statProviderPathIdentity(path string) (providerPathID, error) {
	return providerPathID{}, errors.New("provider-home bindings are unsupported on Windows")
}

func validateProviderHomeDirectory(path string) error {
	return errors.New("provider-home bindings are unsupported on Windows")
}
