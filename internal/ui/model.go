package ui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/behrlich/wingthing/internal/agent"
	"github.com/behrlich/wingthing/internal/config"
	"github.com/behrlich/wingthing/internal/history"
	"github.com/behrlich/wingthing/internal/interfaces"
	"github.com/behrlich/wingthing/internal/llm"
	"github.com/behrlich/wingthing/internal/tools"
)

type sessionState int

const (
	sessionReady sessionState = iota
	sessionThinking
	sessionWaitingPermission
)

// Animation and live block messages
type tickMsg time.Time

type startLiveMsg struct {
	Block *LiveBlock
}

type finalizeLiveMsg struct {
	ID         string
	FinalLines []string
	Suffix     string // e.g. "✔ Done in 420ms"
}

type Model struct {
	state    sessionState
	width    int
	height   int
	input    InputModel
	modal    ModalModel
	theme    Theme
	renderer *Renderer

	// Live blocks system
	scrollback []string              // immutable committed lines
	liveBlocks map[string]*LiveBlock // active animated rows
	tickEvery  time.Duration         // animation tick interval

	// Agent communication
	events       chan agent.Event
	orchestrator *agent.Orchestrator

	// Slash commands
	commandLoader *agent.CommandLoader

	// Command completion
	showingCompletions      bool
	completionOptions       []string
	selectedCompletionIndex int

	// Permission handling
	currentPermissionRequest *agent.PermissionRequest
	selectedPermissionOption int

	// Session management
	currentSession *interfaces.Session
	historyStore   interfaces.HistoryStore

	// Debug logging
	logger *slog.Logger
}

// generateSessionID creates a unique session ID
func generateSessionID() string {
	bytes := make([]byte, 8)
	rand.Read(bytes)
	return fmt.Sprintf("session_%s_%d", hex.EncodeToString(bytes), time.Now().Unix())
}

