//go:build !linux

package main

import "errors"

func prepareSharedAgentHome(string, []string) error {
	return errors.New("shared-host agent homes require Linux")
}

func installSharedAgentBinary(string, string, string) error {
	return errors.New("shared-host agent runtimes require Linux")
}
