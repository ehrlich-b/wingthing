package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func filePolicyFixture(t *testing.T) (ws.SessionInfo, sessionFilePolicy, []string, string) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	data := filepath.Join(root, "support-data")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	rootDeny := ""
	if runtime.GOOS == "linux" {
		rootDeny = "  - deny:/\n"
	}
	rendered := fmt.Sprintf("fs:\n%s  - ro:%s\n  - rw:%s\n  - deny-write:%s\n", rootDeny, repo, data, filepath.Join(data, "protected.txt"))
	session := ws.SessionInfo{SessionID: "session-1", UserID: "alice", CWD: repo, EggConfig: rendered}
	policy, err := loadSessionFilePolicy(session, filepath.Join(root, "home"))
	if err != nil {
		t.Fatal(err)
	}
	return session, policy, []string{repo, data}, data
}

func TestBrowserSessionCapabilitiesGateExportExplicitly(t *testing.T) {
	withoutExport := strings.Join(browserSessionCapabilities(false), ",")
	for _, capability := range []string{capabilitySessionRename, capabilitySessionUpload, capabilitySessionDownload, capabilitySessionResume} {
		if !strings.Contains(withoutExport, capability) {
			t.Fatalf("missing capability %q from %q", capability, withoutExport)
		}
	}
	if strings.Contains(withoutExport, capabilitySessionExport) {
		t.Fatal("export capability advertised without a destination")
	}
	withExport := browserSessionCapabilities(true)
	if withExport[len(withExport)-1] != capabilitySessionExport {
		t.Fatalf("export capability list = %v", withExport)
	}
}

func TestSessionFilePolicyUsesWritableDataAreaAndHonorsEffectiveDeny(t *testing.T) {
	_, policy, userPaths, data := filePolicyFixture(t)
	destination, err := policy.uploadDirectory(userPaths)
	if err != nil {
		t.Fatal(err)
	}
	if destination != wingpolicy.CanonicalSessionPath(data) {
		t.Fatalf("destination = %q, want %q", destination, wingpolicy.CanonicalSessionPath(data))
	}
	if _, ok := policy.writableRoot(filepath.Join(data, "result.txt")); !ok {
		t.Fatalf("writable data file was denied: policy=%#v path=%q", policy, wingpolicy.CanonicalPolicyPath(filepath.Join(data, "result.txt")))
	}
	if _, ok := policy.writableRoot(filepath.Join(data, "protected.txt")); ok {
		t.Fatal("deny-write file was writable")
	}
	if _, ok := policy.writableRoot(filepath.Join(userPaths[0], "source.go")); ok {
		t.Fatal("read-only cwd was writable")
	}
}

func TestSessionFilePolicyBroadExplicitDenyOverridesNarrowWritableRoot(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	policy := sessionFilePolicy{
		writableRoots: []string{data},
		deny:          []string{root},
	}
	if _, ok := policy.writableRoot(filepath.Join(data, "report.txt")); ok {
		t.Fatal("narrow writable root overrode a covering explicit deny")
	}
}

func TestSessionFilePolicyRootDenyMatchesRuntimeAndDenyWriteAlwaysWins(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(data, "report.txt")
	policy := sessionFilePolicy{writableRoots: []string{data}, deny: []string{string(filepath.Separator)}}
	_, allowed := policy.writableRoot(path)
	if allowed != (runtime.GOOS == "linux") {
		t.Fatalf("deny:/ writable result = %v on %s", allowed, runtime.GOOS)
	}
	policy = sessionFilePolicy{writableRoots: []string{data}, denyWrite: []string{string(filepath.Separator)}}
	if _, allowed := policy.writableRoot(path); allowed {
		t.Fatal("deny-write:/ did not override writable root")
	}
}

