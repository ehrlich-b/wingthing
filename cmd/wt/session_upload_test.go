package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestValidSessionUploadName(t *testing.T) {
	for _, name := range []string{"evidence.png", "support notes.txt", "résumé.pdf", ".env.example"} {
		if !validSessionUploadName(name) {
			t.Errorf("validSessionUploadName(%q) = false", name)
		}
	}
	for _, name := range []string{"", ".", "..", "../secret", "sub/file", `sub\file`, "/tmp/file", "bad\x00name", "line\nbreak"} {
		if validSessionUploadName(name) {
			t.Errorf("validSessionUploadName(%q) = true", name)
		}
	}
	for _, name := range []string{"bad\x00name", "line\nbreak", "tab\tname"} {
		if validSessionUploadName(name) {
			t.Errorf("validSessionUploadName(%q) = true", name)
		}
	}
}

func TestSessionUploadTargetRequiresExactOwnerAndCurrentPathGrant(t *testing.T) {
	cwd := t.TempDir()
	sessions := []ws.SessionInfo{{SessionID: "session-1", UserID: "alice", CWD: cwd}}
	member := ws.TunnelRequest{SenderUserID: "alice", SenderOrgRole: "member"}
	target, err := sessionUploadTarget(member, "session-1", sessions, []string{cwd})
	if err != nil {
		t.Fatalf("sessionUploadTarget: %v", err)
	}
	if target.CWD != canonicalSessionPath(cwd) {
		t.Fatalf("cwd = %q, want %q", target.CWD, canonicalSessionPath(cwd))
	}

	for name, req := range map[string]ws.TunnelRequest{
		"different member": {SenderUserID: "bob", SenderOrgRole: "member"},
		"different admin":  {SenderUserID: "admin", SenderOrgRole: "admin"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := sessionUploadTarget(req, "session-1", sessions, []string{cwd}); err == nil {
				t.Fatal("other user reached session upload target")
			}
		})
	}
	if _, err := sessionUploadTarget(member, "session-1", sessions, []string{t.TempDir()}); err == nil {
		t.Fatal("member reached session outside current path grant")
	}
	if _, err := sessionUploadTarget(member, "missing", sessions, []string{cwd}); err == nil {
		t.Fatal("missing session resolved an upload target")
	}
}

func TestSessionUploadRegistryBindsChunksAndExpiresAbandonedUploads(t *testing.T) {
	cwd := t.TempDir()
	now := time.Unix(1000, 0)
	registry := newSessionUploadRegistry()
	registry.now = func() time.Time { return now }
	req := ws.TunnelRequest{SenderUserID: "alice", SenderPub: "browser-key"}
	session := ws.SessionInfo{SessionID: "session-1", UserID: "alice", CWD: cwd}

	upload, err := registry.begin(session, req, "evidence.bin", 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.append(upload.id, "mallory", req.SenderPub, 0, []byte{1}); err == nil {
		t.Fatal("different user appended to upload")
	}
	if _, err := registry.append(upload.id, req.SenderUserID, "other-key", 0, []byte{1}); err == nil {
		t.Fatal("different browser key appended to upload")
	}
	if _, err := registry.append(upload.id, req.SenderUserID, req.SenderPub, 1, []byte{1}); err == nil {
		t.Fatal("out-of-order chunk was accepted")
	}
	if received, err := registry.append(upload.id, req.SenderUserID, req.SenderPub, 0, []byte{1, 2}); err != nil || received != 2 {
		t.Fatalf("first chunk: received=%d err=%v", received, err)
	}
	if _, err := registry.finish(upload.id, req.SenderUserID, req.SenderPub); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete finish error = %v", err)
	}
	if received, err := registry.append(upload.id, req.SenderUserID, req.SenderPub, 2, []byte{3, 4}); err != nil || received != 4 {
		t.Fatalf("second chunk: received=%d err=%v", received, err)
	}
	finished, err := registry.finish(upload.id, req.SenderUserID, req.SenderPub)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = finished.root.Close() }()
	if !bytes.Equal(finished.data, []byte{1, 2, 3, 4}) {
		t.Fatalf("finished data = %v", finished.data)
	}

	abandoned, err := registry.begin(session, req, "later.bin", 1)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(sessionUploadExpiration + time.Second)
	if _, err := registry.append(abandoned.id, req.SenderUserID, req.SenderPub, 0, []byte{1}); err == nil {
		t.Fatal("expired upload accepted a chunk")
	}
}

func TestSessionUploadRegistryRejectsOversizedChunkAndDeclaredSize(t *testing.T) {
	cwd := t.TempDir()
	registry := newSessionUploadRegistry()
	req := ws.TunnelRequest{SenderUserID: "alice", SenderPub: "browser-key"}
	session := ws.SessionInfo{SessionID: "session-1", UserID: "alice", CWD: cwd}
	if _, err := registry.begin(session, req, "huge.bin", maxSessionUploadSize+1); err == nil {
		t.Fatal("oversized upload was admitted")
	}
	upload, err := registry.begin(session, req, "chunk.bin", maxSessionUploadChunk+1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.cancel(upload.id, req.SenderUserID, req.SenderPub) }()
	if _, err := registry.append(upload.id, req.SenderUserID, req.SenderPub, 0, make([]byte, maxSessionUploadChunk+1)); err == nil {
		t.Fatal("oversized chunk was accepted")
	}
	small, err := registry.begin(session, req, "declared.bin", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.cancel(small.id, req.SenderUserID, req.SenderPub) }()
	if _, err := registry.append(small.id, req.SenderUserID, req.SenderPub, 0, []byte{1, 2}); err == nil {
		t.Fatal("chunk exceeding declared size was accepted")
	}
}

