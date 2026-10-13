package wing

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestBrowserFleetLedgerConcurrentConsumeAndReopen(t *testing.T) {
	dir := t.TempDir()
	ledger, err := openBrowserAttachmentLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Consume(t.Context(), "owner-tab", "opaque-attachment", 1, true); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ledger.Consume(context.Background(), "owner-tab", "opaque-attachment", 2, false) == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("same sequence consumed %d times", accepted.Load())
	}
	ledger.Close()
	ledger, err = openBrowserAttachmentLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if err := ledger.Consume(t.Context(), "owner-tab", "opaque-attachment", 1, true); err == nil {
		t.Fatal("takeover replayed after reopen")
	}
	if err := ledger.Consume(t.Context(), "owner-tab", "opaque-attachment", 2, false); err == nil {
		t.Fatal("input replayed after reopen")
	}
	if err := ledger.Consume(t.Context(), "other-tab", "opaque-attachment", 3, false); err == nil {
		t.Fatal("foreign tab inherited sequence")
	}
	if err := ledger.Consume(t.Context(), "owner-tab", "opaque-attachment", 3, false); err != nil {
		t.Fatal(err)
	}
}
func TestBrowserFleetLedgerRefusesUnsafeDirectory(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, "browser-attachment-receipts-v1")); err != nil {
		t.Fatal(err)
	}
	if ledger, err := openBrowserAttachmentLedger(dir); err == nil {
		ledger.Close()
		t.Fatal("symlink receipt directory accepted")
	}
}

func TestBrowserFleetLedgerShortWritePreservesConsumedFence(t *testing.T) {
	ledger, err := openBrowserAttachmentLedger(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if err := ledger.Consume(t.Context(), "owner-tab", "opaque-attachment", 1, true); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Consume(t.Context(), "owner-tab", "opaque-attachment", 9, false); err != nil {
		t.Fatal(err)
	}
	ledger.write = func(f *os.File, _ uint64) error {
		if _, err := f.Write([]byte("1")); err != nil {
			return err
		}
		return io.ErrShortWrite
	}
	if err := ledger.Consume(t.Context(), "owner-tab", "opaque-attachment", 10, false); err == nil {
		t.Fatal("short receipt write acknowledged")
	}
	ledger.write = nil
	// The active attachment's old sequence 9 remains fenced after the failed
	// update to 10, even though the temporary receipt contained a valid 1.
	if err := ledger.Consume(t.Context(), "owner-tab", "opaque-attachment", 2, false); err == nil {
		t.Fatal("failed replacement lowered replay fence")
	}
	if err := ledger.Consume(t.Context(), "owner-tab", "opaque-attachment", 9, false); err == nil {
		t.Fatal("consumed input replayed after storage failure")
	}
	if err := ledger.Consume(t.Context(), "owner-tab", "opaque-attachment", 10, false); err != nil {
		t.Fatal(err)
	}
}