func TestResolveOwnedSessionFileTargetRequiresOwnerPathAndEffectiveConfig(t *testing.T) {
	session, _, userPaths, _ := filePolicyFixture(t)
	req := ws.TunnelRequest{SenderUserID: "alice"}
	if _, _, err := resolveOwnedSessionFileTarget(req, session.SessionID, []ws.SessionInfo{session}, userPaths, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		req     ws.TunnelRequest
		paths   []string
		session ws.SessionInfo
	}{
		"other owner":              {req: ws.TunnelRequest{SenderUserID: "bob"}, paths: userPaths, session: session},
		"other administrator":      {req: ws.TunnelRequest{SenderUserID: "admin", SenderOrgRole: "admin"}, paths: userPaths, session: session},
		"revoked path":             {req: req, paths: []string{t.TempDir()}, session: session},
		"missing effective config": {req: req, paths: userPaths, session: func() ws.SessionInfo { copy := session; copy.EggConfig = ""; return copy }()},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := resolveOwnedSessionFileTarget(test.req, test.session.SessionID, []ws.SessionInfo{test.session}, test.paths, t.TempDir()); err == nil {
				t.Fatal("access unexpectedly allowed")
			}
		})
	}
}

func TestPersonalOwnerSessionFilesWorkWithoutConfiguredWingPaths(t *testing.T) {
	session, policy, _, dataDir := filePolicyFixture(t)
	owner := ws.TunnelRequest{SenderUserID: session.UserID, SenderOrgRole: "owner"}
	resolved, err := resolveOwnedActiveSession(owner, session.SessionID, []ws.SessionInfo{session}, nil)
	if err != nil {
		t.Fatalf("personal owner session resolution failed: %v", err)
	}
	destination, err := policy.uploadDirectory(nil)
	if err != nil {
		t.Fatalf("personal owner upload destination failed: %v", err)
	}
	if destination != wingpolicy.CanonicalSessionPath(dataDir) {
		t.Fatalf("destination = %q, want %q", destination, wingpolicy.CanonicalSessionPath(dataDir))
	}
	path := filepath.Join(dataDir, "result.txt")
	if err := os.WriteFile(path, []byte("result"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, _, _, err := openSessionFile(resolved, policy, nil, path)
	if err != nil {
		t.Fatalf("personal owner download failed: %v", err)
	}
	_ = file.Close()
	member := ws.TunnelRequest{SenderUserID: session.UserID, SenderOrgRole: "member"}
	if _, err := resolveOwnedActiveSession(member, session.SessionID, []ws.SessionInfo{session}, nil); err == nil {
		t.Fatal("org member without a current path grant was allowed")
	}
}

func TestSessionUploadIsBoundPrivateAndNoClobber(t *testing.T) {
	session, policy, userPaths, dataDir := filePolicyFixture(t)
	registry := newSessionUploadRegistry()
	req := ws.TunnelRequest{SenderUserID: "alice", SenderPub: "browser-key"}
	upload, err := registry.begin(session, policy, userPaths, req, "evidence.bin", 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.append(upload.id, "bob", req.SenderPub, 0, []byte("no")); err == nil {
		t.Fatal("different owner appended")
	}
	if _, err := registry.append(upload.id, req.SenderUserID, "other-browser", 0, []byte("no")); err == nil {
		t.Fatal("different browser appended")
	}
	if _, err := registry.append(upload.id, req.SenderUserID, req.SenderPub, 0, []byte("data")); err != nil {
		t.Fatal(err)
	}
	finished, err := registry.finish(upload.id, req.SenderUserID, req.SenderPub)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = finished.root.Close() }()
	if _, _, err := finished.commit(finished.destination); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, "evidence.bin")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if _, _, err := writeSessionFileRoot(finished.root, finished.name, strings.NewReader("replace")); err == nil {
		t.Fatal("existing file overwritten")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "data" {
		t.Fatalf("file changed: %q", got)
	}
}

func TestWriteSessionFileRootDoesNotCommitPastTransferLimit(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	reader := &boundedSessionFileReader{reader: strings.NewReader("four"), remaining: 3}
	if _, _, err := writeSessionFileRoot(root, "too-large.txt", reader); !errors.Is(err, errSessionFileTooLarge) {
		t.Fatalf("limit error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "too-large.txt")); !os.IsNotExist(err) {
		t.Fatalf("oversized file was committed: %v", err)
	}
}

func TestOpenSessionFileRejectsTraversalSymlinkNonRegularAndOversize(t *testing.T) {
	session, policy, userPaths, dataDir := filePolicyFixture(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dataDir, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := openSessionFile(session, policy, userPaths, filepath.Join(dataDir, "escape")); err == nil {
		t.Fatal("symlink escape opened")
	}
	if err := os.Mkdir(filepath.Join(dataDir, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := openSessionFile(session, policy, userPaths, filepath.Join(dataDir, "directory")); err == nil {
		t.Fatal("directory opened")
	}
	large := filepath.Join(dataDir, "large.bin")
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxSessionDownloadSize + 1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, _, _, err := openSessionFile(session, policy, userPaths, large); err == nil {
		t.Fatal("oversized file opened")
	}
	if _, _, _, err := openSessionFile(session, policy, userPaths, "../outside"); err == nil {
		t.Fatal("traversal opened")
	}
}

func TestOpenSessionFileNoFollowRejectsSymlinkComponent(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "swapped")); err != nil {
		t.Fatal(err)
	}
	if file, err := openSessionFileNoFollow(root, filepath.Join("swapped", "secret.txt")); err == nil {
		_ = file.Close()
		t.Fatal("symlink component was followed")
	}
}

func TestExportSessionFileIsPerOwnerNoClobberAndRejectsSymlinkRoot(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(sourcePath, []byte("answer"), 0o600); err != nil {
		t.Fatal(err)
	}
	export := func(owner string) error {
		source, err := os.Open(sourcePath)
		if err != nil {
			return err
		}
		defer func() { _ = source.Close() }()
		info, _ := source.Stat()
		_, _, err = exportSessionFile(source, info, config.ExportTarget{Name: "isolated", Path: root, Members: []string{"support@example.com"}}, owner)
		return err
	}
	if err := export("alice"); err != nil {
		t.Fatal(err)
	}
	if err := export("bob"); err != nil {
		t.Fatal(err)
	}
	if err := export("alice"); err == nil {
		t.Fatal("same owner overwrote export")
	}
	for _, owner := range []string{"alice", "bob"} {
		got, err := os.ReadFile(filepath.Join(root, eggclient.UserHash(owner), "report.txt"))
		if err != nil || string(got) != "answer" {
			t.Fatalf("owner %s export = %q err=%v", owner, got, err)
		}
	}
	symlink := filepath.Join(t.TempDir(), "export-link")
	if err := os.Symlink(root, symlink); err != nil {
		t.Fatal(err)
	}
	source, _ := os.Open(sourcePath)
	defer func() { _ = source.Close() }()
	info, _ := source.Stat()
	if _, _, err := exportSessionFile(source, info, config.ExportTarget{Name: "bad", Path: symlink, Members: []string{"support@example.com"}}, "carol"); err == nil {
		t.Fatal("symlink export root accepted")
	}
	maliciousRoot := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(maliciousRoot, eggclient.UserHash("carol"))); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := exportSessionFile(source, info, config.ExportTarget{Name: "bad-owner", Path: maliciousRoot, Members: []string{"support@example.com"}}, "carol"); err == nil {
		t.Fatal("symlink owner export directory accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "report.txt")); !os.IsNotExist(err) {
		t.Fatalf("export escaped through owner symlink: %v", err)
	}
}

func TestExportSessionFileRejectsOwnerDirectorySwap(t *testing.T) {
	root := t.TempDir()
	ownerPath := filepath.Join(root, eggclient.UserHash("alice"))
	otherPath := filepath.Join(root, eggclient.UserHash("bob"))
	for _, path := range []string{ownerPath, otherPath} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sourcePath := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(sourcePath, []byte("answer"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	info, err := source.Stat()
	if err != nil {
		t.Fatal(err)
	}
	originalOpen := openSessionExportOwnerRoot
	t.Cleanup(func() { openSessionExportOwnerRoot = originalOpen })
	openSessionExportOwnerRoot = func(parent *os.Root, name string) (*os.Root, error) {
		// Swap after Lstat validated the owner, immediately before its open.
		if err := os.Rename(ownerPath, filepath.Join(root, "parked-owner")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Base(otherPath), ownerPath); err != nil {
			t.Fatal(err)
		}
		return originalOpen(parent, name)
	}
	if _, _, err := exportSessionFile(source, info, config.ExportTarget{Name: "isolated", Path: root}, "alice"); err == nil {
		t.Fatal("export accepted an owner directory swapped after validation")
	}
	if _, err := os.Stat(filepath.Join(otherPath, info.Name())); !os.IsNotExist(err) {
		t.Fatalf("export followed the owner symlink into another owner's area: %v", err)
	}
	otherInfo, err := os.Stat(otherPath)
	if err != nil || otherInfo.Mode().Perm() != 0o755 {
		t.Fatalf("export changed another owner's directory permissions: info=%v err=%v", otherInfo, err)
	}
	if err := os.Remove(ownerPath); err != nil {
		t.Fatal(err)
	}
	parked := filepath.Join(root, "parked-owner")
	if err := os.Rename(parked, ownerPath); err != nil {
		t.Fatal(err)
	}
	openSessionExportOwnerRoot = func(parent *os.Root, name string) (*os.Root, error) {
		bound, err := originalOpen(parent, name)
		if err != nil {
			return nil, err
		}
		// A swap after opening must leave chmod and writes on this descriptor.
		if err := os.Rename(ownerPath, parked); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Base(otherPath), ownerPath); err != nil {
			t.Fatal(err)
		}
		return bound, nil
	}
	if _, _, err := exportSessionFile(source, info, config.ExportTarget{Name: "isolated", Path: root}, "alice"); err != nil {
		t.Fatalf("export through the retained owner root failed: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(parked, info.Name())); err != nil || string(data) != "answer" {
		t.Fatalf("export did not use the retained owner root: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(otherPath, info.Name())); !os.IsNotExist(err) {
		t.Fatalf("export reopened the swapped owner path: %v", err)
	}
	otherInfo, err = os.Stat(otherPath)
	if err != nil || otherInfo.Mode().Perm() != 0o755 {
		t.Fatalf("chmod reopened the swapped owner path: info=%v err=%v", otherInfo, err)
	}
}

func TestSessionUploadCommitRejectsReplacedDestination(t *testing.T) {
	session, policy, paths, destination := filePolicyFixture(t)
	registry := newSessionUploadRegistry()
	req := ws.TunnelRequest{SenderUserID: "alice", SenderPub: "browser"}
	upload, err := registry.begin(session, policy, paths, req, "report.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := registry.finish(upload.id, req.SenderUserID, req.SenderPub)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = finished.root.Close() }()
	moved := destination + "-moved"
	if err := os.Rename(destination, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	current, err := policy.uploadDirectory(paths)
	if err != nil || current != finished.destination {
		t.Fatalf("replacement did not preserve the authorized pathname: %q, %v", current, err)
	}
	if _, _, err := finished.commit(current); err == nil {
		t.Fatal("upload committed through a handle to a replaced destination")
	}
	for _, path := range []string{destination, moved} {
		if _, err := os.Stat(filepath.Join(path, finished.name)); !os.IsNotExist(err) {
			t.Fatalf("rejected upload wrote to %q: %v", path, err)
		}
	}
}

type sessionFileSwapReader struct {
	io.Reader
	swap func()
}

func (r *sessionFileSwapReader) Read(buffer []byte) (int, error) {
	if r.swap != nil {
		r.swap()
		r.swap = nil
	}
	return r.Reader.Read(buffer)
}

func TestSessionUploadRechecksDestinationBeforeLink(t *testing.T) {
	session, policy, paths, destination := filePolicyFixture(t)
	registry := newSessionUploadRegistry()
	req := ws.TunnelRequest{SenderUserID: "alice", SenderPub: "browser"}
	upload, err := registry.begin(session, policy, paths, req, "report.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := registry.finish(upload.id, req.SenderUserID, req.SenderPub)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = finished.root.Close() }()
	if err := finished.validateDestination(finished.destination); err != nil {
		t.Fatal(err)
	}
	moved := destination + "-moved"
	source := &sessionFileSwapReader{Reader: strings.NewReader("answer"), swap: func() {
		if err := os.Rename(destination, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(destination, 0o700); err != nil {
			t.Fatal(err)
		}
	}}
	if _, _, err := writeSessionFileRootChecked(finished.root, finished.name, source, func() error {
		return finished.validateDestination(finished.destination)
	}); err == nil {
		t.Fatal("upload committed after the destination changed during its write")
	}
	for _, path := range []string{destination, moved} {
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			t.Fatalf("rejected upload left files in %q: entries=%v err=%v", path, entries, err)
		}
	}
}