func NewModel() Model {
	events := make(chan agent.Event, 100)

	// Set up debug logging
	debugFile, err := os.OpenFile("/tmp/wingthing-debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		panic(err)
	}
	logger := slog.New(slog.NewTextHandler(debugFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.Info("Wingthing debug session started")

	// Create filesystem
	fs := interfaces.NewOSFileSystem()

	// Create tool runner with registered tools
	toolRunner := tools.NewMultiRunner()
	toolRunner.RegisterRunner("cli", tools.NewCLIRunner())

	// Register file operations from EditRunner
	editRunner := tools.NewEditRunner()
	toolRunner.RegisterRunner("read_file", editRunner)
	toolRunner.RegisterRunner("write_file", editRunner)
	toolRunner.RegisterRunner("edit_file", editRunner)

	// Get config directories
	userConfigDir, err := config.GetUserConfigDir()
	if err != nil {
		logger.Warn("Failed to get user config directory", "error", err)
		userConfigDir = ""
	}

	projectDir, err := config.GetProjectDir()
	if err != nil {
		logger.Warn("Failed to get project directory", "error", err)
		projectDir = ""
	}

	// Load configuration
	configManager := config.NewManager(fs)
	if err := configManager.Load(userConfigDir, projectDir); err != nil {
		logger.Warn("Failed to load config", "error", err)
	}
	cfg := configManager.Get()

	// Create other components
	memoryManager := agent.NewMemory(fs)
	permissionChecker := agent.NewPermissionEngine(fs)

	// Create LLM provider - use dummy if no API key configured
	useDummy := cfg.APIKey == ""
	llmProvider := llm.NewProvider(cfg, useDummy)

	// Create orchestrator
	orchestrator := agent.NewOrchestrator(
		toolRunner,
		events,
		memoryManager,
		permissionChecker,
		llmProvider,
	)

	// Create command loader and load commands
	commandLoader := agent.NewCommandLoader()

	// Load commands from both directories
	if err := commandLoader.LoadCommands(userConfigDir, projectDir); err != nil {
		logger.Warn("Failed to load commands", "error", err)
	}

	// Load memory from CLAUDE.md files
	if userConfigDir != "" {
		if err := memoryManager.LoadUserMemory(userConfigDir); err != nil {
			logger.Warn("Failed to load user memory", "error", err)
		}
	}
	if projectDir != "" {
		if err := memoryManager.LoadProjectMemory(projectDir); err != nil {
			logger.Warn("Failed to load project memory", "error", err)
		}
	}

	// Create history store
	historyDir := ""
	if userConfigDir != "" {
		historyDir = userConfigDir + "/history"
	}
	historyStore := history.NewStore(historyDir, fs)

	// Create new session
	currentSession := &interfaces.Session{
		ID:        generateSessionID(),
		Timestamp: time.Now(),
		Messages:  []interfaces.Message{},
		Events:    []interfaces.Event{},
	}

	theme := DefaultTheme()
	return Model{
		state:          sessionReady,
		input:          NewInputModel(),
		modal:          NewModalModel(),
		theme:          theme,
		renderer:       NewRenderer(theme),
		scrollback:     []string{},
		liveBlocks:     make(map[string]*LiveBlock),
		tickEvery:      100 * time.Millisecond,
		events:         events,
		orchestrator:   orchestrator,
		commandLoader:  commandLoader,
		currentSession: currentSession,
		historyStore:   historyStore,
		logger:         logger,
	}
}

// WithResumeFlag configures the model to resume the last session on startup
func (m Model) WithResumeFlag() Model {
	// Try to load the last session
	lastSession, err := m.historyStore.LoadLastSession()
	if err != nil {
		m.logger.Warn("Failed to load last session for --resume flag", "error", err)
		return m
	}
	if lastSession != nil {
		m.currentSession = lastSession
		m.logger.Info("Loaded last session for --resume flag", "session_id", lastSession.ID)
	}
	return m
}

func (m Model) Init() tea.Cmd {
	var initCmds []tea.Cmd

	initCmds = append(initCmds,
		m.input.Init(),
		m.modal.Init(),
		m.listenForEvents(),
		tea.EnableBracketedPaste,
		tickLoop(m.tickEvery),
	)

	// Check if we should show resume message for resumed session
	if len(m.currentSession.Messages) > 0 {
		// This is a resumed session, just show confirmation without replaying
		resumeMsg := fmt.Sprintf("Session resumed (ID: %s) - %d messages loaded", m.currentSession.ID, len(m.currentSession.Messages))
		m.scrollback = append(m.scrollback, m.renderer.AgentFinal(resumeMsg))
	} else {
		// New session, show welcome message
		m.scrollback = append(m.scrollback, m.renderer.Welcome())
	}

	return tea.Batch(initCmds...)
}

func tickLoop(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) listenForEvents() tea.Cmd {
	return func() tea.Msg {
		return <-m.events
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg), nil

	case tea.KeyMsg:
		return m.handleKeyEvent(msg, cmds)

	case agent.Event:
		return m.handleAgentEvent(msg, cmds)

	case tickMsg:
		// advance all spinners
		if len(m.liveBlocks) > 0 {
			slog.Debug("Tick updating live blocks", "count", len(m.liveBlocks))
		}
		for id, lb := range m.liveBlocks {
			var cmd tea.Cmd
			lb.Spinner, cmd = lb.Spinner.Update(msg)
			m.liveBlocks[id] = lb
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		cmds = append(cmds, tickLoop(m.tickEvery))
		return m, tea.Batch(cmds...)

	case startLiveMsg:
		// Initialize the spinner and start the block
		block := msg.Block
		m.liveBlocks[block.ID] = block
		return m, tea.Batch(cmds...)

	case finalizeLiveMsg:
		if lb, ok := m.liveBlocks[msg.ID]; ok {
			delete(m.liveBlocks, msg.ID)
			// Build final text once
			final := m.renderer.FinalizedBlock(lb, msg.FinalLines, msg.Suffix)
			m.scrollback = append(m.scrollback, final)
		}
		return m, nil
	}

	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	if m.modal.IsOpen() {
		return lipgloss.Place(
			m.width, m.height,
			lipgloss.Center, lipgloss.Center,
			m.modal.View(),
			lipgloss.WithWhitespaceChars(""),
		)
	}

	var output strings.Builder

	// 1. committed history (scrollback)
	for _, line := range m.scrollback {
		output.WriteString(line)
		output.WriteByte('\n')
	}

	// 2. live animated section
	if len(m.liveBlocks) > 0 {
		for _, lb := range orderBlocks(m.liveBlocks) {
			output.WriteString(m.renderer.LiveBlock(lb))
			output.WriteByte('\n')
		}
		output.WriteByte('\n') // Add extra spacing after live blocks
	}

	// 3. permission request overlay (if needed)
	if m.state == sessionWaitingPermission && m.currentPermissionRequest != nil {
		// Show permission request overlay
		command := "unknown command"
		if cmd, exists := m.currentPermissionRequest.Parameters["command"]; exists {
			command = fmt.Sprintf("%v", cmd)
		}

		headerStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("11")). // Yellow
			Bold(true)

		output.WriteString(headerStyle.Render("⚠️  Permission Required"))
		output.WriteString("\n")
		output.WriteString(fmt.Sprintf("Tool: %s", m.currentPermissionRequest.Tool))
		output.WriteString("\n")
		output.WriteString(fmt.Sprintf("Command: %s", command))
		output.WriteString("\n\n")

		// Show options with selection
		options := []string{"Allow Once", "No"}
		keys := []string{"A", "N"}

		for i, option := range options {
			if i == m.selectedPermissionOption {
				selectedStyle := lipgloss.NewStyle().
					Foreground(lipgloss.Color("0")).  // Black text
					Background(lipgloss.Color("11")). // Yellow background
					Bold(true)
				output.WriteString(selectedStyle.Render(fmt.Sprintf("> [%s] %s", keys[i], option)))
			} else {
				output.WriteString(fmt.Sprintf("  [%s] %s", keys[i], option))
			}
			output.WriteString("\n")
		}
		output.WriteString("\nUse ↑/↓ to navigate, Enter to select, or press the letter key")
		output.WriteString("\n")
	}

	// 4. input field + completions
	output.WriteString(m.input.View())

	// Add completion dropdown if showing
	if m.showingCompletions && len(m.completionOptions) > 0 {
		output.WriteString("\n")

		// Style for completion dropdown
		borderStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("8")). // Gray
			Bold(false)

		selectedStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).  // Black text
			Background(lipgloss.Color("12")). // Blue background
			Bold(true)

		normalStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")). // White text
			Bold(false)

		// Show completion header
		output.WriteString(borderStyle.Render("Commands:"))
		output.WriteString("\n")

		// Show up to 5 completion options
		maxOptions := 5
		if len(m.completionOptions) < maxOptions {
			maxOptions = len(m.completionOptions)
		}

		for i := 0; i < maxOptions; i++ {
			command := m.completionOptions[i]
			if i == m.selectedCompletionIndex {
				output.WriteString(selectedStyle.Render(fmt.Sprintf("> /%s", command)))
			} else {
				output.WriteString(normalStyle.Render(fmt.Sprintf("  /%s", command)))
			}
			output.WriteString("\n")
		}

		// Show navigation hint
		if len(m.completionOptions) > 0 {
			hintStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color("8")). // Gray
				Italic(true)
			output.WriteString(hintStyle.Render("↑/↓ to navigate, Tab/Enter to select, Esc to cancel"))
		}
	}

	return output.String()
}

