package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLocalOwnerPersistsAcrossBoots(t *testing.T) {
	dir := t.TempDir()
	start := make(chan struct{})
	owners := make(chan *LocalOwner, 8)
	errors := make(chan error, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			<-start
			owner, err := EnsureLocalOwner(dir, "")
			owners <- owner
			errors <- err
		})
	}
	close(start)
	workers.Wait()
	first := <-owners
	for range 8 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	for range 7 {
		if owner := <-owners; owner.ID != first.ID {
			t.Fatalf("concurrent boots chose different owners: %s, %s", first.ID, owner.ID)
		}
	}
	if first.ID == "" {
		t.Fatal("empty owner")
	}
	for _, name := range []string{"local-owner.yaml", ".local-owner.lock"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("owner file permissions: %s %+v %v", name, info, err)
		}
	}
	for range 2 {
		owner, err := EnsureLocalOwner(dir, "later-account")
		if err != nil || owner.ID != first.ID {
			t.Fatalf("restart replaced owner: %+v %v", owner, err)
		}
	}
}

func TestRelayEnrollmentPreservesLocalOwnership(t *testing.T) {
	dir := t.TempDir()
	before, err := EnsureLocalOwner(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := before.OwnerForRelay("https://relay.example", "account-owner"); got != "account-owner" {
		t.Fatal("unbound relay account inherited local ownership")
	}
	after, err := BindLocalOwnerRelay(dir, "wss://relay.example/", "account-owner")
	if err != nil || after.ID != before.ID {
		t.Fatalf("enrollment replaced local owner: %+v %v", after, err)
	}
	reloaded, err := EnsureLocalOwner(dir, "account-owner")
	if err != nil || reloaded.ID != before.ID || reloaded.OwnerForRelay("https://relay.example", "account-owner") != before.ID {
		t.Fatalf("enrollment binding lost at restart: %+v %v", reloaded, err)
	}
	for _, pair := range [][2]string{{"https://other.example", "account-owner"}, {"https://relay.example", "other-user"}} {
		if got := reloaded.OwnerForRelay(pair[0], pair[1]); got != pair[1] {
			t.Fatal("binding granted another relay or user the local owner")
		}
	}
	if _, err := BindLocalOwnerRelay(dir, "https://relay.example", "other-user"); err == nil {
		t.Fatal("silently replaced explicit binding")
	}
}

func TestLocalOwnerRejectsCorruptOrPublicIdentity(t *testing.T) {
	for _, data := range []string{"id: ''\n", "id: one\nunexpected: field\n"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "local-owner.yaml"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureLocalOwner(dir, ""); err == nil {
			t.Fatal("replaced invalid identity")
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "local-owner.yaml")
	if err := os.WriteFile(path, []byte("id: public\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureLocalOwner(dir, ""); err == nil {
		t.Fatal("accepted publicly readable owner file")
	}
}
