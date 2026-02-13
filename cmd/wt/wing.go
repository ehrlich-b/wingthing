package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/memory"
	"github.com/ehrlich-b/wingthing/internal/orchestrator"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/skill"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/spf13/cobra"
)

// wingAttention tracks sessions that have triggered a terminal bell (need user attention).
var wingAttention sync.Map // sessionID → bool

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

	return cmd
}

func wingStartCmd() *cobra.Command {
	var roostFlag string
	var labelsFlag string
	var convFlag string
	var foregroundFlag bool
	var debugFlag bool
	var eggConfigFlag string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start wing daemon and go online",
		Long:  "Start a wing — your machine becomes reachable from anywhere via the roost. Runs as a background daemon by default. Use --foreground for debugging.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Foreground mode: run directly
			if foregroundFlag {
				return runWingForeground(cmd, roostFlag, labelsFlag, convFlag, eggConfigFlag, debugFlag)
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
			if debugFlag {
				childArgs = append(childArgs, "--debug")
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
			fmt.Printf("wing daemon started (pid %d)\n", child.Process.Pid)
			fmt.Printf("  log: %s\n", wingLogPath())
			fmt.Println()
			fmt.Println("open https://app.wingthing.ai to start a terminal")
			return nil
		},
	}

	cmd.Flags().StringVar(&roostFlag, "roost", "", "roost server URL (default: ws.wingthing.ai)")
	cmd.Flags().StringVar(&labelsFlag, "labels", "", "comma-separated wing labels (e.g. gpu,cuda,research)")
	cmd.Flags().StringVar(&convFlag, "conv", "auto", "conversation mode: auto (daily rolling), new (fresh), or a named thread")
	cmd.Flags().BoolVar(&foregroundFlag, "foreground", false, "run in foreground instead of daemonizing")
	cmd.Flags().BoolVar(&debugFlag, "debug", false, "dump raw PTY output to /tmp/wt-pty-<session>.bin for each egg")
	cmd.Flags().StringVar(&eggConfigFlag, "egg-config", "", "path to egg.yaml for wing-level sandbox defaults")

	return cmd
}

