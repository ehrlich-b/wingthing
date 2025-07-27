# Wingthing Development TODO

## Phase 1: Core Agent-UI Integration ✅
**Goal: Make enter key work - basic conversation flow**

- [x] Wire agent orchestrator to UI model in `internal/ui/model.go:84`
- [x] Implement dummy LLM responses in orchestrator to test event flow
- [x] Test basic user input → thinking → agent response cycle
- [x] Verify transcript updates correctly with agent events

## Phase 1.5: Better Fake LLM ✅
**Goal: Enhanced dummy responses for better testing, tiny bit more input polish before moving on**

- [x] Default response: "Hi, I'm your fake AI assistant! Here's some of the things I can do:" + multi-line lorem ipsum
- [x] Add short thinking delay before all responses (500ms)
- [x] Special case: prompt "tool" triggers CLI tool call
- [x] Special case: prompt "diff" shows large multiline diff viewer
- [x] Shift+Enter to add a newline in input box (handled by textarea component)
- [x] Add "thinking..." state in UI while waiting for response
- [x] Implement basic error handling for agent events

## Phase 2: Tool Execution Pipeline ✅
**Goal: Enable CLI commands and file operations**

- [x] Complete CLI tool runner with permission requests  
- [x] Connect tool execution to permission modal flow
- [x] Test permission approval/denial in UI
- [x] Implement file read tool (view file contents)
- [x] Implement file write tool (create/overwrite files)
- [x] Implement file edit tool (find/replace operations)
- [x] Add tool result display formatting in renderer
- [x] Implement permission policy (read-only vs write operations)
- [x] Add replace_all parameter to edit tool for flexible editing

## Phase 3: Basic Slash Commands ✅
**Goal: Enable slash command functionality**

- [x] Add slash command detection in input component
- [x] Load and template slash commands from filesystem
- [x] Test `/help`, `/clear`, and custom commands
- [x] Implement basic command execution flow

## Phase 3.5: Command Auto-completion ✅
**Goal: Enhanced input experience**

- [x] Implement command auto-completion dropdown
- [x] Add fuzzy matching for command suggestions
- [x] Test completion UI with keyboard navigation

## Phase 4: Memory & Session Management ✅
**Goal: Persistent context and sessions**

- [x] Implement CLAUDE.md parsing for memory context
- [x] Add session save/load to history store
- [x] Implement `--resume` flag functionality to resume last session
- [x] Implement `/save` command with optional filename parameter, saving as versioned JSON
- [x] Implement `/resume` command to load from most recent save file or specified file
- [x] Test session persistence across app restarts
- [x] Implement `/compact` command for conversation compression (basic version)
- [x] Fixed: Resume logic to prevent resuming current session
- [x] Fixed: Input border disappearing when no completion matches found
- [x] Decision: `/save` takes optional filename and creates versioned JSON files

## Phase 5: Headless Mode
**Goal: One-shot task execution without REPL**

- [ ] Implement headless mode that executes one complete task loop and exits
- [ ] Normal terminal output by default (same as interactive mode)
- [ ] Add `--json` flag for structured JSON output format (for scripting)
- [ ] Process single prompt through full agent loop (thinking → tools → response)
- [ ] Exit when control would normally return to user (no REPL)
- [ ] Test both human-readable and JSON output modes

## Phase 6: Real LLM Integration  
**Goal: Replace dummy with actual AI**

- [ ] Replace dummy responses with real LLM API (OpenAI/Anthropic)
- [ ] Add comprehensive error handling and validation
- [ ] Test with real AI responses and tool usage
- [ ] Optimize token usage and response streaming
- [ ] Implement AI-powered `/compact` command that intelligently summarizes conversation history

## Phase 6.5: Extended Tool Suite
**Goal: More useful tools**

- [ ] Add git tool implementations (status, diff, commit)
- [ ] Add grep tool for searching file contents  
- [ ] Add find tool for file discovery
- [ ] Add more file operations (copy, move, delete)

## Phase 7: Polish & Documentation
**Goal: Production ready**

- [ ] Research and implement MCP protocol support
- [ ] Performance optimization and profiling
- [ ] User documentation and examples
- [ ] Installation and packaging (homebrew, releases)
- [ ] Security review and hardening
- [ ] Expand test coverage to 80%+

## Phase 8: User Management & Authentication
**Goal: Multi-user support (design unclear)**

- [ ] Design user authentication system - unclear if this should be:
  - Simple API key/provider management (local config)
  - Full wingthing.ai service integration with accounts
  - Something else entirely
- [ ] Implement `/login` command (functionality TBD)
- [ ] Add user session management
- [ ] Research: Should wingthing be purely local or cloud-connected?

---

## Current Focus
**Phase 4 Complete! Ready for Phase 5** - implement headless mode with JSON output, or Phase 6 for real LLM integration.

Each phase should be completable in 1-2 focused sessions. Don't try to implement everything at once.
