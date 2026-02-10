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
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/memory"
	"github.com/ehrlich-b/wingthing/internal/orchestrator"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/skill"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/spf13/cobra"
)

// ringBuffer is a fixed-size circular buffer for PTY output replay.
type ringBuffer struct {
	mu   sync.Mutex
	buf  []byte
	size int
	pos  int
	full bool
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{buf: make([]byte, size), size: size}
}

func (r *ringBuffer) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range p {
		r.buf[r.pos] = b
		r.pos = (r.pos + 1) % r.size
		if r.pos == 0 {
			r.full = true
		}
	}
}

func (r *ringBuffer) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]byte(nil), r.buf[:r.pos]...)
	}
	result := make([]byte, r.size)
	copy(result, r.buf[r.pos:])
	copy(result[r.size-r.pos:], r.buf[:r.pos])
	return result
}

func wingCmd() *cobra.Command {
	var relayFlag string
	var labelsFlag string
	var convFlag string

	cmd := &cobra.Command{
		Use:   "wing",
		Short: "Connect to relay and accept remote tasks",
		Long:  "Start a wing — your machine becomes reachable from anywhere via the relay. Same as sitting at the keyboard, just remote.",
		RunE: func(cmd *cobra.Command, args []string) error {
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
			for _, name := range []string{"claude", "ollama", "gemini", "codex", "agent"} {
				if _, err := exec.LookPath(name); err == nil {
					agents = append(agents, name)
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

			fmt.Printf("connecting to %s\n", wsURL)
			fmt.Printf("  agents: %v\n", agents)
			fmt.Printf("  skills: %v\n", skills)
			if len(labels) > 0 {
				fmt.Printf("  labels: %v\n", labels)
			}
			fmt.Printf("  conv: %s\n", convFlag)

			client := &ws.Client{
				RelayURL:  wsURL,
				Token:     tok.Token,
				MachineID: cfg.MachineID,
				Agents:    agents,
				Skills:    skills,
				Labels:    labels,
			}

			client.OnTask = func(ctx context.Context, task ws.TaskSubmit, send ws.ChunkSender) (string, error) {
				return executeRelayTask(ctx, cfg, s, task, send)
			}

			client.OnPTY = func(ctx context.Context, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte) {
				handlePTYSession(ctx, cfg, start, write, input)
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

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			return client.Run(ctx)
		},
	}

	cmd.Flags().StringVar(&relayFlag, "relay", "", "relay server URL (default: ws.wingthing.ai)")
	cmd.Flags().StringVar(&labelsFlag, "labels", "", "comma-separated wing labels (e.g. gpu,cuda,research)")
	cmd.Flags().StringVar(&convFlag, "conv", "auto", "conversation mode: auto (daily rolling), new (fresh), or a named thread")

	return cmd
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

func timeNow() time.Time {
	return time.Now().UTC()
}

// agentCommand returns the command and args for an interactive terminal session.
// Not all agents support terminal mode — returns empty if unsupported.
func agentCommand(agentName string) (string, []string) {
	switch agentName {
	case "claude":
		return "claude", nil
	case "codex":
		return "codex", nil
	case "ollama":
		return "ollama", []string{"run", "llama3.2"}
	default:
		return "", nil
	}
}

// handlePTYSession spawns an agent in a PTY and pipes I/O over WebSocket.
// If the browser sends a public key in pty.start, E2E encryption is used.
// Keeps a ring buffer of recent output for session reattach replay.
func handlePTYSession(ctx context.Context, cfg *config.Config, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte) {
	name, args := agentCommand(start.Agent)
	if name == "" {
		write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1})
		return
	}

	binPath, err := exec.LookPath(name)
	if err != nil {
		log.Printf("pty: agent %q not found: %v", name, err)
		write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1})
		return
	}

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

	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

	cmd := exec.CommandContext(sessionCtx, binPath, args...)
	cmd.Env = os.Environ()

	size := &pty.Winsize{
		Cols: uint16(start.Cols),
		Rows: uint16(start.Rows),
	}
	ptmx, err := pty.StartWithSize(cmd, size)
	if err != nil {
		log.Printf("pty: start failed: %v", err)
		write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1})
		return
	}
	defer ptmx.Close()

	// Ring buffer for replay on reattach (~50KB of plaintext output)
	ring := newRingBuffer(50 * 1024)

	log.Printf("pty session %s: spawned %s (pid %d)", start.SessionID, name, cmd.Process.Pid)

	// Notify browser with wing's public key
	write(ws.PTYStarted{
		Type:      ws.TypePTYStarted,
		SessionID: start.SessionID,
		Agent:     start.Agent,
		PublicKey: wingPubKeyB64,
	})

	// Read PTY output -> send to browser (encrypted if E2E)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				// Always store plaintext in ring buffer for replay
				ring.Write(buf[:n])

				gcmMu.Lock()
				currentGCM := gcm
				gcmMu.Unlock()

				var data string
				if currentGCM != nil {
					encrypted, encErr := auth.Encrypt(currentGCM, buf[:n])
					if encErr != nil {
						log.Printf("pty session %s: encrypt error: %v", start.SessionID, encErr)
						continue
					}
					data = encrypted
				} else {
					data = base64.StdEncoding.EncodeToString(buf[:n])
				}
				write(ws.PTYOutput{
					Type:      ws.TypePTYOutput,
					SessionID: start.SessionID,
					Data:      data,
				})
			}
			if err != nil {
				if err != io.EOF {
					log.Printf("pty session %s: read error: %v", start.SessionID, err)
				}
				return
			}
		}
	}()

	// Process input from browser (decrypt if E2E)
	go func() {
		for data := range input {
			var env ws.Envelope
			if err := json.Unmarshal(data, &env); err != nil {
				continue
			}
			switch env.Type {
			case ws.TypePTYAttach:
				// Reattach: derive new E2E key with new browser's public key
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

				// Replay buffered output with new key
				buffered := ring.Bytes()
				if len(buffered) > 0 {
					gcmMu.Lock()
					replayGCM := gcm
					gcmMu.Unlock()

					var replayData string
					if replayGCM != nil {
						encrypted, encErr := auth.Encrypt(replayGCM, buffered)
						if encErr != nil {
							log.Printf("pty session %s: replay encrypt error: %v", start.SessionID, encErr)
							continue
						}
						replayData = encrypted
					} else {
						replayData = base64.StdEncoding.EncodeToString(buffered)
					}
					write(ws.PTYOutput{
						Type:      ws.TypePTYOutput,
						SessionID: start.SessionID,
						Data:      replayData,
					})
					log.Printf("pty session %s: replayed %d bytes", start.SessionID, len(buffered))
				}

				// Send pty.started so browser knows attach succeeded
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
				ptmx.Write(decoded)

			case ws.TypePTYResize:
				var msg ws.PTYResize
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				pty.Setsize(ptmx, &pty.Winsize{
					Cols: uint16(msg.Cols),
					Rows: uint16(msg.Rows),
				})

			case ws.TypePTYKill:
				log.Printf("pty session %s: kill received, terminating", start.SessionID)
				sessionCancel()
				return
			}
		}
	}()

	exitCode := 0
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	log.Printf("pty session %s: exited with code %d", start.SessionID, exitCode)
	write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: exitCode})
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