// Live block helper functions
func startLive(lb *LiveBlock) tea.Cmd {
	return func() tea.Msg { return startLiveMsg{Block: lb} }
}

func finalizeLast(m *Model, suffix string) tea.Cmd {
	// Find the most recent live block to finalize (in chronological order)
	var mostRecent *LiveBlock
	var mostRecentID string
	var mostRecentTime time.Time

	for id, lb := range m.liveBlocks {
		if mostRecent == nil || lb.StartedAt.After(mostRecentTime) {
			mostRecent = lb
			mostRecentID = id
			mostRecentTime = lb.StartedAt
		}
	}

	if mostRecent != nil {
		return func() tea.Msg {
			return finalizeLiveMsg{
				ID:         mostRecentID,
				FinalLines: mostRecent.Lines,
				Suffix:     suffix,
			}
		}
	}
	return nil
}

func appendToToolBlock(liveBlocks map[string]*LiveBlock, content string) {
	// Find the most recent tool block and append content
	var mostRecent *LiveBlock
	var mostRecentTime time.Time

	for _, lb := range liveBlocks {
		if lb.Kind == LiveTool {
			if mostRecent == nil || lb.StartedAt.After(mostRecentTime) {
				mostRecent = lb
				mostRecentTime = lb.StartedAt
			}
		}
	}

	if mostRecent != nil {
		mostRecent.AppendLine(content)
	}
}

func finalizeThinkingBlocks(m *Model) tea.Cmd {
	// Find and finalize thinking blocks only
	for id, lb := range m.liveBlocks {
		if lb.Kind == LiveThinking {
			return func() tea.Msg {
				return finalizeLiveMsg{
					ID:         id,
					FinalLines: lb.Lines,
					Suffix:     "",
				}
			}
		}
	}
	return nil
}

func finalizeToolBlocks(m *Model, suffix string) tea.Cmd {
	// Find the most recent tool block to finalize
	var mostRecent *LiveBlock
	var mostRecentID string
	var mostRecentTime time.Time
	
	for id, lb := range m.liveBlocks {
		if lb.Kind == LiveTool {
			if mostRecent == nil || lb.StartedAt.After(mostRecentTime) {
				mostRecent = lb
				mostRecentID = id
				mostRecentTime = lb.StartedAt
			}
		}
	}
	
	if mostRecent != nil {
		return func() tea.Msg {
			return finalizeLiveMsg{
				ID:         mostRecentID,
				FinalLines: mostRecent.Lines,
				Suffix:     suffix,
			}
		}
	}
	return nil
}

// orderBlocks returns live blocks in a consistent order (thinking first, then tools by creation time)
func orderBlocks(liveBlocks map[string]*LiveBlock) []*LiveBlock {
	var blocks []*LiveBlock

	// First add thinking blocks
	for _, lb := range liveBlocks {
		if lb.Kind == LiveThinking {
			blocks = append(blocks, lb)
		}
	}

	// Then add tool blocks in creation order
	for _, lb := range liveBlocks {
		if lb.Kind == LiveTool {
			blocks = append(blocks, lb)
		}
	}

	return blocks
}

// handlePermissionResponse processes a permission response and updates the transcript
func (m Model) handlePermissionResponse(choice, decision string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	if m.currentPermissionRequest == nil {
		m.logger.Error("No current permission request to respond to")
		return m, tea.Batch(cmds...)
	}

	// Add the user's choice to scrollback
	responseOutput := m.renderer.PermissionResponse(choice, decision)
	m.scrollback = append(m.scrollback, responseOutput)

	// Send the permission response to the orchestrator
	tool := m.currentPermissionRequest.Tool
	action := "execute" // Standard action for tool execution
	params := m.currentPermissionRequest.Parameters

	switch decision {
	case "allow_once":
		m.orchestrator.GrantPermission(tool, action, params, agent.AllowOnce)
		m.logger.Debug("Granted permission (allow once)", "tool", tool)
		// Retry the pending tool execution
		go func() {
			ctx := context.Background()
			if err := m.orchestrator.RetryPendingTool(ctx); err != nil {
				m.logger.Error("Failed to retry pending tool", "error", err)
			}
		}()
	case "deny":
		m.orchestrator.DenyPermission(tool, action, params, agent.Deny)
		m.logger.Debug("Denied permission (deny once)", "tool", tool)
	default:
		m.logger.Error("Unknown permission decision", "decision", decision)
	}

	// Reset permission state
	m.currentPermissionRequest = nil
	m.selectedPermissionOption = 0
	m.state = sessionThinking
	m.input.SetThinking(true)

	m.logger.Debug("Permission response handled, returning to thinking state")

	return m, tea.Batch(cmds...)
}

