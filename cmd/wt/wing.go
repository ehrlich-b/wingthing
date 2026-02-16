package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/cipher"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/spf13/cobra"
)

// wingAttention tracks sessions that have triggered a terminal bell (need user attention).
var wingAttention sync.Map // sessionID → bool

// tunnelKeys caches derived AES-GCM keys per sender public key.
var tunnelKeys sync.Map // senderPub string → cipher.AEAD

// readEggOwner reads the creator user ID from an egg's owner file.
func readEggOwner(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "egg.owner"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readEggMeta reads agent/cwd from an egg's meta file.
func readEggMeta(dir string) (agent, cwd string) {
	data, err := os.ReadFile(filepath.Join(dir, "egg.meta"))
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "agent":
			agent = v
		case "cwd":
			cwd = v
		}
	}
	return agent, cwd
}

// hasBell returns true if data contains any BEL character (0x07).
// Does NOT try to distinguish OSC terminators from "real" bells — callers
// use a time-window heuristic instead (repeated BELs = real notification).
func hasBell(data []byte) bool {
	return bytes.IndexByte(data, 0x07) >= 0
}

func gzipData(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// sendReplayChunked splits replay data into chunks, compresses and encrypts each
// independently, and sends as multiple pty.output messages. Each chunk is a complete
// gzip stream so the browser can decompress them individually.
const replayChunkSize = 128 * 1024 // 128KB raw → compresses well under WS limit

func sendReplayChunked(sessionID string, raw []byte, gcm cipher.AEAD, write ws.PTYWriteFunc) {
	sent := 0
	chunks := 0
	totalCompressed := 0
	for sent < len(raw) {
		end := sent + replayChunkSize
		if end > len(raw) {
			end = len(raw)
		}
		chunk := raw[sent:end]
		compressed, gzErr := gzipData(chunk)
		if gzErr != nil {
			compressed = chunk
		}
		isCompressed := gzErr == nil
		encrypted, encErr := auth.Encrypt(gcm, compressed)
		if encErr != nil {
			log.Printf("pty session %s: replay chunk encrypt error: %v", sessionID, encErr)
			return
		}
		write(ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sessionID, Data: encrypted, Compressed: isCompressed})
		totalCompressed += len(compressed)
		sent = end
		chunks++
	}
	log.Printf("pty session %s: replayed %d bytes (gzip %d, %d chunks)", sessionID, len(raw), totalCompressed, chunks)
}

// discoverProjects scans dir for git repositories up to maxDepth levels deep.
// Returns group directories (sorted by project count) followed by individual repos (sorted by mtime).
func discoverProjects(dir string, maxDepth int) []ws.WingProject {
	var repos []ws.WingProject
	scanDir(dir, 0, maxDepth, &repos)

	// Count repos per parent directory
	parentCount := make(map[string]int)
	for _, r := range repos {
		parent := filepath.Dir(r.Path)
		if parent != dir { // skip the root scan dir itself
			parentCount[parent]++
		}
	}

	// Build group entries for parents with 2+ repos
	var groups []ws.WingProject
	seen := make(map[string]bool)
	for parent, count := range parentCount {
		if count >= 2 && !seen[parent] {
			seen[parent] = true
			groups = append(groups, ws.WingProject{
				Name:    filepath.Base(parent),
				Path:    parent,
				ModTime: int64(count), // abuse ModTime to carry count for sorting
			})
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].ModTime > groups[j].ModTime // most projects first
	})
	// Reset ModTime to actual value
	for i := range groups {
		groups[i].ModTime = projectModTime(groups[i].Path)
	}

	// Sort individual repos by mtime
	sort.Slice(repos, func(i, j int) bool {
		return repos[i].ModTime > repos[j].ModTime
	})

	return append(groups, repos...)
}

func projectModTime(dir string) int64 {
	info, err := os.Stat(dir)
	if err != nil {
		return 0
	}
	return info.ModTime().Unix()
}

func scanDir(dir string, depth, maxDepth int, projects *[]ws.WingProject) {
	if depth > maxDepth {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if e.Name() == ".git" {
			// Parent is a git repo
			*projects = append(*projects, ws.WingProject{
				Name:    filepath.Base(dir),
				Path:    dir,
				ModTime: projectModTime(dir),
			})
			return // don't recurse into .git
		}
		// Check if this child has a .git dir
		gitDir := filepath.Join(full, ".git")
		if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
			*projects = append(*projects, ws.WingProject{
				Name:    e.Name(),
				Path:    full,
				ModTime: projectModTime(full),
			})
			continue // don't recurse into known repos
		}
		scanDir(full, depth+1, maxDepth, projects)
	}
}

