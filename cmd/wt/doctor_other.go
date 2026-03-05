//go:build !linux

package main

import "fmt"

func printSystemSection() {
	// System diagnostics only relevant on Linux
}

func doctorFix() error {
	fmt.Println("wt doctor --fix: no fixable issues detected on this platform")
	return nil
}
