package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestBuildManifest(t *testing.T) {
	dir := t.TempDir()

	// Create some .md files
	os.WriteFile(filepath.Join(dir, "identity.md"), []byte("# Identity\nI am a test."), 0o644)
	os.WriteFile(filepath.Join(dir, "goals.md"), []byte("# Goals\nShip stuff."), 0o644)
	// Non-md file should be ignored
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644)
	// .conflicts dir should be skipped
	os.MkdirAll(filepath.Join(dir, ".conflicts"), 0o755)
	os.WriteFile(filepath.Join(dir, ".conflicts", "old.md"), []byte("conflict"), 0o644)

	m, err := BuildManifest(dir, "mac-01")
	if err != nil {
		t.Fatalf("build manifest: %v", err)
	}

	if m.MachineID != "mac-01" {
		t.Errorf("machine_id = %q, want mac-01", m.MachineID)
	}
	if len(m.Files) != 2 {
		t.Fatalf("got %d files, want 2", len(m.Files))
	}

	byPath := make(map[string]FileEntry)
	for _, f := range m.Files {
		byPath[f.Path] = f
	}

	id, ok := byPath["identity.md"]
	if !ok {
		t.Fatal("missing identity.md")
	}

	expectedHash := sha256.Sum256([]byte("# Identity\nI am a test."))
	if id.SHA256 != hex.EncodeToString(expectedHash[:]) {
		t.Errorf("hash mismatch for identity.md")
	}
	if id.MachineID != "mac-01" {
		t.Errorf("file machine_id = %q, want mac-01", id.MachineID)
	}
	if id.Size != int64(len("# Identity\nI am a test.")) {
		t.Errorf("size = %d, want %d", id.Size, len("# Identity\nI am a test."))
	}
}

func TestDiffManifests(t *testing.T) {
	local := &Manifest{
		MachineID: "mac",
		Files: []FileEntry{
			{Path: "shared.md", SHA256: "aaa"},
			{Path: "local-only.md", SHA256: "bbb"},
			{Path: "changed.md", SHA256: "ccc"},
		},
	}
	remote := &Manifest{
		MachineID: "wsl",
		Files: []FileEntry{
			{Path: "shared.md", SHA256: "aaa"},         // same hash, no diff
			{Path: "remote-only.md", SHA256: "ddd"},     // add
			{Path: "changed.md", SHA256: "eee"},         // update
		},
	}

	diffs := DiffManifests(local, remote)

	if len(diffs) != 2 {
		t.Fatalf("got %d diffs, want 2", len(diffs))
	}

	byPath := make(map[string]FileDiff)
	for _, d := range diffs {
		byPath[d.Path] = d
	}

	add, ok := byPath["remote-only.md"]
	if !ok {
		t.Fatal("missing add diff for remote-only.md")
	}
	if add.Op != OpAdd {
		t.Errorf("remote-only.md op = %q, want add", add.Op)
	}

	update, ok := byPath["changed.md"]
	if !ok {
		t.Fatal("missing update diff for changed.md")
	}
	if update.Op != OpUpdate {
		t.Errorf("changed.md op = %q, want update", update.Op)
	}

	// local-only.md should NOT appear (no deletes)
	if _, ok := byPath["local-only.md"]; ok {
		t.Error("local-only.md should not be in diffs (additive only)")
	}
}

func TestApplyDiffs(t *testing.T) {
	dir := t.TempDir()

	eng := &Engine{
		MemoryDir: dir,
		MachineID: "mac-01",
	}

	diffs := []FileDiff{
		{Path: "new-file.md", Op: OpAdd},
		{Path: "subdir/nested.md", Op: OpAdd},
	}

	contentMap := map[string][]byte{
		"new-file.md":      []byte("# New\nHello."),
		"subdir/nested.md": []byte("# Nested\nDeep."),
	}

	err := eng.ApplyDiffs(diffs, func(path string) ([]byte, error) {
		return contentMap[path], nil
	})
	if err != nil {
		t.Fatalf("apply diffs: %v", err)
	}

	// Verify files were written
	data, err := os.ReadFile(filepath.Join(dir, "new-file.md"))
	if err != nil {
		t.Fatalf("read new-file.md: %v", err)
	}
	if string(data) != "# New\nHello." {
		t.Errorf("new-file.md content = %q", string(data))
	}

	data, err = os.ReadFile(filepath.Join(dir, "subdir", "nested.md"))
	if err != nil {
		t.Fatalf("read nested.md: %v", err)
	}
	if string(data) != "# Nested\nDeep." {
		t.Errorf("nested.md content = %q", string(data))
	}
}

