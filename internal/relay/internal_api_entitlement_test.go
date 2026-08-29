package relay

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeEntitlementRows struct {
	next       bool
	scanErr    error
	iterateErr error
}

func (r *fakeEntitlementRows) Next() bool {
	if r.next {
		r.next = false
		return true
	}
	return false
}

func (r *fakeEntitlementRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	*(dest[0].(*string)) = "user"
	*(dest[1].(*string)) = "free"
	return nil
}

func (r *fakeEntitlementRows) Err() error { return r.iterateErr }

func TestScanEntitlementRowsFailsInsteadOfPublishingPartialPolicy(t *testing.T) {
	for name, rows := range map[string]*fakeEntitlementRows{
		"scan":      {next: true, scanErr: errors.New("scan failed")},
		"iteration": {iterateErr: errors.New("iteration failed")},
	} {
		t.Run(name, func(t *testing.T) {
			entries, err := scanEntitlementRows(rows)
			if err == nil || entries != nil || !strings.Contains(err.Error(), "failed") {
				t.Fatalf("entries=%#v err=%v", entries, err)
			}
		})
	}
}

func TestInternalEntitlementsClosesCursorBeforePolicyQueries(t *testing.T) {
	store, err := OpenRelay(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser("one-connection-user"); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	server := &Server{Store: store, Config: ServerConfig{RelayPolicy: RelayPolicyDirectFree}}
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.handleInternalEntitlements(recorder, httptest.NewRequest("GET", "/internal/entitlements", nil))
		close(done)
	}()
	select {
	case <-done:
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("entitlement handler deadlocked on the one-connection in-memory store")
	}
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "one-connection-user") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
