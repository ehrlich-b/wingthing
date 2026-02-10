package main

import (
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

// discoverProjects scans dir for git repositories up to maxDepth levels deep.
func discoverProjects(dir string, maxDepth int) []ws.WingProject {
	var projects []ws.WingProject
	scanDir(dir, 0, maxDepth, &projects)
	return projects
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
				Name: filepath.Base(dir),
				Path: dir,
			})
			return // don't recurse into .git
		}
		// Check if this child has a .git dir
		gitDir := filepath.Join(full, ".git")
		if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
			*projects = append(*projects, ws.WingProject{
				Name: e.Name(),
				Path: full,
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
	var relayFlag string
	var labelsFlag string
	var convFlag string
	var daemonFlag bool

	cmd := &cobra.Command{
		Use:   "wing",
		Short: "Connect to relay and accept remote tasks",
		Long:  "Start a wing — your machine becomes reachable from anywhere via the relay. Same as sitting at the keyboard, just remote.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Daemonize: re-exec detached, write PID file, return
			if daemonFlag {
				if pid, err := readPid(); err == nil {
					return fmt.Errorf("wing daemon already running (pid %d)", pid)
				}

				exe, err := os.Executable()
				if err != nil {
					return err
				}

				// Build args without -d
				var childArgs []string
				childArgs = append(childArgs, "wing")
				if relayFlag != "" {
					childArgs = append(childArgs, "--relay", relayFlag)
				}
				if labelsFlag != "" {
					childArgs = append(childArgs, "--labels", labelsFlag)
				}
				if convFlag != "auto" {
					childArgs = append(childArgs, "--conv", convFlag)
				}

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
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			// Resolve relay URL
			relayURL := relayFlag
			if relayURL == "" {
				relayURL = cfg.RelayURL
			}
			if relayURL == "" {
				relayURL = "https://ws.wingthing.ai"
			}
			// Convert HTTP URL to WebSocket URL
			wsURL := strings.Replace(relayURL, "https://", "wss://", 1)
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

			// Scan for git projects in current directory
			cwd, _ := os.Getwd()
			projects := discoverProjects(cwd, 2)

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

			client := &ws.Client{
				RelayURL:  wsURL,
				Token:     tok.Token,
				MachineID: cfg.MachineID,
				Agents:    agents,
				Skills:    skills,
				Labels:    labels,
				Projects:  projects,
			}

			client.OnTask = func(ctx context.Context, task ws.TaskSubmit, send ws.ChunkSender) (string, error) {
				return executeRelayTask(ctx, cfg, s, task, send)
			}

			// Start or connect to the egg process
			eggClient, eggErr := ensureEgg(cfg)
			if eggErr != nil {
				log.Printf("egg: %v (PTY sessions will fail)", eggErr)
			} else {
				defer eggClient.Close()
			}

			client.OnPTY = func(ctx context.Context, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte) {
				if eggClient == nil {
					write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1})
					return
				}
				handlePTYSession(ctx, cfg, eggClient, start, write, input)
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

			client.OnUpdate = func(ctx context.Context) {
				log.Println("remote update requested")
				exe, err := os.Executable()
				if err != nil {
					log.Printf("update: find executable: %v", err)
					return
				}
				cmd := exec.Command(exe, "update")
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				if err := cmd.Run(); err != nil {
					log.Printf("update: %v", err)
				}
			}

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			// Reclaim surviving egg sessions after reconnect
			if eggClient != nil {
				go reclaimEggSessions(ctx, eggClient, client)
			}

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
			go func() {
				<-sigCh
				log.Println("wing shutting down...")
				cancel()
			}()

			return client.Run(ctx)
		},
	}

	cmd.Flags().StringVar(&relayFlag, "relay", "", "relay server URL (default: ws.wingthing.ai)")
	cmd.Flags().StringVar(&labelsFlag, "labels", "", "comma-separated wing labels (e.g. gpu,cuda,research)")
	cmd.Flags().StringVar(&convFlag, "conv", "auto", "conversation mode: auto (daily rolling), new (fresh), or a named thread")
	cmd.Flags().BoolVarP(&daemonFlag, "daemon", "d", false, "run as background daemon")

	cmd.AddCommand(wingStopCmd())
	cmd.AddCommand(wingStatusCmd())

	return cmd
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

			// Show egg status
			cfg, _ := config.Load()
			if cfg != nil {
				eggPid, eggErr := readEggPid()
				if eggErr != nil {
					fmt.Println("  egg: not running")
				} else {
					fmt.Printf("  egg: running (pid %d)\n", eggPid)
					// Try to list sessions
					ec, dialErr := egg.Dial(
						filepath.Join(cfg.Dir, "egg.sock"),
						filepath.Join(cfg.Dir, "egg.token"),
					)
					if dialErr == nil {
						defer ec.Close()
						resp, listErr := ec.List(cmd.Context())
						if listErr == nil && len(resp.Sessions) > 0 {
							fmt.Println("  sessions:")
							for _, s := range resp.Sessions {
								started, _ := time.Parse(time.RFC3339, s.StartedAt)
								ago := time.Since(started).Truncate(time.Second)
								fmt.Printf("    %s  %s  %s  %s ago\n", s.SessionId, s.Agent, s.Cwd, ago)
							}
						} else {
							fmt.Println("  sessions: none")
						}
					}
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
		sb, sbErr := sandbox.New(sandbox.Config{
			Isolation: sandbox.ParseLevel(isolation),
			Mounts:    mounts,
			Timeout:   pr.Timeout,
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
		if strings.HasPrefix(e.Name(), ".") {
			continue // skip hidden files
		}
		if prefix != "" && !strings.HasPrefix(strings.ToLower(e.Name()), prefix) {
			continue
		}
		full := filepath.Join(path, e.Name())
		results = append(results, ws.DirEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Path:  full,
		})
	}
	write(ws.DirResults{Type: ws.TypeDirResults, RequestID: req.RequestID, Entries: results})
}

func timeNow() time.Time {
	return time.Now().UTC()
}

// ensureEgg starts the egg process if not already running, returns a connected client.
func ensureEgg(cfg *config.Config) (*egg.Client, error) {
	sockPath := filepath.Join(cfg.Dir, "egg.sock")
	tokenPath := filepath.Join(cfg.Dir, "egg.token")

	// Try to connect to existing egg
	ec, err := egg.Dial(sockPath, tokenPath)
	if err == nil {
		// Verify it's alive with a Version call
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		eggVer, verErr := ec.Version(ctx)
		if verErr == nil {
			if eggVer != version {
				log.Printf("egg: version mismatch (egg=%s wing=%s) — reconnecting to old egg", eggVer, version)
			}
			log.Printf("egg: connected to existing process (version=%s)", eggVer)
			return ec, nil
		}
		ec.Close()
	}

	// Clean stale files
	os.Remove(sockPath)
	os.Remove(tokenPath)
	pidPath := filepath.Join(cfg.Dir, "egg.pid")
	os.Remove(pidPath)

	// Start a new egg process
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find executable: %w", err)
	}

	logPath := filepath.Join(cfg.Dir, "egg.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open egg log: %w", err)
	}

	child := exec.Command(exe, "egg", "serve")
	child.Stdout = logFile
	child.Stderr = logFile
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := child.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("start egg: %w", err)
	}
	logFile.Close()

	log.Printf("egg: started (pid %d)", child.Process.Pid)

	// Poll for socket to appear (100ms x 50 = 5s timeout)
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		ec, err = egg.Dial(sockPath, tokenPath)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, listErr := ec.List(ctx)
			cancel()
			if listErr == nil {
				return ec, nil
			}
			ec.Close()
		}
	}

	return nil, fmt.Errorf("egg did not start within 5s")
}

