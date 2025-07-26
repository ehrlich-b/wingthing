# Wingthing Development TODO

## Phase 1: Core Agent-UI Integration ✅
**Goal: Make enter key work - basic conversation flow**

- [x] Wire agent orchestrator to UI model in `internal/ui/model.go:84`
- [x] Implement dummy LLM responses in orchestrator to test event flow
- [x] Test basic user input → thinking → agent response cycle
- [x] Verify transcript updates correctly with agent events

## Phase 1.5: Better Fake LLM ⏳
**Goal: Enhanced dummy responses for better testing, tiny bit more input polish before moving on**

- [ ] Default response: "Hi, I'm your fake AI assistant! Here's some of the things I can do:" + multi-line lorem ipsum
- [ ] Add short thinking delay before all responses
- [ ] Special case: prompt "tool" triggers bash tool call
- [ ] Special case: prompt "diff" shows large multiline diff viewer
- [ ] Shift+Enter to add a newline in input box
- [ ] Add "thinking..." state in UI while waiting for response
- [ ] Implement basic error handling for agent events

## Phase 2: Tool Execution Pipeline
**Goal: Enable bash commands and file operations**

- [ ] Complete bash tool runner with permission requests
- [ ] Connect tool execution to permission modal flow
- [ ] Test permission approval/denial in UI
- [ ] Implement file edit tool integration
- [ ] Add tool result display in transcript

## Phase 3: Slash Commands & Input Enhancement
**Goal: Improve input experience**

- [ ] Add slash command detection in input component
- [ ] Implement command auto-completion dropdown
- [ ] Load and template slash commands from filesystem
- [ ] Test `/help`, `/clear`, and custom commands

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

## Phase 6: Production Features
**Goal: Real LLM and advanced features**

- [ ] Replace dummy responses with real LLM API (OpenAI/Anthropic)
- [ ] Add more tool implementations (git, grep, find)
- [ ] Research and implement MCP protocol support
- [ ] Add comprehensive error handling and validation
- [ ] Expand test coverage

## Phase 7: Polish & Documentation
**Goal: Production ready**

- [ ] Performance optimization
- [ ] User documentation and examples
- [ ] Installation and packaging
- [ ] Security review and hardening

---

## Current Focus
**Start with Phase 1** - get basic conversation working before moving to tools or advanced features.

Each phase should be completable in 1-2 focused sessions. Don't try to implement everything at once.
