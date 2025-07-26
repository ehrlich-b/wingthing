package interfaces

// MemoryManager handles CLAUDE.md memory management
type MemoryManager interface {
	LoadUserMemory(configDir string) error
	LoadProjectMemory(projectDir string) error
	SaveUserMemory(configDir string) error
	SaveProjectMemory(projectDir string) error
	UpdateUserMemory(key, value string)
	UpdateProjectMemory(key, value string)
	GetUserMemory(key string) (string, bool)
	GetProjectMemory(key string) (string, bool)
}
