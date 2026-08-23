//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

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
	if overlayFSAvailable() {
		fmt.Printf("  %-14s %s\n", "overlayfs:", "available")
	} else {
		fmt.Printf("  %-14s %s\n", "overlayfs:", "not available")
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

func overlayFSAvailable() bool {
	filesystems, _ := os.ReadFile("/proc/filesystems")
	kernelRelease, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	if overlayFSAvailableFrom(filesystems, strings.TrimSpace(string(kernelRelease)), []string{"/lib/modules", "/usr/lib/modules"}) {
		return true
	}
	modinfo, err := exec.LookPath("modinfo")
	if err != nil {
		return false
	}
	output, err := exec.Command(modinfo, "-F", "filename", "overlay").Output()
	return err == nil && strings.TrimSpace(string(output)) != ""
}

func overlayFSAvailableFrom(filesystems []byte, kernelRelease string, moduleRoots []string) bool {
	for _, line := range strings.Split(string(filesystems), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[len(fields)-1] == "overlay" {
			return true
		}
	}
	if kernelRelease == "" {
		return false
	}
	for _, root := range moduleRoots {
		matches, err := filepath.Glob(filepath.Join(root, kernelRelease, "kernel", "fs", "overlayfs", "overlay.ko*"))
		if err == nil && len(matches) > 0 {
			return true
		}
	}
	return false
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
	if err := validateAppArmorExecutablePath(exe); err != nil {
		return err
	}
	fmt.Println("AppArmor profile executable:", exe)

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
				cmd := exec.Command("apparmor_parser", "-r", profilePath)
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				if err := cmd.Run(); err != nil {
					return fmt.Errorf("apparmor_parser -r %s: %w", profilePath, err)
				}
				fmt.Println("AppArmor profile loaded for", exe)
				fmt.Println("verify from the unprivileged account with: wt doctor")
				return nil
			}
			if ok, _ := sandbox.CheckCapability(); ok {
				fmt.Println("sandbox is working")
				return nil
			}
			fmt.Println("profile exists; reload it with: sudo apparmor_parser -r", profilePath)
			return nil
		}
		if previous := appArmorProfileExecutable(existing); previous != "" {
			fmt.Printf("replacing AppArmor profile executable %s with %s\n", previous, exe)
		} else {
			fmt.Println("replacing existing AppArmor profile at", profilePath)
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
		fmt.Println("AppArmor profile installed and loaded for", exe)
		fmt.Println("verify from the unprivileged account with: wt doctor")
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

// validateAppArmorExecutablePath ensures the profile cannot be redirected by
// replacing the executable or one of its parent directories. AppArmor path
// attachment has no content hash, so the complete path must be controlled by
// root. This deliberately rejects binaries run from a checkout, download
// directory, user home, or /tmp; install wt to a stable system path first.
func validateAppArmorExecutablePath(path string) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return fmt.Errorf("AppArmor profile requires an absolute executable path; install wt to /usr/local/bin/wt first")
	}
	if strings.ContainsAny(clean, " \t\r\n{}") {
		return fmt.Errorf("AppArmor profile executable path contains unsupported characters: %q", clean)
	}

	for component := clean; ; component = filepath.Dir(component) {
		info, err := os.Stat(component)
		if err != nil {
			return fmt.Errorf("inspect AppArmor executable path component %s: %w", component, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("inspect ownership of AppArmor executable path component %s", component)
		}
		if stat.Uid != 0 || info.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("AppArmor profile requires a root-owned, root-writable-only executable path; unsafe component %s has owner uid %d and mode %04o. Install wt to /usr/local/bin/wt, then run sudo /usr/local/bin/wt doctor --fix",
				component, stat.Uid, info.Mode().Perm())
		}
		if component == string(filepath.Separator) {
			break
		}
	}

	info, err := os.Stat(clean)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("AppArmor profile executable must be a regular file: %s", clean)
	}
	return nil
}

func appArmorProfileExecutable(profile []byte) string {
	for _, line := range strings.Split(string(profile), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 3 && fields[0] == "profile" && fields[1] == "wingthing" {
			return fields[2]
		}
	}
	return ""
}