// handleSlashCommand processes slash commands
func (m Model) handleSlashCommand(input string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	// Parse command and arguments
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return m, tea.Batch(cmds...)
	}

	command := strings.TrimPrefix(parts[0], "/")
	args := parts[1:]

	m.logger.Debug("Processing slash command", "command", command, "args", args)

	// Handle built-in commands
	switch command {
	case "help":
		return m.handleHelpCommand(cmds)
	case "clear":
		return m.handleClearCommand(cmds)
	case "quit", "exit":
		return m.handleQuitCommand(cmds)
	case "compact":
		return m.handleCompactCommand(cmds)
	case "save":
		return m.handleSaveCommand(args, cmds)
	case "resume":
		return m.handleResumeCommand(args, cmds)
	case "login":
		return m.handlePlaceholderCommand("login", cmds)
	default:
		// Try to find custom command
		return m.handleCustomCommand(command, args, cmds)
	}
}

// handleHelpCommand shows available commands
func (m Model) handleHelpCommand(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	helpText := `Available slash commands:
  /help           - Show this help message
  /clear          - Clear the conversation history
  /quit           - Exit the program
  /save [file]    - Save conversation to versioned JSON file (optional filename)
  /resume [file]  - Resume from most recent save file, or specify filename
  /compact        - Compact conversation (keep first user + last assistant message)
  /login          - Login to service (not implemented yet)
  
Custom commands will be available when command files are loaded.`

	output := m.renderer.AgentFinal(helpText)
	m.scrollback = append(m.scrollback, output)

	return m, tea.Batch(cmds...)
}

// handleClearCommand clears the conversation
func (m Model) handleClearCommand(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	// Send tea.ClearScreen command to clear the terminal
	cmds = append(cmds, tea.ClearScreen)

	// Clear scrollback and live blocks
	m.scrollback = []string{}
	m.liveBlocks = make(map[string]*LiveBlock)

	// Show a confirmation message
	output := m.renderer.AgentFinal("Conversation cleared.")
	m.scrollback = append(m.scrollback, output)

	return m, tea.Batch(cmds...)
}

// handleQuitCommand exits the program
func (m Model) handleQuitCommand(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	// Show a goodbye message
	output := m.renderer.AgentFinal("Goodbye!")
	m.scrollback = append(m.scrollback, output)

	// Exit the program
	cmds = append(cmds, tea.Quit)

	return m, tea.Batch(cmds...)
}

// handleCompactCommand compacts the conversation by keeping only essential messages
func (m Model) handleCompactCommand(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	if len(m.currentSession.Messages) <= 2 {
		output := m.renderer.AgentFinal("Conversation is already compact (2 or fewer messages).")
		m.scrollback = append(m.scrollback, output)
		return m, tea.Batch(cmds...)
	}

	// Keep first user message and last assistant message
	var compactedMessages []interfaces.Message

	// Find first user message
	for _, msg := range m.currentSession.Messages {
		if msg.Role == "user" {
			compactedMessages = append(compactedMessages, msg)
			break
		}
	}

	// Find last assistant message
	for i := len(m.currentSession.Messages) - 1; i >= 0; i-- {
		msg := m.currentSession.Messages[i]
		if msg.Role == "assistant" {
			compactedMessages = append(compactedMessages, msg)
			break
		}
	}

	// Update session with compacted messages
	originalCount := len(m.currentSession.Messages)
	m.currentSession.Messages = compactedMessages

	// Clear screen and scrollback, replay compacted conversation
	cmds = append(cmds, tea.ClearScreen)
	m.scrollback = []string{}
	m.liveBlocks = make(map[string]*LiveBlock)

	// Replay compacted messages
	for _, message := range compactedMessages {
		var output string
		switch message.Role {
		case "user":
			output = m.renderer.User(message.Content)
		case "assistant":
			output = m.renderer.AgentFinal(message.Content)
		default:
			continue
		}
		m.scrollback = append(m.scrollback, output)
	}

	successMsg := fmt.Sprintf("Conversation compacted: %d messages → %d messages", originalCount, len(compactedMessages))
	output := m.renderer.AgentFinal(successMsg)
	m.scrollback = append(m.scrollback, output)

	return m, tea.Batch(cmds...)
}