func wingPidPath() string {
	cfg, _ := config.Load()
	if cfg != nil {
		return filepath.Join(cfg.Dir, "wing.pid")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wingthing", "wing.pid")
}

const maxLogSize = 1 << 20 // 1MB

// rotateLog rotates path when it exceeds maxLogSize.
// Chain: .log -> .log.1 -> .log.2.gz -> deleted
func rotateLog(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < maxLogSize {
		return
	}

	// Delete oldest (.log.2.gz)
	os.Remove(path + ".2.gz")

	// Compress .log.1 -> .log.2.gz
	if data, err := os.ReadFile(path + ".1"); err == nil {
		if gz, err := os.Create(path + ".2.gz"); err == nil {
			w := gzip.NewWriter(gz)
			w.Write(data)
			w.Close()
			gz.Close()
			os.Remove(path + ".1")
		}
	}

	// Rotate current -> .log.1
	os.Rename(path, path+".1")
}

func wingArgsPath() string {
	cfg, _ := config.Load()
	if cfg != nil {
		return filepath.Join(cfg.Dir, "wing.args")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wingthing", "wing.args")
}

func wingLogPath() string {
	cfg, _ := config.Load()
	if cfg != nil {
		return filepath.Join(cfg.Dir, "wing.log")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wingthing", "wing.log")
}

func readPid() (int, error) {
	data, err := os.ReadFile(wingPidPath())
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	// Check if process is alive
	proc, err := os.FindProcess(pid)
	if err != nil {
		return 0, err
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		os.Remove(wingPidPath())
		return 0, fmt.Errorf("stale pid")
	}
	return pid, nil
}

func wingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wing",
		Short: "Manage your wing",
		Long:  "Your wing makes this machine reachable from anywhere via the roost. Use 'wt wing start' to go online.",
	}

	cmd.AddCommand(wingStartCmd())
	cmd.AddCommand(wingStopCmd())
	cmd.AddCommand(wingStatusCmd())
	cmd.AddCommand(wingAllowCmd())
	cmd.AddCommand(wingRevokeCmd())
	cmd.AddCommand(wingLockCmd())
	cmd.AddCommand(wingUnlockCmd())
	cmd.AddCommand(wingConfigCmd())

	return cmd
}

func wingStartCmd() *cobra.Command {
	var roostFlag string
	var labelsFlag string
	var convFlag string
	var foregroundFlag bool
	var debugFlag bool
	var eggConfigFlag string
	var orgFlag string
	var allowFlags []string
	var pathsFlag string
	var auditFlag bool
	var localFlag bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start wing daemon and go online",
		Long:  "Start a wing — your machine becomes reachable from anywhere via the roost. Runs as a background daemon by default. Use --foreground for debugging.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Foreground mode: run directly
			if foregroundFlag {
				return runWingForeground(cmd, roostFlag, labelsFlag, convFlag, eggConfigFlag, orgFlag, allowFlags, pathsFlag, debugFlag, auditFlag, localFlag)
			}

			// Daemon mode (default): re-exec detached, write PID file, return
			if pid, err := readPid(); err == nil {
				return fmt.Errorf("wing daemon already running (pid %d)", pid)
			}

			exe, err := os.Executable()
			if err != nil {
				return err
			}

			// Build args for foreground child
			var childArgs []string
			childArgs = append(childArgs, "wing", "start", "--foreground")
			if roostFlag != "" {
				childArgs = append(childArgs, "--roost", roostFlag)
			}
			if labelsFlag != "" {
				childArgs = append(childArgs, "--labels", labelsFlag)
			}
			if convFlag != "auto" {
				childArgs = append(childArgs, "--conv", convFlag)
			}
			if eggConfigFlag != "" {
				childArgs = append(childArgs, "--egg-config", eggConfigFlag)
			}
			if orgFlag != "" {
				childArgs = append(childArgs, "--org", orgFlag)
			}
			for _, ak := range allowFlags {
				childArgs = append(childArgs, "--allow", ak)
			}
			if pathsFlag != "" {
				childArgs = append(childArgs, "--paths", pathsFlag)
			}
			if debugFlag {
				childArgs = append(childArgs, "--debug")
			}
			if auditFlag {
				childArgs = append(childArgs, "--audit")
			}
			if localFlag {
				childArgs = append(childArgs, "--local")
			}

			rotateLog(wingLogPath())
			logFile, err := os.OpenFile(wingLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				return fmt.Errorf("open log: %w", err)
			}

			home, _ := os.UserHomeDir()

			child := exec.Command(exe, childArgs...)
			child.Dir = home
			child.Stdout = logFile
			child.Stderr = logFile
			child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

			if err := child.Start(); err != nil {
				logFile.Close()
				return fmt.Errorf("start daemon: %w", err)
			}
			logFile.Close()

			os.WriteFile(wingPidPath(), []byte(strconv.Itoa(child.Process.Pid)), 0644)
			os.WriteFile(wingArgsPath(), []byte(strings.Join(childArgs, "\n")), 0644)
			fmt.Printf("wing daemon started (pid %d)\n", child.Process.Pid)
			fmt.Printf("  log: %s\n", wingLogPath())
			fmt.Println()
			if localFlag {
				fmt.Println("open http://localhost:8080 to start a terminal")
			} else {
				fmt.Println("open https://app.wingthing.ai to start a terminal")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&roostFlag, "roost", "", "roost server URL (default: ws.wingthing.ai)")
	cmd.Flags().StringVar(&labelsFlag, "labels", "", "comma-separated wing labels (e.g. gpu,cuda,research)")
	cmd.Flags().StringVar(&convFlag, "conv", "auto", "conversation mode: auto (daily rolling), new (fresh), or a named thread")
	cmd.Flags().BoolVar(&foregroundFlag, "foreground", false, "run in foreground instead of daemonizing")
	cmd.Flags().BoolVar(&debugFlag, "debug", false, "dump raw PTY output to /tmp/wt-pty-<session>.bin for each egg")
	cmd.Flags().StringVar(&eggConfigFlag, "egg-config", "", "path to egg.yaml for wing-level sandbox defaults")
	cmd.Flags().StringVar(&orgFlag, "org", "", "org name or ID — share this wing with org members")
	cmd.Flags().StringSliceVar(&allowFlags, "allow", nil, "ephemeral passkey public key(s) for this session")
	cmd.Flags().StringVar(&pathsFlag, "paths", "", "comma-separated directories the wing can browse (default: ~/)")
	cmd.Flags().BoolVar(&auditFlag, "audit", false, "enable audit logging for all egg sessions")
	cmd.Flags().BoolVar(&localFlag, "local", false, "connect to localhost:8080 (for self-hosted wt serve)")

	return cmd
}

func runWingForeground(cmd *cobra.Command, roostFlag, labelsFlag, convFlag, eggConfigFlag, orgFlag string, allowFlags []string, pathsFlag string, debug, audit, local bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Load wing.yaml
	wingCfg, err := config.LoadWingConfig(cfg.Dir)
	if err != nil {
		log.Printf("wing: load wing.yaml: %v (continuing with defaults)", err)
		wingCfg = &config.WingConfig{}
	}

	// Merge wing.yaml with CLI flags (CLI extends yaml)
	if roostFlag == "" && wingCfg.Roost != "" {
		roostFlag = wingCfg.Roost
	}
	if orgFlag == "" && wingCfg.Org != "" {
		orgFlag = wingCfg.Org
	} else if orgFlag != "" && wingCfg.Org != "" && orgFlag != wingCfg.Org {
		return fmt.Errorf("org conflict: --org %q vs wing.yaml %q", orgFlag, wingCfg.Org)
	}
	// Merge paths: CLI extends yaml (same pattern as labels)
	var cliPaths []string
	if pathsFlag != "" {
		for _, p := range strings.Split(pathsFlag, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				cliPaths = append(cliPaths, p)
			}
		}
	}
	if len(cliPaths) == 0 && len(wingCfg.Paths) > 0 {
		cliPaths = wingCfg.Paths
	}
	if eggConfigFlag == "" && wingCfg.EggConfig != "" {
		eggConfigFlag = wingCfg.EggConfig
	}
	if convFlag == "auto" && wingCfg.Conv != "" {
		convFlag = wingCfg.Conv
	}
	if wingCfg.Audit {
		audit = true
	}
	if wingCfg.Debug {
		debug = true
	}
	if labelsFlag == "" && len(wingCfg.Labels) > 0 {
		labelsFlag = strings.Join(wingCfg.Labels, ",")
	}

	// Hot-reloadable flags — new sessions read .Load(), SIGHUP updates .Store()
	var auditLive atomic.Bool
	auditLive.Store(audit)
	var debugLive atomic.Bool
	debugLive.Store(debug)

	// Build allowed passkey keys: pinned (from wing.yaml) + ephemeral (from --allow)
	var allowedKeys []config.AllowKey
	allowedKeys = append(allowedKeys, wingCfg.AllowKeys...)
	pinnedCount := len(allowedKeys)
	for _, k := range allowFlags {
		k = strings.TrimSpace(k)
		if k != "" {
			allowedKeys = append(allowedKeys, config.AllowKey{Key: k})
		}
	}
	ephemeralCount := len(allowedKeys) - pinnedCount

	// Boot-scoped passkey auth cache — tokens live until wing process dies
	passkeyCache := auth.NewAuthCache()

	// Load wing-level egg config (with base chain resolution)
	var wingEggCfg *egg.EggConfig
	if eggConfigFlag != "" {
		wingEggCfg, err = egg.ResolveEggConfig(eggConfigFlag)
		if err != nil {
			return fmt.Errorf("load egg config: %w", err)
		}
		log.Printf("egg: loaded wing config from %s (network=%s)", eggConfigFlag, wingEggCfg.NetworkSummary())
	} else {
		// Check ~/.wingthing/egg.yaml
		defaultPath := filepath.Join(cfg.Dir, "egg.yaml")
		wingEggCfg, err = egg.ResolveEggConfig(defaultPath)
		if err != nil {
			wingEggCfg = egg.DefaultEggConfig()
			log.Printf("egg: using default config (network=%s)", wingEggCfg.NetworkSummary())
		} else {
			log.Printf("egg: loaded wing config from %s (network=%s)", defaultPath, wingEggCfg.NetworkSummary())
		}
	}
	var wingEggMu sync.Mutex

	// Resolve roost URL
	roostURL := roostFlag
	if local && roostURL == "" {
		roostURL = "http://localhost:8080"
	}
	if roostURL == "" {
		roostURL = cfg.RoostURL
	}
	if roostURL == "" {
		roostURL = "https://ws.wingthing.ai"
	}
	// Convert HTTP URL to WebSocket URL
	wsURL := strings.Replace(roostURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL = strings.TrimRight(wsURL, "/") + "/ws/wing"

	// Load auth token
	ts := auth.NewTokenStore(cfg.Dir)
	tok, err := ts.Load()
	if err != nil || !ts.IsValid(tok) {
		if local {
			return fmt.Errorf("no device token — run: wt serve --local")
		}
		return fmt.Errorf("not logged in — run: wt login")
	}

	// Detect available agents
	var agents []string
	for _, a := range []struct{ name, cmd string }{
		{"claude", "claude"},
		{"ollama", "ollama"},
		{"gemini", "gemini"},
		{"codex", "codex"},
		{"cursor", "agent"},
	} {
		if _, err := exec.LookPath(a.cmd); err == nil {
			agents = append(agents, a.name)
		}
	}

	// List installed skills
	var skills []string
	entries, _ := os.ReadDir(cfg.SkillsDir())
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			skills = append(skills, strings.TrimSuffix(e.Name(), ".md"))
		}
	}

	// Parse labels
	var labels []string
	if labelsFlag != "" {
		labels = strings.Split(labelsFlag, ",")
	}

	// Resolve paths to absolute
	home, _ := os.UserHomeDir()
	var resolvedPaths []string
	for _, p := range cliPaths {
		if strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, p[2:])
		} else if p == "~" {
			p = home
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		resolvedPaths = append(resolvedPaths, p)
	}
	if len(resolvedPaths) == 0 {
		resolvedPaths = []string{home}
	}

	// Scan for git projects in each path
	cwd, _ := os.Getwd()
	seen := make(map[string]bool)
	var projects []ws.WingProject
	for _, sp := range resolvedPaths {
		for _, p := range discoverProjects(sp, 3) {
			if !seen[p.Path] {
				seen[p.Path] = true
				projects = append(projects, p)
			}
		}
	}
	if cwd != "" {
		for _, p := range discoverProjects(cwd, 2) {
			if !seen[p.Path] {
				seen[p.Path] = true
				projects = append(projects, p)
			}
		}
	}

	fmt.Printf("connecting to %s\n", wsURL)
	fmt.Printf("  agents: %v\n", agents)
	fmt.Printf("  skills: %v\n", skills)
	if len(labels) > 0 {
		fmt.Printf("  labels: %v\n", labels)
	}
	fmt.Printf("  paths: %v\n", resolvedPaths)
	fmt.Printf("  projects: %d found\n", len(projects))
	for _, p := range projects {
		fmt.Printf("    %s → %s\n", p.Name, p.Path)
	}
	fmt.Printf("  conv: %s\n", convFlag)
	if len(allowedKeys) > 0 {
		fmt.Printf("  access control enabled: %d pinned + %d ephemeral keys\n", pinnedCount, ephemeralCount)
	}
	fmt.Println()
	if local || strings.Contains(roostURL, "localhost") {
		fmt.Println("open http://localhost:8080 to start a terminal")
	} else {
		fmt.Println("open https://app.wingthing.ai to start a terminal")
	}

	// Reap dead egg directories on startup
	reapDeadEggs(cfg)

	// Load wing private key for tunnel E2E encryption
	privKey, privKeyErr := auth.LoadPrivateKey(cfg.Dir)
	if privKeyErr != nil {
		return fmt.Errorf("load private key: %w", privKeyErr)
	}

	client := &ws.Client{
		RoostURL:    wsURL,
		Token:       tok.Token,
		WingID:      cfg.WingID,
		Hostname:    cfg.Hostname,
		Platform:    runtime.GOOS,
		Version:     version,
		Agents:      agents,
		Skills:      skills,
		Labels:      labels,
		Projects:    projects,
		OrgSlug:     orgFlag,
		RootDir:     resolvedPaths[0],
		Locked:       wingCfg.Locked,
		AllowedCount: len(wingCfg.AllowKeys),
	}

	client.OnPTY = func(ctx context.Context, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte) {
		// Block PTY when wing is locked with no authorized users
		if wingCfg.Locked && len(allowedKeys) == 0 {
			write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "wing is locked with no authorized users — add yourself with: wt wing allow --email <your-email>"})
			return
		}
		// Clamp CWD to paths if configured
		if len(resolvedPaths) > 0 {
			cleaned := filepath.Clean(start.CWD)
			underAny := false
			for _, rp := range resolvedPaths {
				if cleaned == rp || strings.HasPrefix(cleaned, rp+string(filepath.Separator)) {
					underAny = true
					break
				}
			}
			if !underAny {
				start.CWD = resolvedPaths[0]
			}
		}
		wingEggMu.Lock()
		currentEggCfg := wingEggCfg
		wingEggMu.Unlock()
		eggCfg := egg.DiscoverEggConfig(start.CWD, currentEggCfg)
		if auditLive.Load() {
			eggCfg.Audit = true
		}
		var authTTL time.Duration // default 0 = boot-scoped, no expiry
		if wingCfg.AuthTTL != "" {
			if d, err := time.ParseDuration(wingCfg.AuthTTL); err == nil {
				authTTL = d
			}
		}
		handlePTYSession(ctx, cfg, start, write, input, eggCfg, debugLive.Load(), allowedKeys, passkeyCache, authTTL)
	}

	client.OnTunnel = func(ctx context.Context, req ws.TunnelRequest, write ws.PTYWriteFunc) {
		handleTunnelRequest(ctx, cfg, wingCfg, req, write, &allowedKeys, passkeyCache, privKey, resolvedPaths, &wingEggMu, &wingEggCfg, auditLive.Load(), debugLive.Load(), client)
	}

	client.OnOrphanKill = func(ctx context.Context, sessionID string) {
		killOrphanEgg(cfg, sessionID)
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	// Reclaim surviving egg sessions on every (re)connect
	client.OnReconnect = func(rctx context.Context) {
		var authTTL time.Duration // default 0 = boot-scoped, no expiry
		if wingCfg.AuthTTL != "" {
			if d, err := time.ParseDuration(wingCfg.AuthTTL); err == nil {
				authTTL = d
			}
		}
		reclaimEggSessions(rctx, cfg, client, allowedKeys, passkeyCache, authTTL)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		for sig := range sigCh {
			if sig == syscall.SIGHUP {
				log.Println("SIGHUP: reloading wing config")
				newCfg, err := config.LoadWingConfig(cfg.Dir)
				if err != nil {
					log.Printf("reload failed: %v", err)
					continue
				}
				wingCfg.Locked = newCfg.Locked
				wingCfg.AllowKeys = newCfg.AllowKeys
				allowedKeys = append([]config.AllowKey{}, newCfg.AllowKeys...)
				client.Locked = newCfg.Locked
				client.AllowedCount = len(newCfg.AllowKeys)

				// Hot-reload audit + debug (atomic, read at session start)
				auditLive.Store(newCfg.Audit)
				debugLive.Store(newCfg.Debug)

				// Hot-reload conv, auth_ttl
				wingCfg.Conv = newCfg.Conv
				wingCfg.AuthTTL = newCfg.AuthTTL

				// Hot-reload labels
				wingCfg.Labels = newCfg.Labels
				client.Labels = newCfg.Labels

				// Hot-reload paths
				wingCfg.Paths = newCfg.Paths
				if len(newCfg.Paths) > 0 {
					resolvedPaths = nil
					for _, p := range newCfg.Paths {
						if strings.HasPrefix(p, "~/") {
							p = filepath.Join(home, p[2:])
						} else if p == "~" {
							p = home
						}
						if abs, err := filepath.Abs(p); err == nil {
							p = abs
						}
						resolvedPaths = append(resolvedPaths, p)
					}
				} else {
					resolvedPaths = []string{home}
				}
				client.RootDir = resolvedPaths[0]

				// Hot-reload egg config (if path changed)
				oldEggConfig := wingCfg.EggConfig
				wingCfg.EggConfig = newCfg.EggConfig
				if newCfg.EggConfig != oldEggConfig {
					eggPath := newCfg.EggConfig
					if eggPath == "" {
						eggPath = filepath.Join(cfg.Dir, "egg.yaml")
					}
					if newEggCfg, eggErr := egg.ResolveEggConfig(eggPath); eggErr == nil {
						wingEggMu.Lock()
						wingEggCfg = newEggCfg
						wingEggMu.Unlock()
						log.Printf("egg config reloaded from %s", eggPath)
					}
				}

				client.SendConfig(ctx)
				log.Printf("config reloaded: locked=%v allowed=%d audit=%v debug=%v", newCfg.Locked, len(newCfg.AllowKeys), newCfg.Audit, newCfg.Debug)
				continue
			}
			log.Println("wing shutting down...")
			cancel()
			return
		}
	}()

	return client.Run(ctx)
}

func wingStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the wing daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := readPid()
			if err != nil {
				return fmt.Errorf("no wing daemon running")
			}
			proc, _ := os.FindProcess(pid)
			if err := proc.Signal(syscall.SIGTERM); err != nil {
				return fmt.Errorf("kill pid %d: %w", pid, err)
			}
			os.Remove(wingPidPath())
			os.Remove(wingArgsPath())
			fmt.Printf("wing daemon stopped (pid %d)\n", pid)
			return nil
		},
	}
}

func wingStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check wing daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := readPid()
			if err != nil {
				fmt.Println("wing daemon is not running")
				return nil
			}
			fmt.Printf("wing daemon is running (pid %d)\n", pid)
			fmt.Printf("  log: %s\n", wingLogPath())

			// Show egg sessions from filesystem
			cfg, _ := config.Load()
			if cfg != nil {
				sessions := listAliveEggSessions(cfg)
				if len(sessions) > 0 {
					fmt.Println("  egg sessions:")
					for _, s := range sessions {
						fmt.Printf("    %s  %s  %s\n", s.SessionID, s.Agent, s.CWD)
					}
				} else {
					fmt.Println("  egg sessions: none")
				}
			}
			return nil
		},
	}
}

// resolveEmail calls the relay API to look up a user by email. Returns (userID, displayName, error).
func resolveEmail(cfg *config.Config, email string) (string, string, error) {
	roostURL := cfg.RoostURL
	if roostURL == "" {
		roostURL = "https://wingthing.ai"
	}
	ts := auth.NewTokenStore(cfg.Dir)
	tok, err := ts.Load()
	if err != nil || !ts.IsValid(tok) {
		return "", "", fmt.Errorf("not logged in — run: wt login")
	}
	req, _ := http.NewRequest("GET", strings.TrimRight(roostURL, "/")+"/api/app/resolve-email?email="+email, nil)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("resolve email: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("no user found with email: %s", email)
	}
	var result struct {
		UserID      string `json:"user_id"`
		DisplayName string `json:"display_name"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.UserID, result.DisplayName, nil
}

func wingAllowCmd() *cobra.Command {
	var userIDFlag string
	var emailFlag string
	var allFlag bool
	cmd := &cobra.Command{
		Use:   "allow [base64-public-key]",
		Short: "Allow a user or list allowlist (no args)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}

			// No args and no flags: list allowlist
			if len(args) == 0 && userIDFlag == "" && emailFlag == "" && !allFlag {
				if len(wingCfg.AllowKeys) == 0 {
					fmt.Println("no allowed users")
					return nil
				}
				for _, ak := range wingCfg.AllowKeys {
					display := ak.Email
					if display == "" {
						display = ak.UserID
					}
					if display == "" {
						display = "(key-only)"
					}
					keyInfo := ""
					if ak.Key != "" {
						prefix := ak.Key
						if len(prefix) > 16 {
							prefix = prefix[:16] + "..."
						}
						keyInfo = "  key:" + prefix
					}
					fmt.Printf("  %s%s\n", display, keyInfo)
				}
				return nil
			}

			// --all: fetch org members from relay and add all
			if allFlag {
				orgSlug := wingCfg.Org
				if orgSlug == "" {
					return fmt.Errorf("no org configured — set org in wing.yaml or use --org on wt wing")
				}
				roostURL := cfg.RoostURL
				if roostURL == "" {
					roostURL = "https://wingthing.ai"
				}
				ts := auth.NewTokenStore(cfg.Dir)
				tok, err := ts.Load()
				if err != nil || !ts.IsValid(tok) {
					return fmt.Errorf("not logged in — run: wt login")
				}
				base := strings.TrimRight(roostURL, "/")

				// Resolve org slug to ID via GET /api/orgs
				orgsReq, _ := http.NewRequest("GET", base+"/api/orgs", nil)
				orgsReq.Header.Set("Authorization", "Bearer "+tok.Token)
				orgsResp, err := http.DefaultClient.Do(orgsReq)
				if err != nil {
					return fmt.Errorf("fetch orgs: %w", err)
				}
				defer orgsResp.Body.Close()
				if orgsResp.StatusCode != 200 {
					return fmt.Errorf("fetch orgs: HTTP %d", orgsResp.StatusCode)
				}
				var orgs []struct {
					ID   string `json:"id"`
					Slug string `json:"slug"`
				}
				if err := json.NewDecoder(orgsResp.Body).Decode(&orgs); err != nil {
					return fmt.Errorf("parse orgs: %w", err)
				}
				var orgID string
				for _, o := range orgs {
					if o.Slug == orgSlug || o.ID == orgSlug {
						orgID = o.ID
						break
					}
				}
				if orgID == "" {
					return fmt.Errorf("org %q not found — check wing.yaml org setting", orgSlug)
				}

				// Fetch members via GET /api/orgs/{id}/members
				req, _ := http.NewRequest("GET", base+"/api/orgs/"+orgID+"/members", nil)
				req.Header.Set("Authorization", "Bearer "+tok.Token)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					return fmt.Errorf("fetch org members: %w", err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != 200 {
					return fmt.Errorf("fetch org members: HTTP %d", resp.StatusCode)
				}
				var membersResp struct {
					Members []struct {
						UserID          string `json:"user_id"`
						Email           string `json:"email"`
						DisplayName     string `json:"display_name"`
						PasskeyPubKey   string `json:"passkey_public_key"`
					} `json:"members"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&membersResp); err != nil {
					return fmt.Errorf("parse org members: %w", err)
				}
				members := membersResp.Members
				added := 0
				updated := 0
				skipped := 0
				for _, m := range members {
					// Skip members without a registered passkey
					if m.PasskeyPubKey == "" {
						fmt.Printf("skipped %s (no passkey)\n", m.Email)
						skipped++
						continue
					}
					// Deduplicate by user_id
					dupIdx := -1
					for i, ak := range wingCfg.AllowKeys {
						if ak.UserID == m.UserID {
							dupIdx = i
							break
						}
					}
					if dupIdx >= 0 {
						// Update passkey public key if we have one now and didn't before
						if wingCfg.AllowKeys[dupIdx].Key != m.PasskeyPubKey {
							wingCfg.AllowKeys[dupIdx].Key = m.PasskeyPubKey
							fmt.Printf("updated key: %s\n", m.Email)
							updated++
						} else {
							fmt.Printf("already allowed: %s\n", m.Email)
						}
						continue
					}
					wingCfg.AllowKeys = append(wingCfg.AllowKeys, config.AllowKey{Key: m.PasskeyPubKey, UserID: m.UserID, Email: m.Email})
					fmt.Printf("allowed %s\n", m.Email)
					added++
				}
				if added > 0 || updated > 0 {
					if !wingCfg.Locked {
						wingCfg.Locked = true
					}
					if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
						return err
					}
					signalDaemon(syscall.SIGHUP)
				}
				if skipped > 0 {
					fmt.Printf("skipped %d members without passkeys\n", skipped)
				}
				fmt.Printf("added %d members, updated %d keys\n", added, updated)
				return nil
			}

			var keyB64 string
			if len(args) > 0 {
				keyB64 = args[0]
				raw, err := base64.StdEncoding.DecodeString(keyB64)
				if err != nil {
					return fmt.Errorf("invalid base64: %w", err)
				}
				if len(raw) != 64 {
					return fmt.Errorf("invalid key: expected 64 bytes (P-256 X||Y), got %d", len(raw))
				}
				if !auth.IsValidP256Point(raw) {
					return fmt.Errorf("invalid key: not a valid P-256 curve point")
				}
			}

			// Resolve email to user ID
			var resolvedEmail string
			if emailFlag != "" {
				uid, _, resolveErr := resolveEmail(cfg, emailFlag)
				if resolveErr != nil {
					return resolveErr
				}
				userIDFlag = uid
				resolvedEmail = emailFlag
			}

			if keyB64 == "" && userIDFlag == "" {
				return fmt.Errorf("must provide --email, --user-id, or a public key")
			}

			// Deduplicate by key or user_id
			for _, ak := range wingCfg.AllowKeys {
				if keyB64 != "" && ak.Key == keyB64 {
					display := ak.Email
					if display == "" {
						display = ak.UserID
					}
					fmt.Printf("already allowed: %s\n", display)
					return nil
				}
				if userIDFlag != "" && ak.UserID == userIDFlag {
					display := ak.Email
					if display == "" {
						display = ak.UserID
					}
					fmt.Printf("already allowed: %s\n", display)
					return nil
				}
			}

			wingCfg.AllowKeys = append(wingCfg.AllowKeys, config.AllowKey{Key: keyB64, UserID: userIDFlag, Email: resolvedEmail})
			if !wingCfg.Locked {
				wingCfg.Locked = true
			}
			if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
				return err
			}
			display := resolvedEmail
			if display == "" {
				display = userIDFlag
			}
			if display == "" {
				display = keyB64[:12] + "..."
			}
			fmt.Printf("allowed %s\n", display)
			signalDaemon(syscall.SIGHUP)
			return nil
		},
	}
	cmd.Flags().StringVar(&userIDFlag, "user-id", "", "relay user ID to allow")
	cmd.Flags().StringVar(&emailFlag, "email", "", "user email to allow (resolves via relay)")
	cmd.Flags().BoolVar(&allFlag, "all", false, "allow all org members from relay")
	return cmd
}

func wingRevokeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revoke [user-id-or-email]",
		Short: "Remove from allowlist",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			revokeAll, _ := cmd.Flags().GetBool("all")

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}

			if revokeAll {
				count := len(wingCfg.AllowKeys)
				if count == 0 {
					fmt.Println("allowlist is already empty")
					return nil
				}
				wingCfg.AllowKeys = nil
				if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
					return err
				}
				fmt.Printf("revoked all %d entries\n", count)
				signalDaemon(syscall.SIGHUP)
				return nil
			}

			if len(args) == 0 {
				return fmt.Errorf("specify a user-id or email, or use --all")
			}
			query := args[0]

			// Find matches by user_id, email, or key prefix
			var matches []int
			for i, ak := range wingCfg.AllowKeys {
				if ak.UserID == query || ak.Email == query || strings.HasPrefix(ak.Key, query) {
					matches = append(matches, i)
				}
			}

			if len(matches) == 0 {
				return fmt.Errorf("no matching entry found for %q", query)
			}
			if len(matches) > 1 {
				fmt.Println("ambiguous match:")
				for _, i := range matches {
					ak := wingCfg.AllowKeys[i]
					display := ak.Email
					if display == "" {
						display = ak.UserID
					}
					if display == "" {
						display = "(key-only)"
					}
					fmt.Printf("  %s\n", display)
				}
				return fmt.Errorf("specify a more precise user_id or key prefix")
			}

			removed := wingCfg.AllowKeys[matches[0]]
			wingCfg.AllowKeys = append(wingCfg.AllowKeys[:matches[0]], wingCfg.AllowKeys[matches[0]+1:]...)
			if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
				return err
			}
			display := removed.Email
			if display == "" {
				display = removed.UserID
			}
			if display == "" {
				display = removed.Key[:12] + "..."
			}
			fmt.Printf("revoked: %s\n", display)
			signalDaemon(syscall.SIGHUP)
			return nil
		},
	}
	cmd.Flags().Bool("all", false, "Revoke all entries from the allowlist")
	return cmd
}

