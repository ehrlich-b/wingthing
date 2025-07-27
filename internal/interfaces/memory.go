package interfaces

// ClaudeContext represents parsed CLAUDE.md content
type ClaudeContext struct {
	Instructions []string          `json:"instructions"`
	Context      map[string]string `json:"context"`
	RawContent   string            `json:"raw_content"`
}

// MemoryManager handles CLAUDE.md memory management
type MemoryManager interface {
	LoadUserMemory(configDir string) error
	LoadProjectMemory(projectDir string) error
	SaveUserMemory(configDir string) error
	SaveProjectMemory(projectDir string) error
	UpdateUserContext(context *ClaudeContext)
	UpdateProjectContext(context *ClaudeContext)
	GetUserContext() *ClaudeContext
	GetProjectContext() *ClaudeContext
	GetCombinedInstructions() []string
	GetCombinedContext() map[string]string
}
