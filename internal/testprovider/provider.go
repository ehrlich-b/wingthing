// Package testprovider contains fake provider protocols for isolated acceptance tests.
package testprovider

import _ "embed"

//go:embed scoped_claude.py
var ScopedClaude string

//go:embed codex.py
var Codex string