func runWingForeground(cmd *cobra.Command, roostFlag, labelsFlag, convFlag, eggConfigFlag string, debug bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Load wing-level egg config
	var wingEggCfg *egg.EggConfig
	if eggConfigFlag != "" {
		wingEggCfg, err = egg.LoadEggConfig(eggConfigFlag)
		if err != nil {
			return fmt.Errorf("load egg config: %w", err)
		}
		log.Printf("egg: loaded wing config from %s (network=%s)", eggConfigFlag, wingEggCfg.NetworkSummary())
	} else {
		// Check ~/.wingthing/egg.yaml
		defaultPath := filepath.Join(cfg.Dir, "egg.yaml")
		wingEggCfg, err = egg.LoadEggConfig(defaultPath)
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
		return fmt.Errorf("not logged in — run: wt login")
	}

	// Open local store for task execution
	s, err := store.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer s.Close()

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

	// Scan for git projects in home dir and CWD
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	projects := discoverProjects(home, 3)
	if cwd != home {
		// Merge CWD projects (may overlap, dedup by path)
		seen := make(map[string]bool, len(projects))
		for _, p := range projects {
			seen[p.Path] = true
		}
		for _, p := range discoverProjects(cwd, 2) {
			if !seen[p.Path] {
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
	fmt.Printf("  projects: %d found\n", len(projects))
	for _, p := range projects {
		fmt.Printf("    %s → %s\n", p.Name, p.Path)
	}
	fmt.Printf("  conv: %s\n", convFlag)
	fmt.Println()
	fmt.Println("open https://app.wingthing.ai to start a terminal")

	// Reap dead egg directories on startup
	reapDeadEggs(cfg)

	client := &ws.Client{
		RoostURL:  wsURL,
		Token:     tok.Token,
		MachineID: cfg.MachineID,
		Platform:  runtime.GOOS,
		Version:   version,
		Agents:    agents,
		Skills:    skills,
		Labels:    labels,
		Projects:  projects,
	}

	client.OnTask = func(ctx context.Context, task ws.TaskSubmit, send ws.ChunkSender) (string, error) {
		return executeRelayTask(ctx, cfg, s, task, send)
	}

	client.OnPTY = func(ctx context.Context, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte) {
		wingEggMu.Lock()
		currentEggCfg := wingEggCfg
		wingEggMu.Unlock()
		eggCfg := egg.DiscoverEggConfig(start.CWD, currentEggCfg)
		handlePTYSession(ctx, cfg, start, write, input, eggCfg, debug)
	}

	client.OnChatStart = func(ctx context.Context, start ws.ChatStart, write ws.PTYWriteFunc) {
		handleChatStart(ctx, s, start, write)
	}

	client.OnChatMessage = func(ctx context.Context, msg ws.ChatMessage, write ws.PTYWriteFunc) {
		handleChatMessage(ctx, cfg, s, msg, write)
	}

	client.OnChatDelete = func(ctx context.Context, del ws.ChatDelete, write ws.PTYWriteFunc) {
		handleChatDelete(ctx, s, del, write)
	}

	client.OnDirList = func(ctx context.Context, req ws.DirList, write ws.PTYWriteFunc) {
		handleDirList(ctx, req, write)
	}

	client.SessionLister = func(ctx context.Context) []ws.SessionInfo {
		return listAliveEggSessions(cfg)
	}

	client.OnEggConfigUpdate = func(ctx context.Context, yamlStr string) {
		newCfg, err := egg.LoadEggConfigFromYAML(yamlStr)
		if err != nil {
			log.Printf("egg: bad config update: %v", err)
			return
		}
		wingEggMu.Lock()
		wingEggCfg = newCfg
		wingEggMu.Unlock()
		log.Printf("egg: config updated from relay (network=%s)", newCfg.NetworkSummary())
	}

	client.OnOrphanKill = func(ctx context.Context, sessionID string) {
		killOrphanEgg(cfg, sessionID)
	}

	client.OnUpdate = func(ctx context.Context) {
		log.Println("remote update requested")
		exe, err := os.Executable()
		if err != nil {
			log.Printf("update: find executable: %v", err)
			return
		}
		c := exec.Command(exe, "update")
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			log.Printf("update: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	// Reclaim surviving egg sessions on every (re)connect
	client.OnReconnect = func(rctx context.Context) {
		reclaimEggSessions(rctx, cfg, client)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		log.Println("wing shutting down...")
		cancel()
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

// executeRelayTask runs a task received from the relay using the local agent + sandbox.
func executeRelayTask(ctx context.Context, cfg *config.Config, s *store.Store, task ws.TaskSubmit, send ws.ChunkSender) (string, error) {
	fmt.Printf("executing task %s", task.TaskID)
	if task.Skill != "" {
		fmt.Printf(" (skill: %s)", task.Skill)
	}
	fmt.Println()

	// Create a local task record
	t := &store.Task{
		ID:    task.TaskID,
		RunAt: timeNow(),
	}

	if task.Skill != "" {
		t.What = task.Skill
		t.Type = "skill"
		// Check skill exists and is enabled
		state, stErr := skill.LoadState(cfg.Dir)
		if stErr == nil && !state.IsEnabled(task.Skill) {
			return "", fmt.Errorf("skill %q is disabled", task.Skill)
		}
	} else {
		t.What = task.Prompt
		t.Type = "prompt"
	}
	if task.Agent != "" {
		t.Agent = task.Agent
	}

	s.CreateTask(t)
	s.UpdateTaskStatus(t.ID, "running")

	agents := map[string]agent.Agent{
		"claude": newAgent("claude"),
		"ollama": newAgent("ollama"),
		"gemini": newAgent("gemini"),
		"codex":  newAgent("codex"),
		"cursor": newAgent("cursor"),
	}
	mem := memory.New(cfg.MemoryDir())

	builder := &orchestrator.Builder{
		Store:  s,
		Memory: mem,
		Config: cfg,
		Agents: agents,
	}

	pr, err := builder.Build(ctx, t.ID)
	if err != nil {
		s.SetTaskError(t.ID, err.Error())
		return "", fmt.Errorf("build prompt: %w", err)
	}

	agentName := pr.Agent
	a := agents[agentName]

	var runOpts agent.RunOpts
	if t.Type == "skill" {
		runOpts.SystemPrompt = `CRITICAL: You are a non-interactive data processor executing a skill. The prompt is a strict specification. Output ONLY what it specifies, EXACTLY in the format it specifies. NO conversational text. NO explanations. NO questions. NO markdown formatting unless specified. NO preamble or commentary.`
		runOpts.ReplaceSystemPrompt = true
	}

	isolation := task.Isolation
	if isolation == "" {
		isolation = pr.Isolation
	}
	if isolation != "privileged" {
		var mounts []sandbox.Mount
		for _, m := range pr.Mounts {
			mounts = append(mounts, sandbox.Mount{Source: m, Target: m})
		}
		// Map isolation string to NetworkNeed for the task execution path
		netNeed := sandbox.NetworkNone
		level := sandbox.ParseLevel(isolation)
		if level >= sandbox.Network {
			netNeed = sandbox.NetworkFull
		}
		sb, sbErr := sandbox.New(sandbox.Config{
			Mounts:      mounts,
			NetworkNeed: netNeed,
		})
		if sbErr != nil {
			s.SetTaskError(t.ID, sbErr.Error())
			return "", fmt.Errorf("create sandbox: %w", sbErr)
		}
		defer sb.Destroy()
		runOpts.CmdFactory = func(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
			return sb.Exec(ctx, name, args)
		}
	}

	stream, err := a.Run(ctx, pr.Prompt, runOpts)
	if err != nil {
		s.SetTaskError(t.ID, err.Error())
		return "", fmt.Errorf("run agent: %w", err)
	}

	// Stream output back to relay
	for {
		chunk, ok := stream.Next()
		if !ok {
			break
		}
		fmt.Print(chunk.Text) // local echo
		send(task.TaskID, chunk.Text)
	}
	fmt.Println()

	if err := stream.Err(); err != nil {
		s.SetTaskError(t.ID, err.Error())
		return "", fmt.Errorf("agent error: %w", err)
	}

	output := stream.Text()
	s.SetTaskOutput(t.ID, output)
	s.UpdateTaskStatus(t.ID, "done")

	// Record in thread
	inputTok, outputTok := stream.Tokens()
	totalTok := inputTok + outputTok
	if totalTok > 0 {
		s.AppendThread(&store.ThreadEntry{
			TaskID:     &t.ID,
			MachineID:  cfg.MachineID,
			Agent:      &agentName,
			UserInput:  &t.What,
			Summary:    truncate(output, 200),
			TokensUsed: &totalTok,
		})
	}

	fmt.Printf("task %s done (%d tokens)\n", task.TaskID, totalTok)
	return output, nil
}

func handleDirList(_ context.Context, req ws.DirList, write ws.PTYWriteFunc) {
	path := req.Path
	if path == "" {
		home, _ := os.UserHomeDir()
		path = home
	}
	if strings.HasPrefix(path, "~") {
		home, _ := os.UserHomeDir()
		path = home + path[1:]
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
			write(ws.DirResults{Type: ws.TypeDirResults, RequestID: req.RequestID})
			return
		}
	}

	var results []ws.DirEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue // dirs only — this is for cwd selection
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
	write(ws.DirResults{Type: ws.TypeDirResults, RequestID: req.RequestID, Entries: results})
}

func timeNow() time.Time {
	return time.Now().UTC()
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
func cleanEggDir(dir string) {
	os.Remove(filepath.Join(dir, "egg.sock"))
	os.Remove(filepath.Join(dir, "egg.token"))
	os.Remove(filepath.Join(dir, "egg.pid"))
	os.Remove(filepath.Join(dir, "egg.meta"))
	os.Remove(filepath.Join(dir, "egg.log"))
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
		}
		if _, ok := wingAttention.Load(sessionID); ok {
			info.NeedsAttention = true
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

// reclaimEggSessions discovers surviving egg sessions and sends pty.reclaim to the relay.
func reclaimEggSessions(ctx context.Context, cfg *config.Config, wsClient *ws.Client) {
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

		// Read metadata from egg's meta file
		agent, cwd := readEggMeta(dir)

		// If a goroutine is already handling this session (survived the
		// reconnect), just tell the relay about it — don't create a
		// duplicate subscriber or goroutine, which would cause decrypt
		// errors (old goroutine encrypts with old key, browser has new key).
		if wsClient.HasPTYSession(sessionID) {
			log.Printf("egg: session %s already tracked, sending reclaim only", sessionID)
			wsClient.SendReclaim(ctx, sessionID, agent, cwd)
			continue
		}

		// Alive — dial and reclaim
		sockPath := filepath.Join(dir, "egg.sock")
		tokenPath := filepath.Join(dir, "egg.token")
		ec, dialErr := egg.Dial(sockPath, tokenPath)
		if dialErr != nil {
			log.Printf("egg: reclaim %s: dial failed: %v", sessionID, dialErr)
			continue
		}

		log.Printf("egg: reclaiming session %s (pid %d agent=%s)", sessionID, pid, agent)
		wsClient.SendReclaim(ctx, sessionID, agent, cwd)

		// Set up input routing for this session
		write, input, cleanup := wsClient.RegisterPTYSession(ctx, sessionID)
		go func(sid string, ec *egg.Client, dir string) {
			defer cleanup()
			defer ec.Close()
			handleReclaimedPTY(ctx, cfg, ec, sid, dir, write, input)
		}(sessionID, ec, dir)
	}
}

// handleReclaimedPTY sets up I/O routing for a reclaimed (surviving) egg session.
func handleReclaimedPTY(ctx context.Context, cfg *config.Config, ec *egg.Client, sessionID, eggDir string, write ws.PTYWriteFunc, input <-chan []byte) {
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
					derived, deriveErr := auth.DeriveSharedKey(privKey, attach.PublicKey)
					if deriveErr != nil {
						log.Printf("pty session %s: reattach derive key failed: %v", sessionID, deriveErr)
					} else {
						newGCM = derived
						log.Printf("pty session %s: re-keyed E2E for reattach", sessionID)
					}
				}

				// 3. Send pty.started so browser can derive key
				write(ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: sessionID, PublicKey: wingPubKeyB64})

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
func handlePTYSession(ctx context.Context, cfg *config.Config, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte, eggCfg *egg.EggConfig, debug bool) {
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
		derived, deriveErr := auth.DeriveSharedKey(privKey, start.PublicKey)
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

	log.Printf("pty session %s: egg spawned", start.SessionID)

	// Notify browser
	write(ws.PTYStarted{
		Type:      ws.TypePTYStarted,
		SessionID: start.SessionID,
		Agent:     start.Agent,
		PublicKey: wingPubKeyB64,
		CWD:       start.CWD,
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
					derived, deriveErr := auth.DeriveSharedKey(privKey, attach.PublicKey)
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

// handleChatStart creates a new chat session or resumes an existing one.
func handleChatStart(ctx context.Context, s *store.Store, start ws.ChatStart, write ws.PTYWriteFunc) {
	sessionID := start.SessionID

	// Resume: load history from local DB
	if sessionID != "" {
		existing, err := s.GetChatSession(sessionID)
		if err == nil && existing != nil {
			// Send history
			msgs, _ := s.ListChatMessages(sessionID)
			var entries []ws.ChatHistoryEntry
			for _, m := range msgs {
				entries = append(entries, ws.ChatHistoryEntry{Role: m.Role, Content: m.Content})
			}
			write(ws.ChatHistoryMsg{
				Type:      ws.TypeChatHistory,
				SessionID: sessionID,
				Messages:  entries,
			})
			write(ws.ChatStarted{
				Type:      ws.TypeChatStarted,
				SessionID: sessionID,
				Agent:     existing.Agent,
			})
			log.Printf("chat session %s resumed (%d messages)", sessionID, len(entries))
			return
		}
	}

	// New session
	if err := s.CreateChatSession(start.SessionID, start.Agent); err != nil {
		log.Printf("chat: create session: %v", err)
		return
	}

	write(ws.ChatStarted{
		Type:      ws.TypeChatStarted,
		SessionID: start.SessionID,
		Agent:     start.Agent,
	})
	log.Printf("chat session %s created (agent=%s)", start.SessionID, start.Agent)
}

// handleChatMessage processes a user message: stores it, calls the agent, streams the response.
func handleChatMessage(ctx context.Context, cfg *config.Config, s *store.Store, msg ws.ChatMessage, write ws.PTYWriteFunc) {
	// Store user message
	if err := s.AppendChatMessage(msg.SessionID, "user", msg.Content); err != nil {
		log.Printf("chat: store user message: %v", err)
		return
	}

	// Load conversation history
	messages, err := s.ListChatMessages(msg.SessionID)
	if err != nil {
		log.Printf("chat: load history: %v", err)
		return
	}

	// Build prompt with conversation history
	var promptBuilder strings.Builder
	if len(messages) > 1 {
		promptBuilder.WriteString("Previous conversation:\n\n")
		for _, m := range messages[:len(messages)-1] {
			if m.Role == "user" {
				promptBuilder.WriteString("User: " + m.Content + "\n\n")
			} else {
				promptBuilder.WriteString("Assistant: " + m.Content + "\n\n")
			}
		}
		promptBuilder.WriteString("Now respond to the user's latest message:\n\n")
	}
	promptBuilder.WriteString(msg.Content)

	// Resolve agent
	session, _ := s.GetChatSession(msg.SessionID)
	agentName := "claude"
	if session != nil && session.Agent != "" {
		agentName = session.Agent
	}

	a := newAgent(agentName)
	stream, err := a.Run(ctx, promptBuilder.String(), agent.RunOpts{})
	if err != nil {
		log.Printf("chat session %s: agent error: %v", msg.SessionID, err)
		write(ws.ChatDone{
			Type:      ws.TypeChatDone,
			SessionID: msg.SessionID,
			Content:   "Error: " + err.Error(),
		})
		return
	}

	// Stream chunks back
	for {
		chunk, ok := stream.Next()
		if !ok {
			break
		}
		write(ws.ChatChunk{
			Type:      ws.TypeChatChunk,
			SessionID: msg.SessionID,
			Text:      chunk.Text,
		})
	}

	fullResponse := stream.Text()

	// Store assistant response
	s.AppendChatMessage(msg.SessionID, "assistant", fullResponse)

	write(ws.ChatDone{
		Type:      ws.TypeChatDone,
		SessionID: msg.SessionID,
		Content:   fullResponse,
	})
	log.Printf("chat session %s: response complete (%d chars)", msg.SessionID, len(fullResponse))
}

// handleChatDelete removes a chat session from the local DB.
func handleChatDelete(ctx context.Context, s *store.Store, del ws.ChatDelete, write ws.PTYWriteFunc) {
	if err := s.DeleteChatSession(del.SessionID); err != nil {
		log.Printf("chat: delete session %s: %v", del.SessionID, err)
	}
	write(ws.ChatDeleted{
		Type:      ws.TypeChatDeleted,
		SessionID: del.SessionID,
	})
	log.Printf("chat session %s deleted", del.SessionID)
}
