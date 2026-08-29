// Package fsutil contains the small filesystem primitives shared by
// security-sensitive persistence code.
package fsutil

// SyncDirectory makes a completed rename or removal durable before its caller
// reports success. Syncing only the file does not persist the containing
// directory entry on every filesystem. Windows has no equivalent operation
// through Go's directory handle, so that implementation validates the target
// and relies on the atomic rename's platform semantics.
