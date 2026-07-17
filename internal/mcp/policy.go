// Package mcp exposes a roost's privileged tools as a role-scoped MCP server.
package mcp

import "github.com/ehrlich-b/wingthing/internal/config"

// Policy aliases the wing.yaml MCP configuration used by the protocol adapter.
type Policy = config.MCPConfig

// RolePolicy aliases one role in the wing.yaml MCP configuration.
type RolePolicy = config.MCPRoleConfig
