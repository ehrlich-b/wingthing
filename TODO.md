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

## Phase 3: Basic Slash Commands
**Goal: Enable slash command functionality**

- [ ] Add slash command detection in input component
- [ ] Load and template slash commands from filesystem
- [ ] Test `/help`, `/clear`, and custom commands
- [ ] Implement basic command execution flow

## Phase 3.5: Command Auto-completion
**Goal: Enhanced input experience**

- [ ] Implement command auto-completion dropdown
- [ ] Add fuzzy matching for command suggestions
- [ ] Test completion UI with keyboard navigation

## Phase 4: Memory & Session Management
**Goal: Persistent context and sessions**

- [ ] Implement CLAUDE.md parsing for memory context
- [ ] Add session save/load to history store
- [ ] Implement `--resume` flag functionality
- [ ] Test session persistence across app restarts

## Phase 5: Headless Mode
**Goal: Scriptable JSON interface**

- [ ] Complete headless mode with `--json` flag
- [ ] Implement one-shot prompt processing
- [ ] Add structured JSON output format
- [ ] Test CLI scripting integration

## Phase 6: Real LLM Integration  
**Goal: Replace dummy with actual AI**

- [ ] Replace dummy responses with real LLM API (OpenAI/Anthropic)
- [ ] Add comprehensive error handling and validation
- [ ] Test with real AI responses and tool usage
- [ ] Optimize token usage and response streaming

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

---

## Current Focus
**Phase 1.5 Complete! Ready for Phase 2** - implement tool execution pipeline with permissions.

Each phase should be completable in 1-2 focused sessions. Don't try to implement everything at once.
