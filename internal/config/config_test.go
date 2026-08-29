package config

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

func TestLoadPersistsStableWingID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new-config")
	t.Setenv("WINGTHING_DIR", dir)
	first, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if first.WingID == "" || second.WingID != first.WingID || !wingIDRegex.MatchString(first.WingID) {
		t.Fatalf("wing IDs = %q and %q", first.WingID, second.WingID)
	}
	if info, err := os.Stat(filepath.Join(dir, "wing-id")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("persisted wing ID info=%v err=%v", info, err)
	}
}

func TestLoadRestrictsExistingStateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "existing-config")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("wing_id: 0123456789abcdef01234567\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WINGTHING_DIR", dir)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("state directory permissions = %o, want 700", info.Mode().Perm())
	}
}

func TestDefaultWingIDConcurrentFirstLoadIsStable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new-config")
	const callers = 32
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := defaultWingID(dir)
			ids <- id
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var want string
	for id := range ids {
		if want == "" {
			want = id
		}
		if id != want {
			t.Fatalf("concurrent wing IDs differ: %q and %q", want, id)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "wing-id"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want+"\n" {
		t.Fatalf("persisted wing ID = %q, want %q", data, want+"\n")
	}
}

func TestDefaultWingIDFallsBackWhenHardLinksAreUnavailable(t *testing.T) {
	originalLink := linkWingIDFile
	linkWingIDFile = func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: syscall.EPERM}
	}
	t.Cleanup(func() { linkWingIDFile = originalLink })

	dir := filepath.Join(t.TempDir(), "no-hard-links")
	const callers = 32
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := defaultWingID(dir)
			ids <- id
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var want string
	for id := range ids {
		if want == "" {
			want = id
		}
		if id != want {
			t.Fatalf("fallback wing IDs differ: %q and %q", want, id)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "wing-id"))
	if err != nil || string(data) != want+"\n" {
		t.Fatalf("fallback persisted wing ID = %q, err=%v, want %q", data, err, want+"\n")
	}
}

func TestDefaultWingIDConcurrentCorruptRepairIsStable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wing-id"), []byte("corrupt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	const callers = 32
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := defaultWingID(dir)
			ids <- id
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var want string
	for id := range ids {
		if want == "" {
			want = id
		}
		if id != want {
			t.Fatalf("repaired wing IDs differ: %q and %q", want, id)
		}
	}
	if !wingIDRegex.MatchString(want) {
		t.Fatalf("repaired wing ID %q is invalid", want)
	}
}

func TestLoadReportsWingIDPersistenceFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WINGTHING_DIR", filepath.Join(blocker, "child"))
	if _, err := Load(); err == nil {
		t.Fatal("Load ignored wing ID persistence failure")
	}
}

func TestRelayDBPathMigratesLegacyDatabaseAndSidecars(t *testing.T) {
	dir := t.TempDir()
	for suffix, body := range map[string]string{"": "db", "-wal": "wal", "-shm": "shm"} {
		if err := os.WriteFile(filepath.Join(dir, "social.db")+suffix, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path, err := (&Config{Dir: dir}).RelayDBPath()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "roost.db") {
		t.Fatalf("relay DB path = %q", path)
	}
	for suffix, body := range map[string]string{"": "db", "-wal": "wal", "-shm": "shm"} {
		data, readErr := os.ReadFile(path + suffix)
		if readErr != nil || string(data) != body {
			t.Fatalf("migrated %s = %q, %v", suffix, data, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "social.db") + suffix); !os.IsNotExist(statErr) {
			t.Fatalf("legacy %s remains: %v", suffix, statErr)
		}
	}
}

func TestRelayDBPathConcurrentLegacyMigrationIsStable(t *testing.T) {
	dir := t.TempDir()
	for suffix, body := range map[string]string{"": "db", "-wal": "wal", "-shm": "shm"} {
		if err := os.WriteFile(filepath.Join(dir, "social.db")+suffix, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const callers = 16
	start := make(chan struct{})
	paths := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			path, err := (&Config{Dir: dir}).RelayDBPath()
			paths <- path
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(paths)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent relay DB migration: %v", err)
		}
	}
	wantPath := filepath.Join(dir, "roost.db")
	for path := range paths {
		if path != wantPath {
			t.Fatalf("relay DB path = %q, want %q", path, wantPath)
		}
	}
	for suffix, body := range map[string]string{"": "db", "-wal": "wal", "-shm": "shm"} {
		data, err := os.ReadFile(wantPath + suffix)
		if err != nil || string(data) != body {
			t.Fatalf("migrated %s = %q, %v", suffix, data, err)
		}
	}
}

func TestRelayDBPathRollsBackSidecarsOnMigrationFailure(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "social.db")
	if err := os.WriteFile(oldPath, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath+"-wal", []byte("wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory at the SHM destination forces the second sidecar rename to
	// fail after the WAL has moved, exercising rollback before the DB commit.
	if err := os.WriteFile(oldPath+"-shm", []byte("shm"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "roost.db-shm"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Config{Dir: dir}).RelayDBPath(); err == nil {
		t.Fatal("sidecar migration failure was ignored")
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(oldPath + suffix); err != nil {
			t.Fatalf("legacy %s was not preserved: %v", suffix, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "roost.db-wal")); !os.IsNotExist(err) {
		t.Fatalf("moved WAL was not rolled back: %v", err)
	}
}

func TestRelayDBPathRejectsDirectoryAtDatabasePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "roost.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Config{Dir: dir}).RelayDBPath(); err == nil {
		t.Fatal("database directory was accepted as a regular database")
	}
}