func signalDaemon(sig os.Signal) {
	pid, err := readPid()
	if err != nil {
		return
	}
	proc, _ := os.FindProcess(pid)
	proc.Signal(sig)
}

func wingLockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lock",
		Short: "Enable access control",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}
			if wingCfg.Locked {
				fmt.Println("wing is already locked")
				return nil
			}
			wingCfg.Locked = true
			if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
				return err
			}
			signalDaemon(syscall.SIGHUP)
			fmt.Println("wing locked")
			return nil
		},
	}
}

func wingUnlockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlock",
		Short: "Disable access control",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}
			if !wingCfg.Locked {
				fmt.Println("wing is already unlocked")
				return nil
			}
			wingCfg.Locked = false
			if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
				return err
			}
			signalDaemon(syscall.SIGHUP)
			fmt.Println("wing unlocked")
			return nil
		},
	}
}

func wingConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "View or set wing configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}

			daemonStatus := "(daemon stopped)"
			if _, err := readPid(); err == nil {
				daemonStatus = "(daemon running)"
			}

			fmt.Printf("wing_id:    %s\n", wingCfg.WingID)
			roost := wingCfg.Roost
			if roost == "" {
				roost = "wss://ws.wingthing.ai"
			}
			fmt.Printf("roost:      %s\n", roost)
			fmt.Printf("org:        %s\n", wingCfg.Org)
			fmt.Printf("paths:      %s\n", strings.Join(wingCfg.Paths, ", "))
			fmt.Printf("labels:     %s\n", strings.Join(wingCfg.Labels, ", "))
			fmt.Printf("egg_config: %s\n", wingCfg.EggConfig)
			fmt.Printf("conv:       %s\n", wingCfg.Conv)
			fmt.Printf("audit:      %v\n", wingCfg.Audit)
			fmt.Printf("debug:      %v\n", wingCfg.Debug)
			fmt.Printf("locked:     %v\n", wingCfg.Locked)
			authTTL := wingCfg.AuthTTL
			if authTTL == "" {
				authTTL = "0"
			}
			fmt.Printf("auth_ttl:   %s\n", authTTL)
			fmt.Printf("allow_keys: %d configured\n", len(wingCfg.AllowKeys))
			fmt.Println()
			fmt.Println(daemonStatus)
			return nil
		},
	}
	cmd.AddCommand(wingConfigSetCmd())
	return cmd
}

func wingConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set key=value [key=value ...]",
		Short: "Set wing configuration values",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}

			restartFields := map[string]bool{"org": true}
			immutableFields := map[string]bool{"wing_id": true, "roost": true, "allow_keys": true}

			var changedRestart []string

			for _, arg := range args {
				key, value, ok := strings.Cut(arg, "=")
				if !ok {
					return fmt.Errorf("invalid argument %q — use key=value format", arg)
				}
				key = strings.TrimSpace(key)
				value = strings.TrimSpace(value)

				if immutableFields[key] {
					return fmt.Errorf("%s cannot be changed via config set", key)
				}

				switch key {
				case "audit":
					b, err := strconv.ParseBool(value)
					if err != nil {
						return fmt.Errorf("audit: expected true or false")
					}
					wingCfg.Audit = b
				case "debug":
					b, err := strconv.ParseBool(value)
					if err != nil {
						return fmt.Errorf("debug: expected true or false")
					}
					wingCfg.Debug = b
				case "locked":
					b, err := strconv.ParseBool(value)
					if err != nil {
						return fmt.Errorf("locked: expected true or false")
					}
					wingCfg.Locked = b
				case "labels":
					var labels []string
					for _, l := range strings.Split(value, ",") {
						l = strings.TrimSpace(l)
						if l != "" {
							labels = append(labels, l)
						}
					}
					wingCfg.Labels = labels
				case "conv":
					wingCfg.Conv = value
				case "egg_config":
					if value != "" {
						if _, err := os.Stat(value); err != nil {
							return fmt.Errorf("egg_config: %s does not exist", value)
						}
					}
					wingCfg.EggConfig = value
				case "auth_ttl":
					if _, err := time.ParseDuration(value); err != nil {
						return fmt.Errorf("auth_ttl: invalid duration %q", value)
					}
					wingCfg.AuthTTL = value
				case "paths":
					var paths []string
					for _, p := range strings.Split(value, ",") {
						p = strings.TrimSpace(p)
						if p == "" {
							continue
						}
						info, err := os.Stat(p)
						if err != nil {
							return fmt.Errorf("paths: %s does not exist", p)
						}
						if !info.IsDir() {
							return fmt.Errorf("paths: %s is not a directory", p)
						}
						paths = append(paths, p)
					}
					wingCfg.Paths = paths
					wingCfg.Root = "" // clear legacy
				case "root":
					// compat alias: sets paths to single entry
					if value != "" {
						info, err := os.Stat(value)
						if err != nil {
							return fmt.Errorf("root: %s does not exist", value)
						}
						if !info.IsDir() {
							return fmt.Errorf("root: %s is not a directory", value)
						}
						wingCfg.Paths = []string{value}
					} else {
						wingCfg.Paths = nil
					}
					wingCfg.Root = "" // clear legacy
				case "org":
					wingCfg.Org = value
				default:
					return fmt.Errorf("unknown config key: %s", key)
				}

				if restartFields[key] {
					changedRestart = append(changedRestart, key)
				}
			}

			if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
				return err
			}

			signalDaemon(syscall.SIGHUP)

			for _, key := range changedRestart {
				fmt.Printf("%s: will take effect next restart\n", key)
			}
			return nil
		},
	}
}

// getDirEntries returns directory entries for the given path, suitable for cwd selection.
func getDirEntries(path string, resolvedPaths []string) []ws.DirEntry {
	if path == "" {
		home, _ := os.UserHomeDir()
		path = home
	}
	if strings.HasPrefix(path, "~") {
		home, _ := os.UserHomeDir()
		path = home + path[1:]
	}

	// Constrain to resolved paths if configured
	if len(resolvedPaths) > 0 {
		if path == "" {
			path = resolvedPaths[0]
		}
		abs := filepath.Clean(path)
		if a, err := filepath.Abs(abs); err == nil {
			abs = a
		}
		underAny := false
		for _, rp := range resolvedPaths {
			if abs == rp || strings.HasPrefix(abs, rp+string(filepath.Separator)) {
				underAny = true
				break
			}
		}
		if !underAny {
			path = resolvedPaths[0]
		}
	}

	// Try path as a directory first; if it doesn't exist, treat the last
	// component as a prefix filter on the parent (tab-completion behavior).
	prefix := ""
	entries, err := os.ReadDir(path)
	if err != nil {
		prefix = strings.ToLower(filepath.Base(path))
		path = filepath.Dir(path)
		entries, err = os.ReadDir(path)
		if err != nil {
			return nil
		}
	}

	var results []ws.DirEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue // dirs only -- this is for cwd selection
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue // skip hidden dirs
		}
		if prefix != "" && !strings.HasPrefix(strings.ToLower(e.Name()), prefix) {
			continue
		}
		full := filepath.Join(path, e.Name())
		results = append(results, ws.DirEntry{
			Name:  e.Name(),
			IsDir: true,
			Path:  full,
		})
	}
	return results
}

// reapDeadEggs removes egg directories for dead processes on startup.
func reapDeadEggs(cfg *config.Config) {
	eggsDir := filepath.Join(cfg.Dir, "eggs")
	entries, err := os.ReadDir(eggsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(eggsDir, e.Name())
		pidPath := filepath.Join(dir, "egg.pid")
		data, err := os.ReadFile(pidPath)
		if err != nil {
			// No pid file — stale dir, clean up
			cleanEggDir(dir)
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			cleanEggDir(dir)
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			cleanEggDir(dir)
			continue
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			// Dead process
			log.Printf("egg: reaping dead egg %s (pid %d)", e.Name(), pid)
			cleanEggDir(dir)
		}
	}
}

// cleanEggDir removes the files in an egg session directory, then the directory itself.
// If audit files exist, preserves egg.meta, egg.owner, and audit data (only removes runtime files).
func cleanEggDir(dir string) {
	os.Remove(filepath.Join(dir, "egg.sock"))
	os.Remove(filepath.Join(dir, "egg.token"))
	os.Remove(filepath.Join(dir, "egg.pid"))
	os.Remove(filepath.Join(dir, "egg.log"))
	// Keep egg.meta, egg.owner, and dir if audit recordings exist
	_, hasPty := os.Stat(filepath.Join(dir, "audit.pty.gz"))
	_, hasLog := os.Stat(filepath.Join(dir, "audit.log"))
	if hasPty == nil || hasLog == nil {
		return
	}
	os.Remove(filepath.Join(dir, "egg.meta"))
	os.Remove(filepath.Join(dir, "egg.owner"))
	os.Remove(dir)
}

// listAliveEggSessions scans ~/.wingthing/eggs/ for alive egg processes.
func listAliveEggSessions(cfg *config.Config) []ws.SessionInfo {
	eggsDir := filepath.Join(cfg.Dir, "eggs")
	entries, err := os.ReadDir(eggsDir)
	if err != nil {
		return nil
	}

	var out []ws.SessionInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionID := e.Name()
		dir := filepath.Join(eggsDir, sessionID)
		pidPath := filepath.Join(dir, "egg.pid")
		data, err := os.ReadFile(pidPath)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			cleanEggDir(dir)
			continue
		}

		// Alive — try to dial to confirm it's responsive
		sockPath := filepath.Join(dir, "egg.sock")
		tokenPath := filepath.Join(dir, "egg.token")
		ec, dialErr := egg.Dial(sockPath, tokenPath)
		if dialErr != nil {
			continue
		}
		ec.Close()

		agent, sessionCWD := readEggMeta(dir)
		info := ws.SessionInfo{
			SessionID: sessionID,
			Agent:     agent,
			CWD:       sessionCWD,
			UserID:    readEggOwner(dir),
		}
		if _, ok := wingAttention.Load(sessionID); ok {
			info.NeedsAttention = true
		}
		// Check if audit recording exists
		if _, err := os.Stat(filepath.Join(dir, "audit.pty.gz")); err == nil {
			info.Audit = true
		}
		out = append(out, info)
	}
	return out
}

// killOrphanEgg kills an egg session that has no active goroutine managing it.
// This handles the case where a pty.kill arrives but the session was never reclaimed.
func killOrphanEgg(cfg *config.Config, sessionID string) {
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)
	sockPath := filepath.Join(dir, "egg.sock")
	tokenPath := filepath.Join(dir, "egg.token")

	ec, err := egg.Dial(sockPath, tokenPath)
	if err != nil {
		// Can't reach egg — try to kill by PID
		pidPath := filepath.Join(dir, "egg.pid")
		data, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil {
				if proc, findErr := os.FindProcess(pid); findErr == nil {
					proc.Signal(syscall.SIGTERM)
				}
			}
		}
		cleanEggDir(dir)
		log.Printf("pty session %s: orphan killed (pid)", sessionID)
		return
	}
	ec.Kill(context.Background(), sessionID)
	ec.Close()
	cleanEggDir(dir)
	log.Printf("pty session %s: orphan killed (grpc)", sessionID)
}

