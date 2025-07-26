package interfaces

// PermissionDecision represents a permission decision
type PermissionDecision string

const (
	AllowOnce    PermissionDecision = "allow_once"
	AlwaysAllow  PermissionDecision = "always_allow"
	Deny         PermissionDecision = "deny"
	AlwaysDeny   PermissionDecision = "always_deny"
)

// PermissionChecker handles permission checking and management
type PermissionChecker interface {
	CheckPermission(tool, action string, params map[string]any) (bool, error)
	GrantPermission(tool, action string, params map[string]any, decision PermissionDecision)
	DenyPermission(tool, action string, params map[string]any, decision PermissionDecision)
	LoadFromFile(filePath string) error
	SaveToFile(filePath string) error
}
