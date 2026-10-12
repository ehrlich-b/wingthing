package dashboard

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestAttentionDurableViewerRoutesAndAcknowledgement(t *testing.T) {
	dir := t.TempDir()
	open := func() *state {
		db, err := openAttentionStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return &state{localDir: dir, attentionStore: db,
			machines: map[string]machine{"local": {}, "forge": {}},
			remotes:  map[string]config.Remote{"forge": {SSHTarget: "original", WingthingDir: "/remote"}}}
	}
	first := open()
	for _, name := range []string{"local", "forge"} {
		attentionApply(first, name, attentionSession("working", 1, 0))
		attentionApply(first, name, attentionSession("completed", 2, 2))
	}
	first.acknowledge(first.rows()[0])
	second := open()
	for _, name := range []string{"local", "forge"} {
		attentionApply(second, name, attentionSession("completed", 2, 2))
	}
	if second.rows()[0].attention != "none" || second.rows()[1].attention != "unread" {
		t.Fatal("reopen lost route-specific read cursors")
	}
	shown := second.rows()[1]
	attentionApply(first, "forge", attentionSession("completed", 3, 3))
	second.acknowledge(shown)
	third := open()
	attentionApply(third, "forge", attentionSession("completed", 3, 3))
	if third.rows()[0].attention != "unread" {
		t.Fatal("older viewer erased a newer unread completion")
	}
	third.acknowledge(third.rows()[0])
	fourth := open()
	attentionApply(fourth, "forge", attentionSession("completed", 3, 3))
	if fourth.rows()[0].attention != "none" {
		t.Fatal("acknowledgement did not survive reopen")
	}
	old := first.rows()[1]
	fourth.remotes["forge"] = config.Remote{SSHTarget: "replacement", WingthingDir: "/remote"}
	attentionApply(fourth, "forge", attentionSession("working", 1, 0))
	fourth.remotes["forge"] = first.remotes["forge"]
	attentionApply(fourth, "forge", attentionSession("working", 1, 0))
	attentionApply(fourth, "forge", attentionSession("completed", 2, 2))
	// An old captured row may remain in a different generation's records.
	fourth.attention[old.identity] = first.attention[old.identity]
	fourth.acknowledge(old)
	fifth := open()
	attentionApply(fifth, "forge", attentionSession("completed", 2, 2))
	if fifth.rows()[0].attention != "unread" {
		t.Fatal("old profile generation acknowledged a replacement across reopen")
	}
	fourth.remotes["forge"] = config.Remote{SSHTarget: "replacement", WingthingDir: "/remote"}
	attentionApply(fourth, "forge", attentionSession("completed", 4, 4))
	if fourth.rows()[0].attention != "none" {
		t.Fatal("replacement inherited another route's unread state")
	}
	info, err := os.Stat(filepath.Join(dir, "navigator-attention.db"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("read cursors are not owner-only", info, err)
	}
}

func TestAttentionStoreCorruptionIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "navigator-attention.db"), []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	if db, err := openAttentionStore(dir); err == nil {
		db.Close()
		t.Fatal("corrupt viewer state was silently discarded")
	}
}
