//go:build linux

package main

import (
	"fmt"
	"os"
	"strings"
)

func printSystemSection() {
	fmt.Println("System:")

	// kernel
	if data, err := os.ReadFile("/proc/version"); err == nil {
		// Extract just the version string (first 3 fields)
		parts := strings.Fields(strings.TrimSpace(string(data)))
		if len(parts) >= 3 {
			fmt.Printf("  %-14s %s\n", "kernel:", parts[2])
		}
	}

	// distro
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				name := strings.TrimPrefix(line, "PRETTY_NAME=")
				name = strings.Trim(name, "\"")
				fmt.Printf("  %-14s %s\n", "distro:", name)
				break
			}
		}
	}

	// userns
	if data, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if strings.TrimSpace(string(data)) == "1" {
			fmt.Printf("  %-14s %s\n", "userns:", "enabled")
		} else {
			fmt.Printf("  %-14s %s\n", "userns:", "disabled")
		}
	} else {
		// Sysctl missing — kernel allows it by default (most modern distros)
		fmt.Printf("  %-14s %s\n", "userns:", "enabled (no sysctl gate)")
	}

	// overlayfs
	if data, err := os.ReadFile("/proc/filesystems"); err == nil {
		if strings.Contains(string(data), "overlay") {
			fmt.Printf("  %-14s %s\n", "overlayfs:", "available")
		} else {
			fmt.Printf("  %-14s %s\n", "overlayfs:", "not available")
		}
	}

	// apparmor
	if data, err := os.ReadFile("/sys/module/apparmor/parameters/enabled"); err == nil {
		if strings.TrimSpace(string(data)) == "Y" {
			profileCount := 0
			if profiles, pErr := os.ReadFile("/sys/kernel/security/apparmor/profiles"); pErr == nil {
				for _, line := range strings.Split(string(profiles), "\n") {
					if strings.TrimSpace(line) != "" {
						profileCount++
					}
				}
			}
			fmt.Printf("  %-14s %s (%d profiles)\n", "apparmor:", "enforcing", profileCount)
		} else {
			fmt.Printf("  %-14s %s\n", "apparmor:", "disabled")
		}
	}

	// selinux
	if data, err := os.ReadFile("/sys/fs/selinux/enforce"); err == nil {
		if strings.TrimSpace(string(data)) == "1" {
			fmt.Printf("  %-14s %s\n", "selinux:", "enforcing")
		} else {
			fmt.Printf("  %-14s %s\n", "selinux:", "permissive")
		}
	}

	// cgroup v2
	if data, err := os.ReadFile("/proc/mounts"); err == nil {
		if strings.Contains(string(data), "cgroup2") {
			fmt.Printf("  %-14s %s\n", "cgroup v2:", "mounted")
		} else {
			fmt.Printf("  %-14s %s\n", "cgroup v2:", "not mounted")
		}
	}

	fmt.Println()
}