// readEggCrashInfo reads the last lines of an egg's log looking for panic/crash info.
func readEggCrashInfo(dir string) string {
	logPath := filepath.Join(dir, "egg.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		return "egg process crashed (no log available)"
	}

	lines := strings.Split(string(data), "\n")

	// Find the last panic
	lastPanic := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "panic") || strings.Contains(lines[i], "PANIC") || strings.Contains(lines[i], "fatal error") {
			lastPanic = i
			break
		}
	}

	if lastPanic == -1 {
		return fmt.Sprintf("egg process crashed (check %s)", logPath)
	}

	// Extract up to 20 lines from the panic point
	end := lastPanic + 20
	if end > len(lines) {
		end = len(lines)
	}
	excerpt := strings.Join(lines[lastPanic:end], "\n")
	return fmt.Sprintf("egg crashed: %s", strings.TrimSpace(excerpt))
}

// reclaimEggSessions discovers surviving egg sessions and re-registers their
// input routing goroutines. The relay no longer tracks sessions — browser
// discovers them via E2E tunnel and reattaches directly via wing_id.
func reclaimEggSessions(ctx context.Context, cfg *config.Config, wsClient *ws.Client, allowedKeys []config.AllowKey, passkeyCache *auth.AuthCache, authTTL time.Duration) {
	// Small delay to let registration complete
	time.Sleep(500 * time.Millisecond)

	eggsDir := filepath.Join(cfg.Dir, "eggs")
	entries, err := os.ReadDir(eggsDir)
	if err != nil {
		return
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionID := e.Name()
		dir := filepath.Join(eggsDir, sessionID)
		pidPath := filepath.Join(dir, "egg.pid")
		data, err := os.ReadFile(pidPath)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			cleanEggDir(dir)
			continue
		}

		// If a goroutine is already handling this session (survived the
		// reconnect), skip — don't create a duplicate subscriber or
		// goroutine, which would cause decrypt errors.
		if wsClient.HasPTYSession(sessionID) {
			log.Printf("egg: session %s already tracked, skipping", sessionID)
			continue
		}

		agent, _ := readEggMeta(dir)

		// Alive — dial and set up input routing
		sockPath := filepath.Join(dir, "egg.sock")
		tokenPath := filepath.Join(dir, "egg.token")
		ec, dialErr := egg.Dial(sockPath, tokenPath)
		if dialErr != nil {
			log.Printf("egg: reclaim %s: dial failed: %v", sessionID, dialErr)
			continue
		}

		log.Printf("egg: reclaiming session %s (pid %d agent=%s)", sessionID, pid, agent)

		// Set up input routing for this session
		write, input, cleanup := wsClient.RegisterPTYSession(ctx, sessionID)
		go func(sid string, ec *egg.Client, dir string) {
			defer cleanup()
			defer ec.Close()
			handleReclaimedPTY(ctx, cfg, ec, sid, dir, write, input, allowedKeys, passkeyCache, authTTL)
		}(sessionID, ec, dir)
	}
}

// handleReclaimedPTY sets up I/O routing for a reclaimed (surviving) egg session.
func handleReclaimedPTY(ctx context.Context, cfg *config.Config, ec *egg.Client, sessionID, eggDir string, write ws.PTYWriteFunc, input <-chan []byte, allowedKeys []config.AllowKey, passkeyCache *auth.AuthCache, authTTL time.Duration) {
	var mu sync.Mutex
	var gcm cipher.AEAD
	var activeStream pb.Egg_SessionClient
	var cancelStream context.CancelFunc
	privKey, privKeyErr := auth.LoadPrivateKey(cfg.Dir)
	if privKeyErr != nil {
		log.Printf("pty session %s: FATAL: load private key: %v (reclaim aborted)", sessionID, privKeyErr)
		write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: sessionID, ExitCode: 1, Error: "E2E encryption required but wing private key missing"})
		return
	}
	wingPubKeyB64 := base64.StdEncoding.EncodeToString(privKey.PublicKey().Bytes())

	// Attach to existing egg session
	streamCtx, sCancel := context.WithCancel(ctx)
	stream, err := ec.AttachSession(streamCtx, sessionID)
	if err != nil {
		sCancel()
		log.Printf("pty session %s: reclaim attach failed: %v", sessionID, err)
		return
	}
	activeStream = stream
	cancelStream = sCancel

	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

	// Read output from egg -> encrypt -> send to relay
	go func() {
		var lastHadBell bool
		for {
			msg, err := stream.Recv()
			if err != nil {
				if err != io.EOF {
					log.Printf("pty session %s: egg stream error: %v", sessionID, err)
				}
				return
			}
			switch p := msg.Payload.(type) {
			case *pb.SessionMsg_Output:
				// Two consecutive chunks with BEL = real notification.
				// Single BEL is likely an OSC terminator; repeated means
				// the agent is persistently pinging for attention.
				if hasBell(p.Output) {
					if lastHadBell {
						wingAttention.Store(sessionID, true)
						write(ws.SessionAttention{Type: ws.TypeSessionAttention, SessionID: sessionID})
					}
					lastHadBell = true
				} else {
					lastHadBell = false
				}
				mu.Lock()
				currentGCM := gcm
				mu.Unlock()
				if currentGCM == nil {
					continue // no key yet or reattach in progress
				}
				encrypted, encErr := auth.Encrypt(currentGCM, p.Output)
				if encErr != nil {
					continue
				}
				write(ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sessionID, Data: encrypted})
			case *pb.SessionMsg_ExitCode:
				log.Printf("pty session %s: exited with code %d", sessionID, p.ExitCode)
				write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: sessionID, ExitCode: int(p.ExitCode)})
				wingAttention.Delete(sessionID)
				sessionCancel()
				return
			}
		}
	}()

	// Process input from browser
	go func() {
		for data := range input {
			var env ws.Envelope
			if err := json.Unmarshal(data, &env); err != nil {
				continue
			}
			switch env.Type {
			case ws.TypePTYAttach:
				var attach ws.PTYAttach
				if err := json.Unmarshal(data, &attach); err != nil {
					continue
				}
				wingAttention.Delete(sessionID)

				// Passkey auth gate — same check as handlePTYSession
				var attachAuthToken string
				if len(allowedKeys) > 0 {
					tokenOK := false
					if attach.AuthToken != "" {
						if _, ok := passkeyCache.Check(attach.AuthToken, authTTL); ok {
							tokenOK = true
							log.Printf("pty session %s: reattach passkey auth via cached token", sessionID)
						}
					}
					if !tokenOK {
						challenge, chalErr := auth.GenerateChallenge()
						if chalErr != nil {
							log.Printf("pty session %s: reattach challenge generation failed: %v", sessionID, chalErr)
							continue
						}
						write(ws.PasskeyChallenge{
							Type:      ws.TypePasskeyChallenge,
							SessionID: sessionID,
							Challenge: base64.RawURLEncoding.EncodeToString(challenge),
						})
						log.Printf("pty session %s: reattach passkey challenge sent", sessionID)

						timer := time.NewTimer(60 * time.Second)
						verified := false
						for !verified {
							select {
							case authData, ok := <-input:
								if !ok {
									timer.Stop()
									return
								}
								var authEnv ws.Envelope
								if err := json.Unmarshal(authData, &authEnv); err != nil {
									continue
								}
								if authEnv.Type != ws.TypePasskeyResponse {
									continue
								}
								var resp ws.PasskeyResponse
								if err := json.Unmarshal(authData, &resp); err != nil {
									continue
								}
								ad, _ := base64.StdEncoding.DecodeString(resp.AuthenticatorData)
								cj, _ := base64.StdEncoding.DecodeString(resp.ClientDataJSON)
								sig, _ := base64.StdEncoding.DecodeString(resp.Signature)
								var matched bool
								for _, ak := range allowedKeys {
									rawKey, decErr := base64.StdEncoding.DecodeString(ak.Key)
									if decErr != nil || len(rawKey) != 64 {
										continue
									}
									if err := auth.VerifyPasskeyAssertion(rawKey, challenge, ad, cj, sig); err == nil {
										matched = true
										token, tokErr := auth.GenerateAuthToken()
										if tokErr == nil {
											passkeyCache.Put(token, rawKey)
											attachAuthToken = token
										}
										log.Printf("pty session %s: reattach passkey verified", sessionID)
										break
									}
								}
								if !matched {
									write(ws.ErrorMsg{Type: ws.TypeError, Message: "invalid passkey"})
									continue
								}
								verified = true
							case <-timer.C:
								log.Printf("pty session %s: reattach passkey timed out", sessionID)
								write(ws.ErrorMsg{Type: ws.TypeError, Message: "passkey timed out"})
								timer.Stop()
								continue
							case <-ctx.Done():
								timer.Stop()
								return
							}
						}
						timer.Stop()
					}
				}

				// 1. Invalidate key — old output goroutine stops sending
				mu.Lock()
				gcm = nil
				if cancelStream != nil {
					cancelStream()
				}
				mu.Unlock()

				// 2. Derive new key
				var newGCM cipher.AEAD
				if attach.PublicKey != "" {
					derived, deriveErr := auth.DeriveSharedKey(privKey, attach.PublicKey, "wt-pty")
					if deriveErr != nil {
						log.Printf("pty session %s: reattach derive key failed: %v", sessionID, deriveErr)
					} else {
						newGCM = derived
						log.Printf("pty session %s: re-keyed E2E for reattach", sessionID)
					}
				}

				// 3. Send pty.started so browser can derive key
				{
					started := ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: sessionID, PublicKey: wingPubKeyB64}
					if attachAuthToken != "" {
						started.AuthToken = attachAuthToken
					}
					write(started)
				}

				// 4. New egg subscriber — replay first (atomic), then live frames
				newStreamCtx, newSCancel := context.WithCancel(ctx)
				newStream, reErr := ec.AttachSession(newStreamCtx, sessionID)
				if reErr != nil {
					newSCancel()
					log.Printf("pty session %s: reattach to egg failed: %v", sessionID, reErr)
					continue
				}

				// 5. Read replay (first message) and send to browser in chunks
				if newGCM != nil {
					replayMsg, rErr := newStream.Recv()
					if rErr == nil {
						if replay, ok := replayMsg.Payload.(*pb.SessionMsg_Output); ok && len(replay.Output) > 0 {
							sendReplayChunked(sessionID, replay.Output, newGCM, write)
						}
					}
				}

				// 6. Activate new key + stream, start new output goroutine
				mu.Lock()
				gcm = newGCM
				activeStream = newStream
				cancelStream = newSCancel
				mu.Unlock()

				go func() {
					var lastHadBell bool
					for {
						msg, err := newStream.Recv()
						if err != nil {
							if err != io.EOF {
								log.Printf("pty session %s: egg stream error: %v", sessionID, err)
							}
							return
						}
						switch p := msg.Payload.(type) {
						case *pb.SessionMsg_Output:
							if hasBell(p.Output) {
								if lastHadBell {
									wingAttention.Store(sessionID, true)
						write(ws.SessionAttention{Type: ws.TypeSessionAttention, SessionID: sessionID})
								}
								lastHadBell = true
							} else {
								lastHadBell = false
							}
							mu.Lock()
							currentGCM := gcm
							mu.Unlock()
							if currentGCM == nil {
								continue
							}
							encrypted, encErr := auth.Encrypt(currentGCM, p.Output)
							if encErr != nil {
								continue
							}
							write(ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sessionID, Data: encrypted})
						case *pb.SessionMsg_ExitCode:
							log.Printf("pty session %s: exited with code %d", sessionID, p.ExitCode)
							write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: sessionID, ExitCode: int(p.ExitCode)})
							wingAttention.Delete(sessionID)
							sessionCancel()
							return
						}
					}
				}()

			case ws.TypePTYInput:
				wingAttention.Delete(sessionID)
				var msg ws.PTYInput
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				mu.Lock()
				currentGCM := gcm
				currentStream := activeStream
				mu.Unlock()
				if currentGCM == nil || currentStream == nil {
					log.Printf("pty session %s: rejecting input — E2E not established", sessionID)
					continue
				}
				decoded, decErr := auth.Decrypt(currentGCM, msg.Data)
				if decErr != nil {
					continue
				}
				currentStream.Send(&pb.SessionMsg{SessionId: sessionID, Payload: &pb.SessionMsg_Input{Input: decoded}})

			case ws.TypePTYAttentionAck:
				wingAttention.Delete(sessionID)

			case ws.TypePTYResize:
				var msg ws.PTYResize
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				mu.Lock()
				currentStream := activeStream
				mu.Unlock()
				if currentStream != nil {
					currentStream.Send(&pb.SessionMsg{SessionId: sessionID, Payload: &pb.SessionMsg_Resize{Resize: &pb.Resize{Rows: uint32(msg.Rows), Cols: uint32(msg.Cols)}}})
				}

			case ws.TypePTYKill:
				log.Printf("pty session %s: kill received", sessionID)
				ec.Kill(ctx, sessionID)
				return
			}
		}
	}()

	<-sessionCtx.Done()
}

