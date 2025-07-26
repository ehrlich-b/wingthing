# Wingthing - Claude Code Competitor

## Project Overview
Wingthing is a Go-based terminal application that serves as a competitor to Claude Code. It's built using Bubble Tea for the TUI, with a modular architecture supporting both interactive and headless modes.

## Architecture

### Core Components

**Main Binary (`cmd/wingthing/main.go`)**
- Cobra CLI with flags: `-p` (prompt), `--json`, `--max-turns`, `--resume`
- Headless mode for one-shot prompts with JSON output
- Interactive Bubble Tea UI for REPL-style interaction

**UI Layer (`internal/ui/`)**
- `model.go`: Main Bubble Tea model with session state management
- `input.go`: Multiline textarea input component
- `transcript.go`: Scrollable conversation viewport with message types
- `modal.go`: Permission requests and slash command selection
- `theme.go`: Lip Gloss styling configuration

**Agent System (`internal/agent/`)**
- `orchestrator.go`: Main agent loop with event emission
- `events.go`: Structured event types (Plan, RunTool, Observation, Final, PermissionRequest)
- `permissions.go`: Permission engine with persistent rules (AllowOnce, AlwaysAllow, Deny, AlwaysDeny)
- `memory.go`: CLAUDE.md-style memory management (user + project scope)
- `commands.go`: Slash command system with YAML frontmatter and Go templates

**Tools System (`internal/tools/`)**
- `runner.go`: Pluggable tool runner interface
- `bash.go`: Bash command execution with timeout
- `edit.go`: File operations (read, write, edit with find/replace)

**Configuration (`internal/config/`)**
- `config.go`: Hierarchical settings (user overridden by project)
- `paths.go`: Directory discovery (.git, .wingthing detection)

**History (`internal/history/`)**
- `store.go`: Session persistence as JSON files

## Key Features

### Permission System
- Hash-based parameter matching for granular permissions
- Persistent rules across sessions
- Four decision types: AllowOnce, AlwaysAllow, Deny, AlwaysDeny
- JSON storage in user/project config directories

### Slash Commands
- Markdown files with YAML frontmatter in `~/.wingthing/commands` and `.wingthing/commands`
- Go template expansion with `$ARGS`, `$CWD`, environment variables
- Project commands override user commands

### Memory Management
- CLAUDE.md files at user (`~/.wingthing/`) and project (`.wingthing/`) scope
- TODO: Implement proper CLAUDE.md parsing and formatting

### Configuration Hierarchy
- User config: `~/.wingthing/settings.json`
- Project config: `.wingthing/settings.json`
- Project settings override user settings

## Current State

### ✅ Implemented
- Complete project scaffolding with Go 1.24
- Bubble Tea UI with transcript, input, and modals
- Event-driven agent architecture
- Permission system with persistence
- Slash command loader with templating
- Tool runner interface with bash and file tools
- Configuration and history management
- Unit tests for core components

### 🚧 TODO
See [TODO.md](./TODO.md) for comprehensive development plan broken into phases.

## File Structure
```
wingthing/
├── cmd/wingthing/main.go              # CLI entry point
├── internal/
│   ├── ui/                            # Bubble Tea components
│   │   ├── model.go input.go transcript.go modal.go theme.go
│   │   └── transcript_test.go         # UI tests with golden files
│   ├── agent/                         # Agent orchestration
│   │   ├── orchestrator.go events.go permissions.go memory.go commands.go
│   │   ├── permissions_test.go commands_test.go  # Agent tests
│   ├── tools/                         # Tool execution
│   │   ├── runner.go bash.go edit.go
│   ├── config/                        # Configuration management
│   │   ├── config.go paths.go
│   │   └── mocked_config_test.go      # Config tests with mocks
│   ├── history/                       # Session persistence
│   │   └── store.go
│   ├── interfaces/                    # Dependency injection interfaces
│   │   ├── config.go filesystem.go history.go permissions.go
│   └── mocks/                         # Generated mockery mocks
│       └── interfaces/
├── Makefile                           # Build automation
└── go.mod                             # Dependencies
```

## Dependencies
- `github.com/charmbracelet/bubbletea` - TUI framework
- `github.com/charmbracelet/bubbles` - UI components
- `github.com/charmbracelet/lipgloss` - Styling
- `github.com/spf13/cobra` - CLI framework
- `gopkg.in/yaml.v3` - YAML parsing

## Development Commands
**IMPORTANT: Always use Makefile targets - never run go commands directly**

- `make build` - Build the binary (never use `go build`)
- `make test` - Run all tests (never use `go test`)
- `make mocks` - Generate mockery mocks for testing
- `make fmt` - Format code
- `make vet` - Vet code
- `make lint` - Run linter (requires golangci-lint)
- `make clean` - Remove build artifacts
- `make help` - Show all available targets

## Code Style Standards
**File Formatting Requirements:**
- No trailing whitespace in any file
- All files must end with a single newline character
- Use tabs for indentation in Go files (gofmt standard)
- Use 2 spaces for YAML/JSON indentation
- Line endings must be LF (Unix style), not CRLF

**Code Organization:**
- Tests are co-located with source files (not in separate test/ directory)
- Use dependency injection with interface abstractions
- Follow Go naming conventions and package organization
- Error handling follows Go conventions with wrapped errors

## Development Notes
- Dependency injection architecture with interface abstractions
- Comprehensive unit testing with mockery-generated mocks
- Permission hashing uses SHA256 of JSON-marshaled parameters
- Event system uses channels for async communication
- UI uses alt screen mode with mouse support