// handleSaveCommand saves the current conversation to a JSON file
func (m Model) handleSaveCommand(args []string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	// Get filename (optional parameter)
	filename := "save.json"
	if len(args) > 0 {
		filename = args[0]
		if !strings.HasSuffix(filename, ".json") {
			filename += ".json"
		}
	}

	// Add version to filename
	timestamp := time.Now().Format("20060102_150405")
	if filename == "save.json" {
		filename = fmt.Sprintf("save_%s.json", timestamp)
	} else {
		// Insert timestamp before .json extension
		base := filename[:len(filename)-5]
		filename = fmt.Sprintf("%s_%s.json", base, timestamp)
	}

	// Create save directory in user config
	userConfigDir, err := config.GetUserConfigDir()
	if err != nil {
		errorMsg := fmt.Sprintf("Error getting config directory: %v", err)
		output := m.renderer.AgentError(errorMsg)
		m.scrollback = append(m.scrollback, output)
		return m, tea.Batch(cmds...)
	}

	saveDir := userConfigDir + "/saves"
	savePath := saveDir + "/" + filename

	// Create save directory if it doesn't exist
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		errorMsg := fmt.Sprintf("Error creating save directory: %v", err)
		output := m.renderer.AgentError(errorMsg)
		m.scrollback = append(m.scrollback, output)
		return m, tea.Batch(cmds...)
	}

	// Save the current session
	if err := m.historyStore.SaveSession(m.currentSession); err != nil {
		errorMsg := fmt.Sprintf("Error saving session to history: %v", err)
		output := m.renderer.AgentError(errorMsg)
		m.scrollback = append(m.scrollback, output)
		return m, tea.Batch(cmds...)
	}

	// Also save to the specified file
	sessionData, err := json.MarshalIndent(m.currentSession, "", "  ")
	if err != nil {
		errorMsg := fmt.Sprintf("Error marshaling session data: %v", err)
		output := m.renderer.AgentError(errorMsg)
		m.scrollback = append(m.scrollback, output)
		return m, tea.Batch(cmds...)
	}

	if err := os.WriteFile(savePath, sessionData, 0644); err != nil {
		errorMsg := fmt.Sprintf("Error writing save file: %v", err)
		output := m.renderer.AgentError(errorMsg)
		m.scrollback = append(m.scrollback, output)
		return m, tea.Batch(cmds...)
	}

	successMsg := fmt.Sprintf("Conversation saved to %s", savePath)
	output := m.renderer.AgentFinal(successMsg)
	m.scrollback = append(m.scrollback, output)

	return m, tea.Batch(cmds...)
}

// handleResumeCommand loads a conversation from a saved JSON file
func (m Model) handleResumeCommand(args []string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	var filePath string

	if len(args) > 0 {
		// Load from specified file
		filename := args[0]
		if !strings.HasSuffix(filename, ".json") {
			filename += ".json"
		}

		// Try relative path first, then saves directory
		if _, err := os.Stat(filename); err == nil {
			filePath = filename
		} else {
			userConfigDir, err := config.GetUserConfigDir()
			if err != nil {
				errorMsg := fmt.Sprintf("Error getting config directory: %v", err)
				output := m.renderer.AgentError(errorMsg)
				m.scrollback = append(m.scrollback, output)
				return m, tea.Batch(cmds...)
			}
			filePath = userConfigDir + "/saves/" + filename
		}
	} else {
		// Load most recently modified save file
		userConfigDir, err := config.GetUserConfigDir()
		if err != nil {
			errorMsg := fmt.Sprintf("Error getting config directory: %v", err)
			output := m.renderer.AgentError(errorMsg)
			m.scrollback = append(m.scrollback, output)
			return m, tea.Batch(cmds...)
		}

		saveDir := userConfigDir + "/saves"
		entries, err := os.ReadDir(saveDir)
		if err != nil {
			if os.IsNotExist(err) {
				output := m.renderer.AgentFinal("No saved conversations found to resume.")
				m.scrollback = append(m.scrollback, output)
				return m, tea.Batch(cmds...)
			}
			errorMsg := fmt.Sprintf("Error reading saves directory: %v", err)
			output := m.renderer.AgentError(errorMsg)
			m.scrollback = append(m.scrollback, output)
			return m, tea.Batch(cmds...)
		}

		var mostRecentFile string
		var mostRecentTime time.Time

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}

			info, err := entry.Info()
			if err != nil {
				continue
			}

			if info.ModTime().After(mostRecentTime) {
				mostRecentTime = info.ModTime()
				mostRecentFile = entry.Name()
			}
		}

		if mostRecentFile == "" {
			output := m.renderer.AgentFinal("No saved conversations found to resume.")
			m.scrollback = append(m.scrollback, output)
			return m, tea.Batch(cmds...)
		}

		filePath = saveDir + "/" + mostRecentFile
	}

	// Load session from file
	sessionData, err := os.ReadFile(filePath)
	if err != nil {
		errorMsg := fmt.Sprintf("Error reading save file: %v", err)
		output := m.renderer.AgentError(errorMsg)
		m.scrollback = append(m.scrollback, output)
		return m, tea.Batch(cmds...)
	}

	var session interfaces.Session
	if err := json.Unmarshal(sessionData, &session); err != nil {
		errorMsg := fmt.Sprintf("Error parsing save file: %v", err)
		output := m.renderer.AgentError(errorMsg)
		m.scrollback = append(m.scrollback, output)
		return m, tea.Batch(cmds...)
	}

	// Check if trying to resume the current session
	if session.ID == m.currentSession.ID {
		output := m.renderer.AgentFinal("Cannot resume the session you're already in.")
		m.scrollback = append(m.scrollback, output)
		return m, tea.Batch(cmds...)
	}

	m.currentSession = &session

	// Replay the conversation
	return m.replaySession(cmds)
}