// handlePTYSession bridges a PTY session between a per-session egg and the relay.
// E2E encryption stays in the wing — the egg sees plaintext only.
func handlePTYSession(ctx context.Context, cfg *config.Config, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte, eggCfg *egg.EggConfig, debug bool, allowedKeys []config.AllowKey, passkeyCache *auth.AuthCache, authTTL time.Duration) {
	// Passkey verification — if allowed keys are configured, require auth
	if len(allowedKeys) > 0 {
		// Check cached auth token first
		if start.AuthToken != "" {
			if _, ok := passkeyCache.Check(start.AuthToken, authTTL); ok {
				log.Printf("pty session %s: passkey auth via cached token", start.SessionID)
				goto authDone
			}
		}

		// Need passkey credential ID to proceed
		if start.PasskeyCredentialID == "" {
			log.Printf("pty session %s: passkey required but no credential provided", start.SessionID)
			write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "passkey required"})
			return
		}

		// Check credential ID matches an allowed key
		credIDBytes, decErr := base64.RawURLEncoding.DecodeString(start.PasskeyCredentialID)
		if decErr != nil {
			write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "invalid credential ID"})
			return
		}
		_ = credIDBytes // credential ID is opaque — we verify by public key match during assertion

		// Generate and send challenge
		challenge, chalErr := auth.GenerateChallenge()
		if chalErr != nil {
			write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "challenge generation failed"})
			return
		}

		write(ws.PasskeyChallenge{
			Type:      ws.TypePasskeyChallenge,
			SessionID: start.SessionID,
			Challenge: base64.RawURLEncoding.EncodeToString(challenge),
		})
		log.Printf("pty session %s: passkey challenge sent, waiting for response", start.SessionID)

		// Wait for passkey response on input channel (60s timeout)
		timer := time.NewTimer(60 * time.Second)
		defer timer.Stop()
		var passkeyVerified bool
		for !passkeyVerified {
			select {
			case data, ok := <-input:
				if !ok {
					return
				}
				var env ws.Envelope
				if err := json.Unmarshal(data, &env); err != nil {
					continue
				}
				if env.Type != ws.TypePasskeyResponse {
					continue // ignore non-passkey messages during auth
				}
				var resp ws.PasskeyResponse
				if err := json.Unmarshal(data, &resp); err != nil {
					write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "invalid passkey response"})
					return
				}

				// Decode assertion fields
				authData, _ := base64.StdEncoding.DecodeString(resp.AuthenticatorData)
				clientJSON, _ := base64.StdEncoding.DecodeString(resp.ClientDataJSON)
				sig, _ := base64.StdEncoding.DecodeString(resp.Signature)

				// Try each allowed key
				var matched bool
				for _, ak := range allowedKeys {
					rawKey, decErr := base64.StdEncoding.DecodeString(ak.Key)
					if decErr != nil || len(rawKey) != 64 {
						continue
					}
					if err := auth.VerifyPasskeyAssertion(rawKey, challenge, authData, clientJSON, sig); err == nil {
						matched = true
						display := ak.Email
						if display == "" {
							display = ak.UserID
						}
						if display == "" {
							display = ak.Key[:8] + "..."
						}
						log.Printf("pty session %s: passkey verified (%s)", start.SessionID, display)

						// Issue auth token for subsequent sessions
						token, tokErr := auth.GenerateAuthToken()
						if tokErr == nil {
							passkeyCache.Put(token, rawKey)
							start.AuthToken = token // will be included in PTYStarted
						}
						break
					}
				}
				if !matched {
					write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "invalid passkey signature"})
					return
				}
				passkeyVerified = true

			case <-timer.C:
				write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "passkey authentication timed out"})
				return

			case <-ctx.Done():
				return
			}
		}
	}
authDone:

	// Set up E2E encryption — required, no plaintext fallback
	var mu sync.Mutex
	var gcm cipher.AEAD
	var activeStream pb.Egg_SessionClient
	var cancelStream context.CancelFunc
	var wingPubKeyB64 string
	privKey, privKeyErr := auth.LoadPrivateKey(cfg.Dir)
	if privKeyErr != nil {
		log.Printf("pty session %s: FATAL: load private key: %v", start.SessionID, privKeyErr)
		write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "E2E encryption required but wing private key missing"})
		return
	}
	wingPubKeyB64 = base64.StdEncoding.EncodeToString(privKey.PublicKey().Bytes())
	if start.PublicKey != "" {
		derived, deriveErr := auth.DeriveSharedKey(privKey, start.PublicKey, "wt-pty")
		if deriveErr != nil {
			log.Printf("pty session %s: FATAL: derive shared key: %v", start.SessionID, deriveErr)
			write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "E2E key exchange failed"})
			return
		}
		gcm = derived
		log.Printf("pty session %s: E2E encryption enabled", start.SessionID)
	}

	// Spawn a per-session egg
	ec, err := spawnEgg(cfg, start.SessionID, start.Agent, eggCfg, uint32(start.Rows), uint32(start.Cols), start.CWD, debug)
	if err != nil {
		eggDir := filepath.Join(cfg.Dir, "eggs", start.SessionID)
		crashInfo := readEggCrashInfo(eggDir)
		log.Printf("pty session %s: spawn egg failed: %v", start.SessionID, err)
		write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: crashInfo})
		return
	}
	defer ec.Close()

	log.Printf("pty session %s: spawned (user=%s agent=%s)", start.SessionID, start.UserID, start.Agent)

	// Persist session creator
	if start.UserID != "" {
		ownerPath := filepath.Join(cfg.Dir, "eggs", start.SessionID, "egg.owner")
		os.WriteFile(ownerPath, []byte(start.UserID), 0644)
	}

	// Notify browser
	write(ws.PTYStarted{
		Type:      ws.TypePTYStarted,
		SessionID: start.SessionID,
		Agent:     start.Agent,
		PublicKey: wingPubKeyB64,
		CWD:       start.CWD,
		AuthToken: start.AuthToken,
	})

	// Attach to egg session stream
	streamCtx, sCancel := context.WithCancel(ctx)
	stream, err := ec.AttachSession(streamCtx, start.SessionID)
	if err != nil {
		sCancel()
		log.Printf("pty: egg attach failed: %v", err)
		write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1})
		return
	}
	activeStream = stream
	cancelStream = sCancel

	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

	// Read output from egg -> encrypt -> send to browser
	go func() {
		var lastHadBell bool
		for {
			msg, err := stream.Recv()
			if err != nil {
				if err != io.EOF {
					log.Printf("pty session %s: egg stream error: %v", start.SessionID, err)
				}
				return
			}

			switch p := msg.Payload.(type) {
			case *pb.SessionMsg_Output:
				if hasBell(p.Output) {
					if lastHadBell {
						wingAttention.Store(start.SessionID, true)
					write(ws.SessionAttention{Type: ws.TypeSessionAttention, SessionID: start.SessionID})
					}
					lastHadBell = true
				} else {
					lastHadBell = false
				}

				mu.Lock()
				currentGCM := gcm
				mu.Unlock()
				if currentGCM == nil {
					continue
				}
				encrypted, encErr := auth.Encrypt(currentGCM, p.Output)
				if encErr != nil {
					log.Printf("pty session %s: encrypt error: %v", start.SessionID, encErr)
					continue
				}
				write(ws.PTYOutput{
					Type:      ws.TypePTYOutput,
					SessionID: start.SessionID,
					Data:      encrypted,
				})

			case *pb.SessionMsg_ExitCode:
				log.Printf("pty session %s: exited with code %d", start.SessionID, p.ExitCode)
				write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: int(p.ExitCode)})
				wingAttention.Delete(start.SessionID)
				sessionCancel()
				return
			}
		}
	}()

	// Process input from browser -> decrypt -> send to egg
	go func() {
		for data := range input {
			var env ws.Envelope
			if err := json.Unmarshal(data, &env); err != nil {
				continue
			}
			switch env.Type {
			case ws.TypePTYAttach:
				var attach ws.PTYAttach
				if err := json.Unmarshal(data, &attach); err != nil {
					continue
				}
				wingAttention.Delete(start.SessionID)
				// 1. Invalidate key — old output goroutine stops sending
				mu.Lock()
				gcm = nil
				if cancelStream != nil {
					cancelStream()
				}
				mu.Unlock()

				// 2. Derive new key
				var newGCM cipher.AEAD
				if attach.PublicKey != "" {
					derived, deriveErr := auth.DeriveSharedKey(privKey, attach.PublicKey, "wt-pty")
					if deriveErr != nil {
						log.Printf("pty session %s: reattach derive key failed: %v", start.SessionID, deriveErr)
					} else {
						newGCM = derived
						log.Printf("pty session %s: re-keyed E2E for reattach", start.SessionID)
					}
				}

				// 3. Send pty.started so browser can derive key
				write(ws.PTYStarted{
					Type:      ws.TypePTYStarted,
					SessionID: start.SessionID,
					Agent:     start.Agent,
					PublicKey: wingPubKeyB64,
				})

				// 4. New egg subscriber — replay first (atomic), then live frames
				newStreamCtx, newSCancel := context.WithCancel(ctx)
				newStream, reErr := ec.AttachSession(newStreamCtx, start.SessionID)
				if reErr != nil {
					newSCancel()
					log.Printf("pty session %s: reattach to egg failed: %v", start.SessionID, reErr)
					continue
				}

				// 5. Read replay (first message) and send to browser in chunks
				if newGCM != nil {
					replayMsg, rErr := newStream.Recv()
					if rErr == nil {
						if replay, ok := replayMsg.Payload.(*pb.SessionMsg_Output); ok && len(replay.Output) > 0 {
							sendReplayChunked(start.SessionID, replay.Output, newGCM, write)
						}
					}
				}

				// 6. Activate new key + stream, start new output goroutine
				mu.Lock()
				gcm = newGCM
				activeStream = newStream
				cancelStream = newSCancel
				mu.Unlock()

				go func() {
					var lastHadBell bool
					for {
						msg, err := newStream.Recv()
						if err != nil {
							if err != io.EOF {
								log.Printf("pty session %s: egg stream error: %v", start.SessionID, err)
							}
							return
						}
						switch p := msg.Payload.(type) {
						case *pb.SessionMsg_Output:
							if hasBell(p.Output) {
								if lastHadBell {
									wingAttention.Store(start.SessionID, true)
					write(ws.SessionAttention{Type: ws.TypeSessionAttention, SessionID: start.SessionID})
								}
								lastHadBell = true
							} else {
								lastHadBell = false
							}
							mu.Lock()
							currentGCM := gcm
							mu.Unlock()
							if currentGCM == nil {
								continue
							}
							encrypted, encErr := auth.Encrypt(currentGCM, p.Output)
							if encErr != nil {
								continue
							}
							write(ws.PTYOutput{
								Type:      ws.TypePTYOutput,
								SessionID: start.SessionID,
								Data:      encrypted,
							})
						case *pb.SessionMsg_ExitCode:
							log.Printf("pty session %s: exited with code %d", start.SessionID, p.ExitCode)
							write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: int(p.ExitCode)})
							wingAttention.Delete(start.SessionID)
							sessionCancel()
							return
						}
					}
				}()

			case ws.TypePTYInput:
				wingAttention.Delete(start.SessionID)
				var msg ws.PTYInput
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				mu.Lock()
				currentGCM := gcm
				currentStream := activeStream
				mu.Unlock()
				if currentGCM == nil || currentStream == nil {
					log.Printf("pty session %s: rejecting input — E2E not established", start.SessionID)
					continue
				}
				decoded, decErr := auth.Decrypt(currentGCM, msg.Data)
				if decErr != nil {
					log.Printf("pty session %s: decrypt error: %v", start.SessionID, decErr)
					continue
				}
				currentStream.Send(&pb.SessionMsg{
					SessionId: start.SessionID,
					Payload:   &pb.SessionMsg_Input{Input: decoded},
				})

			case ws.TypePTYAttentionAck:
				wingAttention.Delete(start.SessionID)

			case ws.TypePTYResize:
				var msg ws.PTYResize
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				mu.Lock()
				currentStream := activeStream
				mu.Unlock()
				if currentStream != nil {
					currentStream.Send(&pb.SessionMsg{
						SessionId: start.SessionID,
						Payload: &pb.SessionMsg_Resize{Resize: &pb.Resize{
							Rows: uint32(msg.Rows),
							Cols: uint32(msg.Cols),
						}},
					})
				}

			case ws.TypePTYKill:
				log.Printf("pty session %s: kill received", start.SessionID)
				ec.Kill(ctx, start.SessionID)
				return
			}
		}
	}()

	// Wait for session to end
	<-sessionCtx.Done()
}

