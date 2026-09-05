// canary-agent is the browser E2E suite's stand-in for an agent CLI. The
// sealed shared-host runtime only projects self-contained native binaries, so
// unlike the Linux battery's probe-oriented mock-agent this one stays
// interactive: it prints a banner and echoes each input line, giving the
// Playwright driver a live session for input, reattach, and kill flows.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

type canaryClaudeProfile struct {
	HasCompletedOnboarding bool   `json:"hasCompletedOnboarding"`
	Marker                 string `json:"wtCanaryProfile"`
}

func printProfileState() {
	home := os.Getenv("HOME")
	configDir := os.Getenv("CLAUDE_CONFIG_DIR")
	dirOK := configDir == filepath.Join(home, ".claude")
	data, err := os.ReadFile(filepath.Join(configDir, ".claude.json"))
	if os.IsNotExist(err) {
		fmt.Printf("CANARY_PROFILE_EMPTY dir_ok=%t\r\n", dirOK)
		return
	}
	if err != nil {
		fmt.Printf("CANARY_PROFILE_ERROR dir_ok=%t\r\n", dirOK)
		return
	}
	var profile canaryClaudeProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		fmt.Printf("CANARY_PROFILE_ERROR dir_ok=%t\r\n", dirOK)
		return
	}
	fmt.Printf("CANARY_PROFILE_READY marker=%s dir_ok=%t onboarding=%t\r\n",
		profile.Marker, dirOK, profile.HasCompletedOnboarding)
}

func printModelPolicy() {
	policyOK := false
	model := ""
	for i := 1; i+1 < len(os.Args); i++ {
		if os.Args[i] == "--settings" {
			policyOK = os.Args[i+1] == `{"env":{"CLAUDE_CODE_EFFORT_LEVEL":"xhigh"}}`
		}
		if os.Args[i] == "--model" {
			model = os.Args[i+1]
		}
	}
	var prefs struct {
		Model string `json:"model"`
		Theme string `json:"theme"`
	}
	data, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".claude", "settings.json"))
	json.Unmarshal(data, &prefs)
	fmt.Printf("CANARY_MODEL_POLICY ok=%t saved_model=%s theme=%s\r\n", policyOK && model == "claude-sonnet-5", prefs.Model, prefs.Theme)
}

func main() {
	if slices.Contains(os.Args[1:], "--version") {
		fmt.Println("canary-agent v1")
		return
	}
	host, _ := os.Hostname()
	wd, _ := os.Getwd()
	printProfileState()
	printModelPolicy()
	fmt.Printf("CANARY_SHELL_READY host=%s cwd=%s\r\n> ", host, wd)

	buf := make([]byte, 1024)
	line := make([]byte, 0, 256)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		for _, b := range buf[:n] {
			switch b {
			case '\r', '\n':
				if string(line) == "CANARY_SET_THEME" {
					path := filepath.Join(os.Getenv("HOME"), ".claude", "settings.json")
					data, err := os.ReadFile(path)
					var prefs map[string]any
					if err == nil {
						err = json.Unmarshal(data, &prefs)
					}
					if err == nil && prefs != nil {
						prefs["theme"] = "alice-edited"
						data, err = json.Marshal(prefs)
						if err == nil {
							err = os.WriteFile(path, data, 0600)
						}
					}
					fmt.Printf("\r\nCANARY_SETTINGS_SAVED ok=%t\r\n", err == nil && prefs != nil)
					printModelPolicy()
				}
				fmt.Printf("\r\nECHO:%s\r\n> ", line)
				line = line[:0]
			case 0x03, 0x04: // ^C / ^D end the session
				return
			default:
				line = append(line, b)
			}
		}
	}
}