// replaySession loads a session without replaying all messages to terminal
func (m Model) replaySession(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	// Just show a confirmation message, don't replay the entire conversation
	successMsg := fmt.Sprintf("Resumed conversation (Session ID: %s) - %d messages loaded", m.currentSession.ID, len(m.currentSession.Messages))
	output := m.renderer.AgentFinal(successMsg)
	m.scrollback = append(m.scrollback, output)

	return m, tea.Batch(cmds...)
}

// handlePlaceholderCommand shows a "not implemented" message for placeholder commands
func (m Model) handlePlaceholderCommand(command string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	message := fmt.Sprintf("/%s not implemented yet!", command)
	output := m.renderer.AgentFinal(message)
	m.scrollback = append(m.scrollback, output)

	return m, tea.Batch(cmds...)
}

// handleCustomCommand executes custom commands loaded from files
func (m Model) handleCustomCommand(command string, args []string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	slashCmd, exists := m.commandLoader.GetCommand(command)
	if !exists {
		errorMsg := fmt.Sprintf("Unknown command: /%s\nType /help for available commands.", command)
		output := m.renderer.AgentFinal(errorMsg)
		m.scrollback = append(m.scrollback, output)
		return m, tea.Batch(cmds...)
	}

	m.logger.Debug("Executing custom command", "name", slashCmd.Name, "description", slashCmd.Description)

	// Prepare environment for template execution
	env := make(map[string]string)
	for _, envVar := range os.Environ() {
		parts := strings.SplitN(envVar, "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
		}
	}

	// Execute the command template
	result, err := m.commandLoader.ExecuteCommand(command, args, env)
	if err != nil {
		errorMsg := fmt.Sprintf("Error executing command /%s: %v", command, err)
		output := m.renderer.AgentFinal(errorMsg)
		m.scrollback = append(m.scrollback, output)
		return m, tea.Batch(cmds...)
	}

	// Show the command result
	output := m.renderer.AgentFinal(result)
	m.scrollback = append(m.scrollback, output)

	return m, tea.Batch(cmds...)
}

// updateCompletions updates command completion suggestions based on current input
// Returns true if completions visibility state changed
func (m *Model) updateCompletions() bool {
	wasShowing := m.showingCompletions
	inputValue := m.input.Value()

	// Only show completions for slash commands
	if !strings.HasPrefix(inputValue, "/") {
		m.showingCompletions = false
		m.completionOptions = nil
		m.selectedCompletionIndex = 0
		return wasShowing != m.showingCompletions
	}

	// Extract the command part (without leading /)
	commandPart := strings.TrimPrefix(inputValue, "/")

	// Get all available commands
	allCommands := []string{"help", "clear", "quit", "exit", "compact", "save", "resume", "login"} // Built-in commands
	allCommands = append(allCommands, m.commandLoader.ListCommands()...)                           // Custom commands

	// Filter commands that match the current input (fuzzy matching)
	var matches []string
	for _, cmd := range allCommands {
		if fuzzyMatch(cmd, commandPart) {
			matches = append(matches, cmd)
		}
	}

	// Update completion state
	if len(matches) > 0 && commandPart != "" {
		m.showingCompletions = true
		m.completionOptions = matches
		// Keep selected index in bounds
		if m.selectedCompletionIndex >= len(matches) {
			m.selectedCompletionIndex = 0
		}
	} else {
		m.showingCompletions = false
		m.completionOptions = nil
		m.selectedCompletionIndex = 0
	}

	// Return true if visibility state changed
	return wasShowing != m.showingCompletions
}

// selectCompletion applies the selected completion to the input
func (m *Model) selectCompletion() {
	if !m.showingCompletions || len(m.completionOptions) == 0 {
		return
	}

	selectedCommand := m.completionOptions[m.selectedCompletionIndex]
	m.input.textarea.SetValue("/" + selectedCommand + " ")
	m.input.textarea.CursorEnd()

	// Fix height after setting value
	m.input.FixDynamicHeight()

	// Hide completions after selection
	m.showingCompletions = false
	m.completionOptions = nil
	m.selectedCompletionIndex = 0
}

// fuzzyMatch implements simple fuzzy matching for command completion
func fuzzyMatch(command, pattern string) bool {
	if pattern == "" {
		return true
	}

	// Convert to lowercase for case-insensitive matching
	command = strings.ToLower(command)
	pattern = strings.ToLower(pattern)

	// First try exact prefix match (highest priority)
	if strings.HasPrefix(command, pattern) {
		return true
	}

	// Then try fuzzy matching - all pattern characters must appear in order
	cmdIndex := 0
	for _, patternChar := range pattern {
		found := false
		for cmdIndex < len(command) {
			if rune(command[cmdIndex]) == patternChar {
				found = true
				cmdIndex++
				break
			}
			cmdIndex++
		}
		if !found {
			return false
		}
	}

	return true
}

