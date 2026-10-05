//go:build !linux

package eggclient

import "errors"

func PrepareSharedAgentHome(string, []string) error {
	return errors.New("shared-host agent homes require Linux")
}

func InstallSharedAgentBinary(string, string, string) error {
	return errors.New("shared-host agent runtimes require Linux")
}