func TestConflictLogging(t *testing.T) {
	dir := t.TempDir()

	// Write an existing file that will be overwritten
	os.WriteFile(filepath.Join(dir, "existing.md"), []byte("local content"), 0o644)

	eng := &Engine{
		MemoryDir: dir,
		MachineID: "mac-01",
	}

	diffs := []FileDiff{
		{Path: "existing.md", Op: OpUpdate},
	}

	err := eng.ApplyDiffs(diffs, func(path string) ([]byte, error) {
		return []byte("remote content"), nil
	})
	if err != nil {
		t.Fatalf("apply diffs: %v", err)
	}

	// File should be overwritten with remote content
	data, err := os.ReadFile(filepath.Join(dir, "existing.md"))
	if err != nil {
		t.Fatalf("read existing.md: %v", err)
	}
	if string(data) != "remote content" {
		t.Errorf("existing.md = %q, want 'remote content'", string(data))
	}

	// Conflict should be logged
	conflictsDir := filepath.Join(dir, ".conflicts")
	entries, err := os.ReadDir(conflictsDir)
	if err != nil {
		t.Fatalf("read conflicts dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d conflict files, want 1", len(entries))
	}

	cdata, err := os.ReadFile(filepath.Join(conflictsDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read conflict file: %v", err)
	}

	var c Conflict
	if err := json.Unmarshal(cdata, &c); err != nil {
		t.Fatalf("unmarshal conflict: %v", err)
	}
	if c.Path != "existing.md" {
		t.Errorf("conflict path = %q, want existing.md", c.Path)
	}
	if c.Resolution != "remote_wins" {
		t.Errorf("resolution = %q, want remote_wins", c.Resolution)
	}

	localHash := sha256.Sum256([]byte("local content"))
	if c.LocalHash != hex.EncodeToString(localHash[:]) {
		t.Error("local hash mismatch in conflict")
	}
	remoteHash := sha256.Sum256([]byte("remote content"))
	if c.RemoteHash != hex.EncodeToString(remoteHash[:]) {
		t.Error("remote hash mismatch in conflict")
	}
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestExportImportThreadEntries(t *testing.T) {
	s1 := openTestStore(t)
	s2 := openTestStore(t)

	taskID := "task-001"
	machineID := "mac-01"

	// Create a task in s1 so FK constraint is satisfied
	now := time.Now().UTC().Truncate(time.Second)
	s1.CreateTask(&store.Task{
		ID: taskID, Type: "prompt", What: "test", RunAt: now, Agent: "claude",
	})

	// Append thread entries to s1
	ts1 := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	ts2 := time.Date(2026, 2, 1, 11, 0, 0, 0, time.UTC)

	s1.AppendThreadAt(&store.ThreadEntry{
		TaskID: &taskID, MachineID: machineID, Summary: "first entry",
	}, ts1)
	s1.AppendThreadAt(&store.ThreadEntry{
		TaskID: &taskID, MachineID: machineID, Summary: "second entry",
	}, ts2)

	// Also add an entry from a different machine that should NOT be exported
	otherMachine := "wsl-01"
	s1.AppendThreadAt(&store.ThreadEntry{
		TaskID: &taskID, MachineID: otherMachine, Summary: "other machine",
	}, ts1)

	eng1 := &Engine{Store: s1, MachineID: machineID}

	// Export since before the entries
	exported, err := eng1.ExportThreadEntries(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(exported) != 2 {
		t.Fatalf("exported %d entries, want 2", len(exported))
	}

	// Create the task in s2 so FK is satisfied
	s2.CreateTask(&store.Task{
		ID: taskID, Type: "prompt", What: "test", RunAt: now, Agent: "claude",
	})

	eng2 := &Engine{Store: s2, MachineID: "wsl-01"}

	// Import into s2
	if err := eng2.ImportThreadEntries(exported); err != nil {
		t.Fatalf("import: %v", err)
	}

	// Verify entries exist in s2
	entries, err := s2.ListRecentThread(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries in s2, want 2", len(entries))
	}

	// Import again — should skip duplicates
	if err := eng2.ImportThreadEntries(exported); err != nil {
		t.Fatalf("second import: %v", err)
	}
	entries, err = s2.ListRecentThread(10)
	if err != nil {
		t.Fatalf("list after second import: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries after second import, want 2 (duplicates should be skipped)", len(entries))
	}
}

func TestManifestJSON(t *testing.T) {
	m := &Manifest{
		MachineID: "mac-01",
		CreatedAt: 1707300000,
		Files: []FileEntry{
			{Path: "identity.md", SHA256: "abc123", Size: 42, ModTime: 1707300000, MachineID: "mac-01"},
			{Path: "goals.md", SHA256: "def456", Size: 18, ModTime: 1707300100, MachineID: "mac-01"},
		},
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	parsed, err := ParseManifest(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if parsed.MachineID != m.MachineID {
		t.Errorf("machine_id = %q, want %q", parsed.MachineID, m.MachineID)
	}
	if parsed.CreatedAt != m.CreatedAt {
		t.Errorf("created_at = %d, want %d", parsed.CreatedAt, m.CreatedAt)
	}
	if len(parsed.Files) != 2 {
		t.Fatalf("got %d files, want 2", len(parsed.Files))
	}
	if parsed.Files[0].Path != "identity.md" {
		t.Errorf("files[0].path = %q, want identity.md", parsed.Files[0].Path)
	}
	if parsed.Files[0].SHA256 != "abc123" {
		t.Errorf("files[0].sha256 = %q, want abc123", parsed.Files[0].SHA256)
	}
	if parsed.Files[1].Size != 18 {
		t.Errorf("files[1].size = %d, want 18", parsed.Files[1].Size)
	}
}
