package main

import (
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

// directAgentSandboxConfig applies the same agent capabilities used by the
// interactive egg path to non-interactive `wt run` tasks. Keeping these paths
// in sync is important: an agent that works in a terminal must not silently
// lose its network or persistent state when it is used headlessly.
func directAgentSandboxConfig(agentName, isolation, home string, mountPaths []string) (sandbox.Config, error) {
	return directAgentSandboxConfigWithPolicy(agentName, isolation, home, mountPaths, false)
}

func directAgentSandboxConfigWithPolicy(agentName, isolation, home string, mountPaths []string, sharedHost bool) (sandbox.Config, error) {
	return directAgentSandboxConfigForTask(&egg.EggConfig{}, agentName, isolation, home, "", mountPaths, sharedHost)
}

func directAgentSandboxConfigForTask(eggCfg *egg.EggConfig, agentName, isolation, home, cwd string, mountPaths []string, sharedHost bool) (sandbox.Config, error) {
	if eggCfg == nil {
		eggCfg = &egg.EggConfig{}
	}
	eggCfg = eggConfigWithResolvedFSPaths(eggCfg, cwd)
	profile := egg.Profile(agentName)
	policy, err := egg.ResolvePolicyWithProvider(eggCfg, agentName, home, os.Getenv("WT_PROVIDER_BASE_URL"))
	if err != nil {
		return sandbox.Config{}, err
	}
	domains := policy.Domains
	declared := eggCfg.ToSandboxConfig(home)
	mounts := make([]sandbox.Mount, 0, len(declared.Mounts)+len(mountPaths)+len(profile.WriteDirs)+len(profile.WriteRegex))
	seen := make(map[string]bool)
	appendMount := func(m sandbox.Mount) {
		key := m.Source + "\x00" + m.Target + "\x00" + boolKey(m.ReadOnly) + boolKey(m.UseRegex)
		if seen[key] {
			return
		}
		seen[key] = true
		mounts = append(mounts, m)
	}

	if !sharedHost {
		for _, mount := range declared.Mounts {
			appendMount(mount)
		}
	}
	for _, path := range mountPaths {
		appendMount(sandbox.Mount{Source: path, Target: path})
	}
	if home != "" {
		for _, dir := range profile.WriteRegex {
			path := filepath.Join(home, dir)
			appendMount(sandbox.Mount{Source: path, Target: path, UseRegex: true})
		}
		for _, dir := range profile.WriteDirs {
			path := filepath.Join(home, dir)
			appendMount(sandbox.Mount{Source: path, Target: path})
		}
	}

	netNeed := sandbox.NetworkNone
	if sandbox.ParseLevel(isolation) >= sandbox.Network {
		netNeed = sandbox.NetworkFull
	}
	if profileNeed := sandbox.NetworkNeedFromDomains(domains); profileNeed > netNeed {
		netNeed = profileNeed
	}

	result := sandbox.Config{
		Mounts:   mounts,
		Domains:  domains,
		CPULimit: declared.CPULimit,
		MemLimit: declared.MemLimit,
		MaxFDs:   declared.MaxFDs,
		PidLimit: declared.PidLimit,
		UserHome: home,
		Trace:    declared.Trace,
	}
	if sharedHost {
		result.Deny = []string{"/"}
		for _, path := range sharedHostSystemReadPaths() {
			appendMount(sandbox.Mount{Source: path, Target: path, ReadOnly: true})
		}
		result.Mounts = mounts
	} else {
		result.Deny = declared.Deny
		result.DenyWrite = declared.DenyWrite
	}
	result.NetworkNeed = netNeed
	return result, nil
}

// eggConfigWithResolvedFSPaths matches the interactive egg path: relative fs
// rules are rooted at the task working directory before the sandbox sees them.
func eggConfigWithResolvedFSPaths(cfg *egg.EggConfig, cwd string) *egg.EggConfig {
	resolved := *cfg
	resolved.FS = append([]string(nil), cfg.FS...)
	if cwd == "" {
		return &resolved
	}
	for i, entry := range resolved.FS {
		mode, path, ok := strings.Cut(entry, ":")
		if !ok {
			mode = "rw"
			path = entry
		}
		if path == "." || path == "./" {
			path = cwd
		} else if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~") {
			path = filepath.Join(cwd, path)
		}
		resolved.FS[i] = mode + ":" + path
	}
	return &resolved
}

func boolKey(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

// prepareDirectAgentState creates only the agent-owned writable directories;
// user-requested project mounts must already exist and are never synthesized.
func prepareDirectAgentState(agentName, home string) error {
	if home == "" {
		return nil
	}
	profile := egg.Profile(agentName)
	for _, dir := range append(append([]string(nil), profile.WriteRegex...), profile.WriteDirs...) {
		if err := os.MkdirAll(filepath.Join(home, dir), 0700); err != nil {
			return err
		}
	}
	return nil
}

// directAgentEnv preserves the caller's authentication and CLI environment.
// The Linux sandbox provides its own minimal environment by default, so the
// direct task path must override it just as the interactive egg path does.
func directAgentEnv(agentName, home string, proxyPort int) []string {
	return directAgentEnvWithPolicy(agentName, home, proxyPort, true)
}

func directAgentEnvWithPolicy(agentName, home string, proxyPort int, inheritHost bool) []string {
	profile := egg.Profile(agentName)
	envMap := make(map[string]string)
	if inheritHost {
		for _, entry := range os.Environ() {
			key, value, ok := strings.Cut(entry, "=")
			if ok {
				envMap[key] = value
			}
		}
	} else {
		for _, key := range []string{"PATH", "TERM", "LANG", "USER", "SHELL", "TMPDIR"} {
			if value := os.Getenv(key); value != "" {
				envMap[key] = value
			}
		}
	}
	for key, value := range profile.SetEnv {
		if _, exists := envMap[key]; !exists {
			envMap[key] = value
		}
	}
	if home != "" {
		envMap["HOME"] = home
		localBin := filepath.Join(home, ".local", "bin")
		if path := envMap["PATH"]; path != "" {
			envMap["PATH"] = localBin + string(os.PathListSeparator) + path
		} else {
			envMap["PATH"] = localBin + string(os.PathListSeparator) + "/usr/local/bin:/usr/bin:/bin"
		}
	}
	if envMap["TERM"] == "" {
		envMap["TERM"] = "xterm-256color"
	}
	envMap["GIT_TERMINAL_PROMPT"] = "0"
	if proxyPort > 0 {
		proxyURL := "http://localhost:" + strconv.Itoa(proxyPort)
		envMap["HTTPS_PROXY"] = proxyURL
		envMap["HTTP_PROXY"] = proxyURL
		envMap["NODE_USE_ENV_PROXY"] = "1"
	}
	if host, ok := providerHostFromEnv(); ok && isLoopbackHost(host) {
		envMap["NO_PROXY"] = appendEnvList(envMap["NO_PROXY"], host)
		envMap["no_proxy"] = appendEnvList(envMap["no_proxy"], host)
	}

	keys := make([]string, 0, len(envMap))
	for key := range envMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+envMap[key])
	}
	return env
}

func providerHostFromEnv() (string, bool) {
	raw := strings.TrimSpace(os.Getenv("WT_PROVIDER_BASE_URL"))
	if raw == "" {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false
	}
	return parsed.Hostname(), true
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func appendEnvList(current, value string) string {
	for _, entry := range strings.Split(current, ",") {
		if strings.TrimSpace(entry) == value {
			return current
		}
	}
	if current == "" {
		return value
	}
	return current + "," + value
}