// tunnelInner is the decrypted JSON payload inside a tunnel request.
type tunnelInner struct {
	Type      string `json:"type"`
	Path      string `json:"path,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Kind      string `json:"kind,omitempty"`
	YAML      string `json:"yaml,omitempty"`
	Offset    int    `json:"offset,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	AuthToken string `json:"auth_token,omitempty"`
	Key         string `json:"key,omitempty"` // passkey public key for allow.add
	AllowUserID string `json:"allow_user_id,omitempty"` // target user_id for allow.remove

	// Passkey assertion fields (for type "passkey.auth")
	CredentialID      string `json:"credential_id,omitempty"`
	AuthenticatorData string `json:"authenticator_data,omitempty"`
	ClientDataJSON    string `json:"client_data_json,omitempty"`
	Signature         string `json:"signature,omitempty"`
}

// pastSessionInfo is the local version of PastSessionInfo for tunnel responses.
type pastSessionInfo struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	CWD       string `json:"cwd,omitempty"`
	StartedAt int64  `json:"started_at,omitempty"`
	Audit     bool   `json:"audit,omitempty"`
	UserID    string `json:"user_id,omitempty"`
}

// tunnelRespond encrypts a JSON response and sends it as a tunnel.res message.
func tunnelRespond(gcm cipher.AEAD, requestID string, result any, write ws.PTYWriteFunc) {
	data, _ := json.Marshal(result)
	encrypted, err := auth.Encrypt(gcm, data)
	if err != nil {
		return
	}
	write(ws.TunnelResponse{Type: ws.TypeTunnelResponse, RequestID: requestID, Payload: encrypted})
}

// tunnelStreamChunk encrypts a streaming chunk and sends it as a tunnel.stream message.
func tunnelStreamChunk(gcm cipher.AEAD, requestID string, chunk []byte, done bool, write ws.PTYWriteFunc) {
	encrypted, err := auth.Encrypt(gcm, chunk)
	if err != nil {
		return
	}
	write(ws.TunnelStream{Type: ws.TypeTunnelStream, RequestID: requestID, Payload: encrypted, Done: done})
}

// isMemberFiltered returns true if the tunnel request is from an org member (not owner/admin).
// Empty/unknown roles are treated as "member" (least privilege) when a user ID is present.
func isMemberFiltered(req ws.TunnelRequest) bool {
	if req.SenderUserID == "" {
		return false
	}
	role := req.SenderOrgRole
	return role == "member" || role == ""
}

// canSeeSession returns true if the request sender can view a session with the given owner.
func canSeeSession(req ws.TunnelRequest, sessionUserID string) bool {
	if !isMemberFiltered(req) {
		return true
	}
	return sessionUserID == "" || sessionUserID == req.SenderUserID
}

// handleTunnelRequest decrypts and dispatches an encrypted tunnel request from the browser.
func handleTunnelRequest(ctx context.Context, cfg *config.Config, wingCfg *config.WingConfig, req ws.TunnelRequest, write ws.PTYWriteFunc,
	allowedKeysPtr *[]config.AllowKey, passkeyCache *auth.AuthCache, privKey *ecdh.PrivateKey, resolvedPaths []string,
	wingEggMu *sync.Mutex, wingEggCfg **egg.EggConfig, audit, debug bool, client *ws.Client) {

	allowedKeys := *allowedKeysPtr

	// Derive or retrieve cached AES-GCM key for this sender
	var gcm cipher.AEAD
	if cached, ok := tunnelKeys.Load(req.SenderPub); ok {
		gcm = cached.(cipher.AEAD)
	} else {
		derived, err := auth.DeriveSharedKey(privKey, req.SenderPub, "wt-tunnel")
		if err != nil {
			log.Printf("tunnel: derive key failed: %v", err)
			return
		}
		gcm = derived
		tunnelKeys.Store(req.SenderPub, gcm)
	}

	// Decrypt the payload
	plaintext, err := auth.Decrypt(gcm, req.Payload)
	if err != nil {
		log.Printf("tunnel %s: decrypt failed: %v", req.RequestID, err)
		return
	}

	// Parse inner message
	var inner tunnelInner
	if err := json.Unmarshal(plaintext, &inner); err != nil {
		log.Printf("tunnel %s: bad inner JSON: %v", req.RequestID, err)
		return
	}

	// Two-state auth check for locked wings
	if wingCfg.Locked && inner.Type != "passkey.auth" && inner.Type != "allow.add" {
		// Step 1: Is sender in the allow list?
		inList := false
		if req.SenderUserID != "" {
			for _, ak := range allowedKeys {
				if ak.UserID != "" && ak.UserID == req.SenderUserID {
					inList = true
					break
				}
			}
		}

		if !inList {
			// Not in allow list at all — locked
			tunnelRespond(gcm, req.RequestID, map[string]any{
				"error": "not_allowed",
			}, write)
			return
		}

		// Step 2: In the list — check auth token
		var authTTL time.Duration // default 0 = boot-scoped, no expiry
		if wingCfg.AuthTTL != "" {
			if d, err := time.ParseDuration(wingCfg.AuthTTL); err == nil {
				authTTL = d
			}
		}
		authorized := false
		if inner.AuthToken != "" {
			if _, ok := passkeyCache.Check(inner.AuthToken, authTTL); ok {
				authorized = true
			}
		}

		if !authorized {
			// In list but not yet authenticated — passkey challenge
			tunnelRespond(gcm, req.RequestID, map[string]any{
				"error":    "passkey_required",
				"hostname": client.Hostname,
				"platform": client.Platform,
				"version":  version,
				"locked":   true,
			}, write)
			return
		}
	}

	log.Printf("tunnel %s: %s (user=%s role=%s)", req.RequestID, inner.Type, req.SenderUserID, req.SenderOrgRole)

	switch inner.Type {
	case "dir.list":
		entries := getDirEntries(inner.Path, resolvedPaths)
		tunnelRespond(gcm, req.RequestID, map[string]any{"entries": entries}, write)

	case "wing.info":
		tunnelRespond(gcm, req.RequestID, map[string]any{
			"hostname":     client.Hostname,
			"platform":     client.Platform,
			"version":      version,
			"agents":       client.Agents,
			"projects":     client.Projects,
			"locked":        wingCfg.Locked,
			"allowed_count": len(wingCfg.AllowKeys),
		}, write)

	case "sessions.list":
		sessions := listAliveEggSessions(cfg)
		if isMemberFiltered(req) {
			var filtered []ws.SessionInfo
			for _, s := range sessions {
				if canSeeSession(req, s.UserID) {
					filtered = append(filtered, s)
				}
			}
			sessions = filtered
		}
		tunnelRespond(gcm, req.RequestID, map[string]any{"sessions": sessions}, write)

	case "sessions.history":
		sessions, total := getSessionsHistory(cfg, inner.Offset, inner.Limit)
		if isMemberFiltered(req) {
			var filtered []pastSessionInfo
			for _, s := range sessions {
				if canSeeSession(req, s.UserID) {
					filtered = append(filtered, s)
				}
			}
			sessions = filtered
			total = len(filtered)
		}
		tunnelRespond(gcm, req.RequestID, map[string]any{"sessions": sessions, "total": total}, write)

	case "audit.request":
		if inner.SessionID != "" && isMemberFiltered(req) {
			owner := readEggOwner(filepath.Join(cfg.Dir, "eggs", inner.SessionID))
			if !canSeeSession(req, owner) {
				log.Printf("tunnel %s: denied audit (user=%s session_owner=%s)", req.RequestID, req.SenderUserID, owner)
				tunnelRespond(gcm, req.RequestID, map[string]string{"error": "access denied"}, write)
				return
			}
		}
		streamAuditData(cfg, inner.SessionID, inner.Kind, gcm, req.RequestID, write)

	case "egg.config_update":
		if inner.YAML == "" {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing yaml"}, write)
			return
		}
		newCfg, err := egg.LoadEggConfigFromYAML(inner.YAML)
		if err != nil {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		wingEggMu.Lock()
		*wingEggCfg = newCfg
		wingEggMu.Unlock()
		log.Printf("egg: config updated from tunnel (network=%s)", newCfg.NetworkSummary())
		tunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "pty.kill":
		if inner.SessionID == "" {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing session_id"}, write)
			return
		}
		if isMemberFiltered(req) {
			owner := readEggOwner(filepath.Join(cfg.Dir, "eggs", inner.SessionID))
			if !canSeeSession(req, owner) {
				log.Printf("tunnel %s: denied kill (user=%s session_owner=%s)", req.RequestID, req.SenderUserID, owner)
				tunnelRespond(gcm, req.RequestID, map[string]string{"error": "access denied"}, write)
				return
			}
		}
		killOrphanEgg(cfg, inner.SessionID)
		tunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "wing.update":
		log.Println("tunnel: remote update requested")
		exe, exeErr := os.Executable()
		if exeErr != nil {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": exeErr.Error()}, write)
			return
		}
		c := exec.Command(exe, "update")
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		tunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "passkey.auth":
		if !wingCfg.Locked || len(allowedKeys) == 0 {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "wing is not locked"}, write)
			return
		}
		credID, _ := base64.RawURLEncoding.DecodeString(inner.CredentialID)
		authData, _ := base64.StdEncoding.DecodeString(inner.AuthenticatorData)
		cdJSON, _ := base64.StdEncoding.DecodeString(inner.ClientDataJSON)
		sig, _ := base64.StdEncoding.DecodeString(inner.Signature)

		// Extract challenge from clientDataJSON to find matching one
		var cd struct{ Challenge string `json:"challenge"` }
		json.Unmarshal(cdJSON, &cd)
		challenge, _ := base64.RawURLEncoding.DecodeString(cd.Challenge)

		// Try each allowed key
		var verified bool
		var matchedKey []byte
		for _, ak := range allowedKeys {
			keyBytes, err := base64.StdEncoding.DecodeString(ak.Key)
			if err != nil {
				continue
			}
			if err := auth.VerifyPasskeyAssertion(keyBytes, challenge, authData, cdJSON, sig); err == nil {
				verified = true
				matchedKey = keyBytes
				break
			}
		}
		_ = credID // credential_id used for client-side key lookup, not needed server-side
		if !verified {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "passkey verification failed"}, write)
			return
		}
		token, _ := auth.GenerateAuthToken()
		passkeyCache.Put(token, matchedKey)
		tunnelRespond(gcm, req.RequestID, map[string]string{"auth_token": token}, write)

	case "allow.list":
		type allowInfo struct {
			Key    string `json:"key"`
			UserID string `json:"user_id,omitempty"`
			Email  string `json:"email,omitempty"`
		}
		var allowed []allowInfo
		for _, ak := range allowedKeys {
			allowed = append(allowed, allowInfo{Key: ak.Key, UserID: ak.UserID, Email: ak.Email})
		}
		tunnelRespond(gcm, req.RequestID, map[string]any{"allowed": allowed}, write)

	case "allow.add":
		if req.SenderUserID == "" {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "no user identity"}, write)
			return
		}
		// Check duplicate by user_id
		for _, ak := range allowedKeys {
			if ak.UserID == req.SenderUserID {
				tunnelRespond(gcm, req.RequestID, map[string]string{"error": "already allowed"}, write)
				return
			}
		}
		// Validate key if provided
		if inner.Key != "" {
			keyBytes, decErr := base64.StdEncoding.DecodeString(inner.Key)
			if decErr != nil || len(keyBytes) != 65 {
				tunnelRespond(gcm, req.RequestID, map[string]string{"error": "invalid key"}, write)
				return
			}
		}
		newEntry := config.AllowKey{
			Key:    inner.Key,
			UserID: req.SenderUserID,
			Email:  req.SenderEmail,
		}
		wingCfg.AllowKeys = append(wingCfg.AllowKeys, newEntry)
		if !wingCfg.Locked {
			wingCfg.Locked = true
		}
		config.SaveWingConfig(cfg.Dir, wingCfg)
		allowedKeys = append(allowedKeys, newEntry)
		*allowedKeysPtr = allowedKeys
		client.Locked = wingCfg.Locked
		client.AllowedCount = len(wingCfg.AllowKeys)
		client.SendConfig(ctx)
		log.Printf("allowed: user=%s email=%s has_passkey=%v", req.SenderUserID, req.SenderEmail, inner.Key != "")
		tunnelRespond(gcm, req.RequestID, map[string]any{
			"ok": "true", "email": req.SenderEmail, "user_id": req.SenderUserID,
			"has_passkey": inner.Key != "",
		}, write)

	case "allow.remove":
		if req.SenderUserID == "" {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "no user identity"}, write)
			return
		}
		// Find entry to remove: by key or user_id
		target := inner.AllowUserID
		if target == "" && inner.Key != "" {
			for _, ak := range allowedKeys {
				if ak.Key == inner.Key {
					target = ak.UserID
					break
				}
			}
		}
		if target == "" {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing allow_user_id or key"}, write)
			return
		}
		// Only wing owner or the entry's own user can remove
		isOwner := req.SenderOrgRole == "owner" || req.SenderOrgRole == "admin"
		if !isOwner && req.SenderUserID != target {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "access denied"}, write)
			return
		}
		found := false
		for i, ak := range wingCfg.AllowKeys {
			if ak.UserID == target || (inner.Key != "" && ak.Key == inner.Key) {
				wingCfg.AllowKeys = append(wingCfg.AllowKeys[:i], wingCfg.AllowKeys[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "entry not found"}, write)
			return
		}
		config.SaveWingConfig(cfg.Dir, wingCfg)
		// Rebuild allowedKeys from config
		allowedKeys = append([]config.AllowKey{}, wingCfg.AllowKeys...)
		*allowedKeysPtr = allowedKeys
		client.Locked = wingCfg.Locked
		client.AllowedCount = len(wingCfg.AllowKeys)
		client.SendConfig(ctx)
		log.Printf("revoked: target=%s by=%s", target, req.SenderUserID)
		tunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	default:
		tunnelRespond(gcm, req.RequestID, map[string]string{"error": "unknown type: " + inner.Type}, write)
	}
}

