package skill

import (
	"testing"
)

func TestStateDefaultEnabled(t *testing.T) {
	s := &State{}
	if !s.IsEnabled("compress") {
		t.Error("expected compress enabled by default")
	}
}

func TestStateDisableEnable(t *testing.T) {
	s := &State{}

	s.Disable("compress")
	if s.IsEnabled("compress") {
		t.Error("expected compress disabled")
	}

	// Disable again (idempotent)
	s.Disable("compress")
	if len(s.Disabled) != 1 {
		t.Errorf("disabled len = %d, want 1", len(s.Disabled))
	}

	s.Enable("compress")
	if !s.IsEnabled("compress") {
		t.Error("expected compress re-enabled")
	}
	if len(s.Disabled) != 0 {
		t.Errorf("disabled len = %d, want 0", len(s.Disabled))
	}
}

func TestStateSaveLoad(t *testing.T) {
	dir := t.TempDir()

	s := &State{}
	s.Disable("scorer")
	s.Disable("compress")
	if err := s.Save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.IsEnabled("scorer") {
		t.Error("expected scorer disabled after load")
	}
	if loaded.IsEnabled("compress") {
		t.Error("expected compress disabled after load")
	}
	if !loaded.IsEnabled("other") {
		t.Error("expected other enabled")
	}
}

func TestStateLoadMissing(t *testing.T) {
	dir := t.TempDir()
	s, err := LoadState(dir)
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if len(s.Disabled) != 0 {
		t.Errorf("expected empty disabled list")
	}
}
