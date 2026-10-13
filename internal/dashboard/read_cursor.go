package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// One read cursor per OS viewer/state tree, qualified by the complete saved
// route and session ID. Provider text is never saved. Atomic replacement and a
// lock merge cursors from simultaneous navigators without regressing reads.
func cursorKey(id attentionIdentity) string {
	id.generation = 0
	data, _ := json.Marshal(struct {
		Machine, Session, LocalDir string
		Profile                    any
	}{id.machine, id.session, id.localDir, id.profile})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func readCursors(path string) map[string]int64 {
	cursors := map[string]int64{}
	f, err := os.Open(path)
	if err != nil {
		return cursors
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err == nil && len(data) <= 65536 {
		_ = json.Unmarshal(data, &cursors)
	}
	if cursors == nil {
		cursors = map[string]int64{}
	}
	return cursors
}
func (s *state) loadReadCursors() {
	s.readSeen = readCursors(filepath.Join(s.localDir, "navigator-read.json"))
}
func (s *state) saveReadCursor(id attentionIdentity, seen int64) {
	s.persistReadCursor(id, seen, false)
}
func (s *state) baselineReadCursor(id attentionIdentity, completion int64) int64 {
	return s.persistReadCursor(id, completion, true)
}
func (s *state) persistReadCursor(id attentionIdentity, seen int64, baseline bool) int64 {
	if !s.persistAttention {
		return seen
	}
	path := filepath.Join(s.localDir, "navigator-read.json")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		s.message = "Read cursor not saved; check state-directory permissions"
		return seen
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return seen
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	cursors := readCursors(path)
	key := cursorKey(id)
	persisted := seen
	if previous, exists := cursors[key]; baseline && exists {
		seen = min(previous, seen)
		persisted = previous
	} else if !baseline {
		seen = max(previous, seen)
		persisted = seen
	}
	cursors[key] = persisted
	// Bound persistence. Existing entries are retained until the viewer explicitly
	// reads a new identity; overflow is reported rather than silently forgotten.
	data, err := json.Marshal(cursors)
	if err != nil || len(data) > 65536 {
		s.message = "Read cursor store full; current view still tracks attention"
		return seen
	}
	tmp, err := os.CreateTemp(s.localDir, ".navigator-read-")
	if err != nil {
		return seen
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		s.message = "Read cursor not saved; current view still tracks attention"
		return seen
	}
	if s.readSeen == nil {
		s.readSeen = map[string]int64{}
	}
	s.readSeen[key] = cursors[key]
	return seen
}
