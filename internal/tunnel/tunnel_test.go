package tunnel

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

func appendAuditVarint(target []byte, value uint64) []byte {
	var encoded [10]byte
	count := binary.PutUvarint(encoded[:], value)
	return append(target, encoded[:count]...)
}

func TestStreamPTYAuditParsesV2Incrementally(t *testing.T) {
	recording := []byte("WTA2")
	recording = appendAuditVarint(recording, 80)
	recording = appendAuditVarint(recording, 24)
	recording = appendAuditVarint(recording, 125)
	recording = appendAuditVarint(recording, 0)
	recording = appendAuditVarint(recording, 5)
	recording = append(recording, "hello"...)
	resize := appendAuditVarint(nil, 100)
	resize = appendAuditVarint(resize, 30)
	recording = appendAuditVarint(recording, 75)
	recording = appendAuditVarint(recording, 1)
	recording = appendAuditVarint(recording, uint64(len(resize)))
	recording = append(recording, resize...)

	var chunks []string
	err := streamPTYAudit(bytes.NewReader(recording), 120, 40, func(chunk []byte) error {
		chunks = append(chunks, string(append([]byte(nil), chunk...)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`{"version":2,"width":80,"height":24}`,
		`[0.125,"o","aGVsbG8="]`,
		`[0.200,"r","100x30"]`,
	}
	if len(chunks) != len(want) {
		t.Fatalf("PTY audit chunks = %#v, want %#v", chunks, want)
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Fatalf("PTY audit chunk %d = %q, want %q", i, chunks[i], want[i])
		}
	}
}

func TestStreamPTYAuditRejectsOversizedFrameBeforeReadingIt(t *testing.T) {
	recording := appendAuditVarint(nil, 0)
	recording = appendAuditVarint(recording, maxAuditFrameBytes+1)
	err := streamPTYAudit(bytes.NewReader(recording), 80, 24, func([]byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("oversized PTY audit error = %v", err)
	}
}

func TestStreamPTYAuditToleratesIncompleteLiveFrame(t *testing.T) {
	recording := []byte("WTA2")
	recording = appendAuditVarint(recording, 80)
	recording = appendAuditVarint(recording, 24)
	recording = appendAuditVarint(recording, 0)
	recording = appendAuditVarint(recording, 0)
	recording = appendAuditVarint(recording, 10)
	recording = append(recording, "partial"...)
	var chunks int
	if err := streamPTYAudit(bytes.NewReader(recording), 120, 40, func([]byte) error {
		chunks++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if chunks != 1 {
		t.Fatalf("incomplete audit emitted %d chunks, want header only", chunks)
	}
}

func TestTunnelKeyCacheIsBoundedAndEvictsOldestIdentity(t *testing.T) {
	cache := newTunnelKeyCache(2)
	cache.Put("first", nil)
	cache.Put("second", nil)
	cache.Put("second", nil)
	cache.Put("third", nil)

	if cache.Len() != 2 {
		t.Fatalf("cache size = %d", cache.Len())
	}
	if _, ok := cache.Get("first"); ok {
		t.Fatal("oldest sender identity was not evicted")
	}
	if _, ok := cache.Get("second"); !ok {
		t.Fatal("updated sender identity was unexpectedly evicted")
	}
	if _, ok := cache.Get("third"); !ok {
		t.Fatal("new sender identity is missing")
	}
}

func TestTunnelKeyCacheConcurrentInsertionsStayBounded(t *testing.T) {
	cache := newTunnelKeyCache(8)
	var group sync.WaitGroup
	for i := 0; i < 128; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			cache.Put(strconv.Itoa(index), nil)
		}(i)
	}
	group.Wait()
	if cache.Len() != 8 {
		t.Fatalf("cache size = %d", cache.Len())
	}
}

func TestSessionHistoryFiltersBeforePagination(t *testing.T) {
	workspace := t.TempDir()
	member := ws.TunnelRequest{SenderUserID: "alice", SenderOrgRole: "member"}
	sessions := []pastSessionInfo{
		{SessionID: "hidden-newest", UserID: "mallory", CWD: workspace},
		{SessionID: "alice-first", UserID: "alice", CWD: workspace},
		{SessionID: "alice-second", UserID: "alice", CWD: workspace},
	}
	visible := filterSessionsHistoryForRequest(member, sessions, []string{workspace})
	page, total := paginateSessionsHistory(visible, 0, 1)
	if total != 2 || len(page) != 1 || page[0].SessionID != "alice-first" {
		t.Fatalf("filtered history page = %#v total=%d", page, total)
	}
}

func TestSessionHistoryPaginationBoundsUntrustedIntegers(t *testing.T) {
	sessions := make([]pastSessionInfo, maxSessionsHistoryLimit+10)
	page, total := paginateSessionsHistory(sessions, -1, int(^uint(0)>>1))
	if total != len(sessions) || len(page) != maxSessionsHistoryLimit {
		t.Fatalf("bounded history page length=%d total=%d", len(page), total)
	}
	page, total = paginateSessionsHistory(sessions, int(^uint(0)>>1), 1)
	if total != len(sessions) || len(page) != 0 {
		t.Fatalf("oversized history offset returned length=%d total=%d", len(page), total)
	}
}