// handleWindowSize handles window resize events
func (m Model) handleWindowSize(msg tea.WindowSizeMsg) Model {
	m.width = msg.Width
	m.height = msg.Height
	m.logger.Debug("Window size changed", "width", msg.Width, "height", msg.Height)

	// Update layout - don't set transcript size here, do it in View()
	m.input.SetWidth(msg.Width)
	m.logger.Debug("Window resized", "width", msg.Width, "height", msg.Height)

	return m
}

// handleKeyEvent handles all keyboard events
func (m Model) handleKeyEvent(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	// Handle paste events (v1 style - uses msg.Paste flag)
	if msg.Paste {
		m.logger.Debug("Paste event detected", "content", string(msg.Runes))

		// Add pasted content to input (preserving newlines)
		currentValue := m.input.Value()
		m.input.textarea.SetValue(currentValue + string(msg.Runes))

		// Fix height using the new approach
		m.input.FixDynamicHeight()
		return m, nil
	}

	if m.modal.IsOpen() {
		var cmd tea.Cmd
		m.modal, cmd = m.modal.Update(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	}

	// DEBUG: Log all key events with details
	m.logger.Debug("Key event received", "key", msg.String(), "type", msg.Type, "runes", string(msg.Runes))

	// Handle permission responses
	if m.state == sessionWaitingPermission {
		return m.handlePermissionKeys(msg, cmds)
	}

	// Handle completion navigation if completions are showing
	if m.showingCompletions {
		return m.handleCompletionKeys(msg, cmds)
	}

	return m.handleGeneralKeys(msg, cmds)
}

// handlePermissionKeys handles key events during permission requests
func (m Model) handlePermissionKeys(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.logger.Debug("Ctrl+C pressed, quitting")
		return m, tea.Quit
	case "up", "k":
		if m.selectedPermissionOption > 0 {
			m.selectedPermissionOption--
		}
		return m, nil
	case "down", "j":
		if m.selectedPermissionOption < 1 { // 2 options: 0-1
			m.selectedPermissionOption++
		}
		return m, nil
	case "enter":
		options := []struct{ choice, decision string }{
			{"Allow Once", "allow_once"},
			{"No", "deny"},
		}
		selected := options[m.selectedPermissionOption]
		return m.handlePermissionResponse(selected.choice, selected.decision, cmds)
	case "a", "A":
		return m.handlePermissionResponse("Allow Once", "allow_once", cmds)
	case "n", "N":
		return m.handlePermissionResponse("No", "deny", cmds)
	default:
		// Ignore other keys during permission wait
		return m, nil
	}
}

// handleCompletionKeys handles key events during command completion
func (m Model) handleCompletionKeys(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.logger.Debug("Ctrl+C pressed, quitting")
		return m, tea.Quit
	case "up":
		if m.selectedCompletionIndex > 0 {
			m.selectedCompletionIndex--
		}
		return m, nil
	case "down":
		if m.selectedCompletionIndex < len(m.completionOptions)-1 {
			m.selectedCompletionIndex++
		}
		return m, nil
	case "tab":
		// Tab selects the completion
		m.selectCompletion()
		if m.updateCompletions() {
			// Force a re-render if completions visibility changed
			return m, func() tea.Msg { return tea.WindowSizeMsg{Width: m.width, Height: m.height} }
		}
		return m, nil
	case "enter":
		// Enter completes to selected command AND executes it
		if m.showingCompletions && len(m.completionOptions) > 0 {
			// Complete to the selected command first
			m.selectCompletion()
			m.updateCompletions()
		}

		// Hide completions and execute the (now completed) command
		m.showingCompletions = false
		m.completionOptions = nil
		m.selectedCompletionIndex = 0
		return m.handleEnterKey(cmds)
	case "esc":
		// Escape hides completions
		m.showingCompletions = false
		m.completionOptions = nil
		m.selectedCompletionIndex = 0
		// Force a re-render to fix any display artifacts from hiding completions
		return m, func() tea.Msg { return tea.WindowSizeMsg{Width: m.width, Height: m.height} }
	default:
		// Forward other keys to input and update completions
		var cmd tea.Cmd
		inputPtr, cmd := m.input.Update(msg)
		m.input = *inputPtr
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if m.updateCompletions() {
			// Force a re-render if completions visibility changed
			cmds = append(cmds, func() tea.Msg { return tea.WindowSizeMsg{Width: m.width, Height: m.height} })
		}
		return m, tea.Batch(cmds...)
	}
}

// handleGeneralKeys handles general key events (main input mode)
func (m Model) handleGeneralKeys(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.logger.Debug("Ctrl+C pressed, quitting")
		return m, tea.Quit
	case "enter":
		return m.handleEnterKey(cmds)
	default:
		// Only update input if not thinking
		if m.state != sessionThinking {
			var cmd tea.Cmd
			inputPtr, cmd := m.input.Update(msg)
			m.input = *inputPtr
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			// Update completions after input changes
			if m.updateCompletions() {
				// Force a re-render if completions visibility changed
				cmds = append(cmds, func() tea.Msg { return tea.WindowSizeMsg{Width: m.width, Height: m.height} })
			}
		}
	}
	return m, tea.Batch(cmds...)
}

