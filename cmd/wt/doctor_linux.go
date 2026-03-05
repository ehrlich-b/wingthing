//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
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
			label := fmt.Sprintf("enforcing (%d profiles)", profileCount)
			if userns, uErr := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); uErr == nil {
				if strings.TrimSpace(string(userns)) == "1" {
					label += ", userns restricted"
				}
			}
			fmt.Printf("  %-14s %s\n", "apparmor:", label)
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

func doctorFix() error {
	// Check if AppArmor userns restriction is the issue.
	val, err := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns")
	if err != nil || strings.TrimSpace(string(val)) != "1" {
		// Not an AppArmor issue — check if sandbox works at all.
		if ok, _ := sandbox.CheckCapability(); ok {
			fmt.Println("wt doctor --fix: sandbox is working, nothing to fix")
			return nil
		}
		fmt.Println("wt doctor --fix: sandbox not available, but no auto-fix for this issue")
		fmt.Println("run: sudo sysctl -w kernel.unprivileged_userns_clone=1")
		return nil
	}

	// Resolve the wt binary path for the AppArmor profile.
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve wt binary path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve wt binary symlinks: %w", err)
	}

	profileContent := fmt.Sprintf(`abi <abi/4.0>,
profile wingthing %s flags=(unconfined) {
  userns,
}
`, exe)

	profilePath := "/etc/apparmor.d/wingthing"

	// Check if profile already exists and matches.
	if existing, readErr := os.ReadFile(profilePath); readErr == nil {
		if string(existing) == profileContent {
			fmt.Println("AppArmor profile already installed at", profilePath)
			// Try to reload it in case it wasn't loaded.
			if os.Geteuid() == 0 {
				exec.Command("apparmor_parser", "-r", profilePath).Run()
			}
			if ok, _ := sandbox.CheckCapability(); ok {
				fmt.Println("sandbox is working")
				return nil
			}
			fmt.Println("profile exists but sandbox still not working — try: sudo apparmor_parser -r", profilePath)
			return nil
		}
	}

	// If running as root, just do it.
	if os.Geteuid() == 0 {
		fmt.Println("installing AppArmor profile for wt...")
		if err := os.WriteFile(profilePath, []byte(profileContent), 0644); err != nil {
			return fmt.Errorf("write %s: %w", profilePath, err)
		}
		cmd := exec.Command("apparmor_parser", "-r", profilePath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("apparmor_parser -r %s: %w", profilePath, err)
		}
		fmt.Println("AppArmor profile installed and loaded")

		// Verify.
		if ok, _ := sandbox.CheckCapability(); ok {
			fmt.Println("sandbox is now working")
		} else {
			fmt.Println("warning: profile loaded but sandbox probe still fails")
		}
		return nil
	}

	// Not root — print a script the user can run.
	fmt.Println("AppArmor is blocking unprivileged user namespaces (apparmor_restrict_unprivileged_userns=1).")
	fmt.Println("wt needs a small AppArmor profile to create sandboxes.")
	fmt.Println()
	fmt.Println("Run this script, or run: sudo wt doctor --fix")
	fmt.Println()
	fmt.Println("--- cut here ---")
	fmt.Printf(`#!/bin/bash
# Install an AppArmor profile that allows wt to create user namespaces.
# This grants the 'userns' permission to the wt binary only — no other
# programs are affected. The profile is 'unconfined' so wt keeps all
# other permissions it normally has.
#
# Alternatively, run: sudo wt doctor --fix

cat > %s << 'PROFILE'
%sPROFILE

# Load the profile into the kernel.
apparmor_parser -r %s

echo "done — wt sandbox should now work"
`, profilePath, profileContent, profilePath)
	fmt.Println("--- cut here ---")
	return nil
}