// getSessionsHistory returns dead egg sessions from disk, paginated.
func getSessionsHistory(cfg *config.Config, offset, limit int) ([]pastSessionInfo, int) {
	eggsDir := filepath.Join(cfg.Dir, "eggs")
	entries, err := os.ReadDir(eggsDir)
	if err != nil {
		return nil, 0
	}

	var dead []pastSessionInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionID := e.Name()
		dir := filepath.Join(eggsDir, sessionID)

		// Check if process is alive -- skip alive sessions
		pidData, err := os.ReadFile(filepath.Join(dir, "egg.pid"))
		if err == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(pidData)))
			if pid > 0 {
				proc, _ := os.FindProcess(pid)
				if proc != nil && proc.Signal(syscall.Signal(0)) == nil {
					continue
				}
			}
		}

		agentName, cwd := readEggMeta(dir)
		hasAudit := false
		if _, err := os.Stat(filepath.Join(dir, "audit.pty.gz")); err == nil {
			hasAudit = true
		}
		if agentName == "" && !hasAudit {
			continue
		}
		if agentName == "" {
			agentName = "unknown"
		}

		info := pastSessionInfo{
			SessionID: sessionID,
			Agent:     agentName,
			CWD:       cwd,
			Audit:     hasAudit,
			UserID:    readEggOwner(dir),
		}
		if stat, err := os.Stat(dir); err == nil {
			info.StartedAt = stat.ModTime().Unix()
		}
		dead = append(dead, info)
	}

	sort.Slice(dead, func(i, j int) bool {
		return dead[i].StartedAt > dead[j].StartedAt
	})

	total := len(dead)
	if limit <= 0 {
		limit = 20
	}
	if offset > len(dead) {
		offset = len(dead)
	}
	end := offset + limit
	if end > len(dead) {
		end = len(dead)
	}
	return dead[offset:end], total
}

// streamAuditData reads audit data from disk and streams encrypted chunks via tunnel.stream.
func streamAuditData(cfg *config.Config, sessionID, kind string, gcm cipher.AEAD, requestID string, write ws.PTYWriteFunc) {
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)

	var filePath string
	switch kind {
	case "keylog":
		filePath = filepath.Join(dir, "audit.log")
	default:
		filePath = filepath.Join(dir, "audit.pty.gz")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		tunnelRespond(gcm, requestID, map[string]string{"error": "file not found: " + kind}, write)
		return
	}

	if kind != "pty" {
		// Keylog: stream text wrapped in JSON chunks
		text := string(data)
		const chunkSize = 32 * 1024
		for i := 0; i < len(text); i += chunkSize {
			end := i + chunkSize
			if end > len(text) {
				end = len(text)
			}
			chunk := map[string]string{"data": text[i:end]}
			chunkJSON, _ := json.Marshal(chunk)
			tunnelStreamChunk(gcm, requestID, chunkJSON, false, write)
		}
		tunnelStreamChunk(gcm, requestID, []byte(`{"done":true}`), true, write)
		return
	}

	// Decompress gzip and stream as asciinema v2 NDJSON
	gr, gzErr := gzip.NewReader(bytes.NewReader(data))
	if gzErr != nil {
		tunnelRespond(gcm, requestID, map[string]string{"error": "decompress: " + gzErr.Error()}, write)
		return
	}
	raw, readErr := io.ReadAll(gr)
	gr.Close()
	if readErr != nil {
		tunnelRespond(gcm, requestID, map[string]string{"error": "read: " + readErr.Error()}, write)
		return
	}

	// Read terminal dimensions from egg.meta
	cols, rows := 120, 40
	if meta, metaErr := os.ReadFile(filepath.Join(dir, "egg.meta")); metaErr == nil {
		for _, line := range strings.Split(string(meta), "\n") {
			if strings.HasPrefix(line, "cols=") {
				if v, pErr := strconv.Atoi(strings.TrimPrefix(line, "cols=")); pErr == nil && v > 0 {
					cols = v
				}
			}
			if strings.HasPrefix(line, "rows=") {
				if v, pErr := strconv.Atoi(strings.TrimPrefix(line, "rows=")); pErr == nil && v > 0 {
					rows = v
				}
			}
		}
	}

	// Convert varint format to asciinema v2 NDJSON
	isV2 := len(raw) >= 4 && string(raw[:4]) == "WTA2"
	pos := 0
	if isV2 {
		pos = 4
		if v, n := readVarint(raw[pos:]); n > 0 {
			cols = int(v)
			pos += n
		}
		if v, n := readVarint(raw[pos:]); n > 0 {
			rows = int(v)
			pos += n
		}
	}
	var cumulativeMs int64
	var ndjson strings.Builder
	fmt.Fprintf(&ndjson, `{"version":2,"width":%d,"height":%d}`, cols, rows)
	ndjson.WriteByte('\n')
	for pos < len(raw) {
		deltaMs, n := readVarint(raw[pos:])
		if n <= 0 {
			break
		}
		pos += n

		var frameType int64
		if isV2 {
			frameType, n = readVarint(raw[pos:])
			if n <= 0 {
				break
			}
			pos += n
		}

		dataLen, n := readVarint(raw[pos:])
		if n <= 0 {
			break
		}
		pos += n
		if pos+int(dataLen) > len(raw) {
			break
		}
		chunk := raw[pos : pos+int(dataLen)]
		pos += int(dataLen)
		cumulativeMs += deltaMs

		if frameType == 1 {
			rCols, cn := readVarint(chunk)
			if cn <= 0 {
				continue
			}
			rRows, rn := readVarint(chunk[cn:])
			if rn <= 0 {
				continue
			}
			fmt.Fprintf(&ndjson, "[%.3f,\"r\",\"%dx%d\"]\n", float64(cumulativeMs)/1000.0, rCols, rRows)
		} else {
			escaped := base64.StdEncoding.EncodeToString(chunk)
			fmt.Fprintf(&ndjson, "[%.3f,\"o\",\"%s\"]\n", float64(cumulativeMs)/1000.0, escaped)
		}
	}

	// Stream NDJSON lines as JSON-wrapped chunks
	text := ndjson.String()
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Parse each NDJSON line and send as a chunk the browser can JSON.parse
		tunnelStreamChunk(gcm, requestID, []byte(line), false, write)
	}
	tunnelStreamChunk(gcm, requestID, []byte(`{"done":true}`), true, write)
}

// readVarint reads a varint from buf, returns (value, bytes consumed).
func readVarint(buf []byte) (int64, int) {
	var x int64
	var s uint
	for i, b := range buf {
		if i >= 10 {
			return 0, 0
		}
		if b < 0x80 {
			return x | int64(b)<<s, i + 1
		}
		x |= int64(b&0x7f) << s
		s += 7
	}
	return 0, 0
}