// handleEnterKey handles enter key press for message submission
func (m Model) handleEnterKey(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	// Ignore enter if thinking
	if m.state == sessionThinking {
		m.logger.Debug("Enter pressed but thinking, ignoring")
		return m, nil
	}

	inputValue := m.input.Value()

	m.logger.Debug("Enter pressed", "input_value", inputValue, "input_length", len(inputValue))
	if inputValue != "" {
		m.logger.Debug("Processing user input", "message", inputValue)
		// Handle user input
		userMsg := inputValue
		(&m.input).Reset()
		m.logger.Debug("Reset input field")

		// Record user message in session
		userMessage := interfaces.Message{
			Role:      "user",
			Content:   userMsg,
			Timestamp: time.Now(),
		}
		m.currentSession.Messages = append(m.currentSession.Messages, userMessage)

		// Add user message to scrollback
		userOutput := m.renderer.User(userMsg)
		m.logger.Debug("Adding user message to scrollback", "content", userOutput)
		m.scrollback = append(m.scrollback, userOutput)
		m.logger.Debug("Added user message to scrollback")

		// Check if this is a slash command
		if strings.HasPrefix(userMsg, "/") {
			m.logger.Debug("Detected slash command")
			return m.handleSlashCommand(userMsg, cmds)
		}

		// Send to agent orchestrator
		m.state = sessionThinking
		m.input.SetThinking(true)
		m.logger.Debug("Set state to thinking")

		go func() {
			ctx := context.Background()
			m.logger.Debug("Starting orchestrator processing")
			m.orchestrator.ProcessPrompt(ctx, userMsg)
		}()
	} else {
		m.logger.Debug("Enter pressed but input is empty")
	}
	return m, tea.Batch(cmds...)
}

// handleAgentEvent handles events from the agent orchestrator
func (m Model) handleAgentEvent(msg agent.Event, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	// Record event in session
	eventRecord := interfaces.Event{
		Type:    msg.Type,
		Content: msg.Content,
		Data:    msg.Data,
	}
	m.currentSession.Events = append(m.currentSession.Events, eventRecord)

	// Handle agent events
	m.logger.Debug("Agent event received", "type", msg.Type, "content", msg.Content)
	switch msg.Type {
	case string(agent.EventTypePlan):
		// Start a thinking block
		lb := NewThinkingBlock()
		cmds = append(cmds, startLive(lb))
		m.logger.Debug("Started thinking live block")
	case string(agent.EventTypeRunTool):
		// Finalize any existing thinking block (not tool blocks) and start a tool block
		if cmd := finalizeThinkingBlocks(&m); cmd != nil {
			cmds = append(cmds, cmd)
		}
		lb := NewToolBlock(msg.Content)
		cmds = append(cmds, startLive(lb))
		m.logger.Debug("Started tool live block", "title", msg.Content)
	case string(agent.EventTypeObservation):
		// Append to current tool block
		appendToToolBlock(m.liveBlocks, msg.Content)
		m.logger.Debug("Appended observation to tool block")
	case string(agent.EventTypeFinal):
		// Record assistant message in session
		assistantMessage := interfaces.Message{
			Role:      "assistant",
			Content:   msg.Content,
			Timestamp: time.Now(),
		}
		m.currentSession.Messages = append(m.currentSession.Messages, assistantMessage)

		// Finalize any remaining live blocks and add final message to scrollback
		if cmd := finalizeThinkingBlocks(&m); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := finalizeToolBlocks(&m, "✔ Done"); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.scrollback = append(m.scrollback, m.renderer.AgentFinal(msg.Content))
		m.state = sessionReady
		m.input.SetThinking(false)
		m.logger.Debug("Finalized live blocks and added final message to scrollback, set state to ready")
	case string(agent.EventTypePermissionRequest):
		// Finalize any thinking blocks since we're waiting for user input
		if cmd := finalizeThinkingBlocks(&m); cmd != nil {
			cmds = append(cmds, cmd)
		}
		// Parse the permission request data
		if permReq, ok := msg.Data.(agent.PermissionRequest); ok {
			m.currentPermissionRequest = &permReq
			m.selectedPermissionOption = 0 // Reset to first option

			m.state = sessionWaitingPermission
			m.input.SetThinking(false) // Allow input for permission response
			m.logger.Debug("Showing permission request", "tool", permReq.Tool)
		} else {
			m.logger.Error("Failed to parse permission request data")
			m.scrollback = append(m.scrollback, m.renderer.AgentError("Failed to parse permission request"))
			m.state = sessionReady
			m.input.SetThinking(false)
		}
	case string(agent.EventTypeError):
		// Finalize any tool blocks and add error to scrollback
		if cmd := finalizeToolBlocks(&m, "❌ Error"); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.scrollback = append(m.scrollback, m.renderer.AgentError(msg.Content))
		m.state = sessionReady
		m.input.SetThinking(false)
		m.logger.Debug("Finalized live blocks and added error message to scrollback, set state to ready")
	}
	// Continue listening for more events
	cmds = append(cmds, m.listenForEvents())

	return m, tea.Batch(cmds...)
}