func TestSessionUploadRegistryEnforcesAdmissionLimitsAndReleasesCapacity(t *testing.T) {
	session := ws.SessionInfo{SessionID: "session-1", UserID: "alice", CWD: t.TempDir()}
	req := func(user string) ws.TunnelRequest {
		return ws.TunnelRequest{SenderUserID: user, SenderPub: "browser-key"}
	}

	t.Run("per client", func(t *testing.T) {
		registry := newSessionUploadRegistry()
		first, err := registry.begin(session, req("alice"), "first.bin", 1)
		if err != nil {
			t.Fatal(err)
		}
		second, err := registry.begin(session, req("alice"), "second.bin", 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := registry.begin(session, req("alice"), "third.bin", 1); err == nil {
			t.Fatal("third upload from one client was admitted")
		}
		if err := registry.cancel(first.id, "mallory", "browser-key"); err == nil {
			t.Fatal("another user cancelled the upload")
		}
		if err := registry.cancel(first.id, "alice", "browser-key"); err != nil {
			t.Fatal(err)
		}
		third, err := registry.begin(session, req("alice"), "third.bin", 1)
		if err != nil {
			t.Fatalf("capacity was not released after cancel: %v", err)
		}
		for _, upload := range []*sessionUpload{second, third} {
			if err := registry.cancel(upload.id, upload.userID, upload.senderPub); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("global count", func(t *testing.T) {
		registry := newSessionUploadRegistry()
		uploads := make([]*sessionUpload, 0, maxActiveSessionUploads)
		for i := range maxActiveSessionUploads {
			upload, err := registry.begin(session, req(fmt.Sprintf("user-%d", i)), fmt.Sprintf("file-%d.bin", i), 1)
			if err != nil {
				t.Fatal(err)
			}
			uploads = append(uploads, upload)
		}
		if _, err := registry.begin(session, req("overflow"), "overflow.bin", 1); err == nil {
			t.Fatal("upload beyond global count was admitted")
		}
		for _, upload := range uploads {
			if err := registry.cancel(upload.id, upload.userID, upload.senderPub); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("reserved bytes", func(t *testing.T) {
		registry := newSessionUploadRegistry()
		var uploads []*sessionUpload
		for i, size := range []int64{25 << 20, 25 << 20, 14 << 20} {
			upload, err := registry.begin(session, req(fmt.Sprintf("user-%d", i)), fmt.Sprintf("file-%d.bin", i), size)
			if err != nil {
				t.Fatal(err)
			}
			uploads = append(uploads, upload)
		}
		if _, err := registry.begin(session, req("overflow"), "overflow.bin", 1); err == nil {
			t.Fatal("upload beyond reserved capacity was admitted")
		}
		if err := registry.cancel(uploads[2].id, uploads[2].userID, uploads[2].senderPub); err != nil {
			t.Fatal(err)
		}
		replacement, err := registry.begin(session, req("replacement"), "replacement.bin", 1)
		if err != nil {
			t.Fatalf("reserved capacity was not released after cancel: %v", err)
		}
		uploads[2] = replacement
		for _, upload := range uploads {
			if err := registry.cancel(upload.id, upload.userID, upload.senderPub); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestSessionUploadRegistryFinishesEmptyFile(t *testing.T) {
	cwd := t.TempDir()
	registry := newSessionUploadRegistry()
	req := ws.TunnelRequest{SenderUserID: "alice", SenderPub: "browser-key"}
	session := ws.SessionInfo{SessionID: "session-1", UserID: "alice", CWD: cwd}
	upload, err := registry.begin(session, req, "empty.bin", 0)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := registry.finish(upload.id, req.SenderUserID, req.SenderPub)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = finished.root.Close() }()
	if len(finished.data) != 0 {
		t.Fatalf("empty upload contains %d bytes", len(finished.data))
	}
	if err := writeSessionUploadRoot(finished.root, finished.name, finished.data); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(cwd, "empty.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("empty upload size = %d", info.Size())
	}
}

func TestWriteSessionUploadIsPrivateBinarySafeAndNoClobber(t *testing.T) {
	cwd := t.TempDir()
	data := []byte{0, 1, 2, 0xff, 0xfe}
	if err := writeSessionUpload(cwd, "evidence.bin", data); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cwd, "evidence.bin")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("uploaded bytes = %v", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o, want 600", info.Mode().Perm())
		}
	}
	if err := writeSessionUpload(cwd, "evidence.bin", []byte("replace")); err == nil {
		t.Fatal("existing file was overwritten")
	}
	got, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("existing file changed: data=%v err=%v", got, err)
	}
}

func TestWriteSessionUploadDoesNotFollowDestinationSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires extra Windows privileges")
	}
	cwd := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cwd, "evidence.bin")); err != nil {
		t.Fatal(err)
	}
	if err := writeSessionUpload(cwd, "evidence.bin", []byte("replace")); err == nil {
		t.Fatal("destination symlink was followed")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "keep" {
		t.Fatalf("outside target changed: data=%q err=%v", data, err)
	}
}

func TestWriteSessionUploadRootStaysBoundAcrossDirectoryReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("renaming an open directory is platform-specific on Windows")
	}
	parent := t.TempDir()
	original := filepath.Join(parent, "session")
	moved := filepath.Join(parent, "session-moved")
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(original)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeSessionUploadRoot(root, "evidence.txt", []byte("bound")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(original, "evidence.txt")); !os.IsNotExist(err) {
		t.Fatalf("upload escaped into replacement directory: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(moved, "evidence.txt"))
	if err != nil || string(data) != "bound" {
		t.Fatalf("upload missed original session directory: data=%q err=%v", data, err)
	}
}