// reclaimEggSessions discovers surviving egg sessions and sends pty.reclaim to the relay.
func reclaimEggSessions(ctx context.Context, ec *egg.Client, wsClient *ws.Client) {
	// Small delay to let registration complete
	time.Sleep(500 * time.Millisecond)

	resp, err := ec.List(ctx)
	if err != nil || len(resp.Sessions) == 0 {
		return
	}

	for _, s := range resp.Sessions {
		log.Printf("egg: reclaiming session %s (agent=%s pid=%d)", s.SessionId, s.Agent, s.Pid)
		wsClient.SendReclaim(ctx, s.SessionId)
	}
}

// handlePTYSession bridges a PTY session between the egg (local gRPC) and the relay (remote WS).
// E2E encryption stays in the wing — the egg sees plaintext only.
func handlePTYSession(ctx context.Context, cfg *config.Config, ec *egg.Client, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte) {
	// Set up E2E encryption if browser sent a public key
	var gcmMu sync.Mutex
	var gcm cipher.AEAD
	var wingPubKeyB64 string
	privKey, privKeyErr := auth.LoadPrivateKey(cfg.Dir)
	if privKeyErr != nil {
		log.Printf("pty: load private key: %v (E2E disabled)", privKeyErr)
	} else {
		wingPubKeyB64 = base64.StdEncoding.EncodeToString(privKey.PublicKey().Bytes())
	}
	if start.PublicKey != "" && privKeyErr == nil {
		derived, deriveErr := auth.DeriveSharedKey(privKey, start.PublicKey)
		if deriveErr != nil {
			log.Printf("pty: derive shared key: %v", deriveErr)
		} else {
			gcm = derived
			log.Printf("pty session %s: E2E encryption enabled", start.SessionID)
		}
	}

	// Build env vars to pass to egg
	envMap := make(map[string]string)
	passthrough := []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
		"HOME", "PATH", "SHELL", "TERM", "USER", "LANG",
	}
	for _, k := range passthrough {
		if v := os.Getenv(k); v != "" {
			envMap[k] = v
		}
	}

	// Spawn in egg
	spawnResp, err := ec.Spawn(ctx, &pb.SpawnRequest{
		SessionId: start.SessionID,
		Agent:     start.Agent,
		Cwd:       start.CWD,
		Rows:      uint32(start.Rows),
		Cols:      uint32(start.Cols),
		Env:       envMap,
	})
	if err != nil {
		log.Printf("pty: egg spawn failed: %v", err)
		write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1})
		return
	}

	log.Printf("pty session %s: spawned in egg (pid %d)", start.SessionID, spawnResp.Pid)

	// Notify browser
	write(ws.PTYStarted{
		Type:      ws.TypePTYStarted,
		SessionID: start.SessionID,
		Agent:     start.Agent,
		PublicKey: wingPubKeyB64,
		CWD:       start.CWD,
	})

	// Attach to egg session stream
	stream, err := ec.AttachSession(ctx, start.SessionID)
	if err != nil {
		log.Printf("pty: egg attach failed: %v", err)
		write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1})
		return
	}

	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

	// Read output from egg -> encrypt -> send to browser
	go func() {
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
				gcmMu.Lock()
				currentGCM := gcm
				gcmMu.Unlock()

				var data string
				if currentGCM != nil {
					encrypted, encErr := auth.Encrypt(currentGCM, p.Output)
					if encErr != nil {
						log.Printf("pty session %s: encrypt error: %v", start.SessionID, encErr)
						continue
					}
					data = encrypted
				} else {
					data = base64.StdEncoding.EncodeToString(p.Output)
				}
				write(ws.PTYOutput{
					Type:      ws.TypePTYOutput,
					SessionID: start.SessionID,
					Data:      data,
				})

			case *pb.SessionMsg_ExitCode:
				log.Printf("pty session %s: exited with code %d", start.SessionID, p.ExitCode)
				write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: int(p.ExitCode)})
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
				if attach.PublicKey != "" && privKeyErr == nil {
					derived, deriveErr := auth.DeriveSharedKey(privKey, attach.PublicKey)
					if deriveErr != nil {
						log.Printf("pty session %s: reattach derive key: %v", start.SessionID, deriveErr)
					} else {
						gcmMu.Lock()
						gcm = derived
						gcmMu.Unlock()
						log.Printf("pty session %s: re-keyed E2E for reattach", start.SessionID)
					}
				}

				// Ask egg to replay ring buffer via a new attach stream
				reattachStream, reErr := ec.AttachSession(ctx, start.SessionID)
				if reErr != nil {
					log.Printf("pty session %s: reattach to egg failed: %v", start.SessionID, reErr)
				} else {
					// Read replay data (ring buffer contents)
					replayMsg, rErr := reattachStream.Recv()
					if rErr == nil {
						if replay, ok := replayMsg.Payload.(*pb.SessionMsg_Output); ok && len(replay.Output) > 0 {
							gcmMu.Lock()
							replayGCM := gcm
							gcmMu.Unlock()

							var replayData string
							if replayGCM != nil {
								encrypted, encErr := auth.Encrypt(replayGCM, replay.Output)
								if encErr != nil {
									log.Printf("pty session %s: replay encrypt error: %v", start.SessionID, encErr)
								} else {
									replayData = encrypted
								}
							} else {
								replayData = base64.StdEncoding.EncodeToString(replay.Output)
							}
							if replayData != "" {
								write(ws.PTYOutput{
									Type:      ws.TypePTYOutput,
									SessionID: start.SessionID,
									Data:      replayData,
								})
								log.Printf("pty session %s: replayed %d bytes", start.SessionID, len(replay.Output))
							}
						}
					}
					// Send detach to close the replay stream without affecting the main one
					reattachStream.Send(&pb.SessionMsg{
						SessionId: start.SessionID,
						Payload:   &pb.SessionMsg_Detach{Detach: true},
					})
				}

				write(ws.PTYStarted{
					Type:      ws.TypePTYStarted,
					SessionID: start.SessionID,
					Agent:     start.Agent,
					PublicKey: wingPubKeyB64,
				})

			case ws.TypePTYInput:
				var msg ws.PTYInput
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				gcmMu.Lock()
				currentGCM := gcm
				gcmMu.Unlock()

				var decoded []byte
				if currentGCM != nil {
					var decErr error
					decoded, decErr = auth.Decrypt(currentGCM, msg.Data)
					if decErr != nil {
						log.Printf("pty session %s: decrypt error: %v", start.SessionID, decErr)
						continue
					}
				} else {
					var decErr error
					decoded, decErr = base64.StdEncoding.DecodeString(msg.Data)
					if decErr != nil {
						continue
					}
				}
				stream.Send(&pb.SessionMsg{
					SessionId: start.SessionID,
					Payload:   &pb.SessionMsg_Input{Input: decoded},
				})

			case ws.TypePTYResize:
				var msg ws.PTYResize
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				stream.Send(&pb.SessionMsg{
					SessionId: start.SessionID,
					Payload: &pb.SessionMsg_Resize{Resize: &pb.Resize{
						Rows: uint32(msg.Rows),
						Cols: uint32(msg.Cols),
					}},
				})

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
