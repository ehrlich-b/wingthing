//go:build !linux

package agent

import "os"

// Process-group cancellation handles descendants on Unix; closing the reader
// bounds shutdown even if an independently detached child retains stdout.
func stopOutputWriters(reader *os.File) {}
