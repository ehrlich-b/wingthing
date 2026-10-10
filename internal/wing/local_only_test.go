package wing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

func localOnlyState(t *testing.T) (state, home, work string) {
	t.Helper()
	home = t.TempDir()
	scratch, err := filepath.Abs("../../.scratch")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	alias, err := os.MkdirTemp(scratch, "l")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(alias) })
	state = filepath.Join(alias, "s")
	work = config.CanonicalProviderPath(home)
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", state)
	return state, home, work
}

type localOnlyRuntime struct {
	service *wingsession.Service
	fixture *sessionTransportFixture
	stop    func()
}

func startLocalOnlyRuntime(t *testing.T, home, work, roost string, options EntryOptions, spawn func(*wingsession.Launch, wingsession.StartOptions) (*egg.Client, error)) *localOnlyRuntime {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan *wingsession.Service, 1)
	done := make(chan error, 1)
	f := &sessionTransportFixture{t: t, home: home, work: work, eggs: map[string]*sessionEggFixture{}}
	options.Version = "test"
	options.SetSessionService = func(service *wingsession.Service) {
		f.cfg = service.Config
		f.sessions = service
		if spawn == nil {
			service.Spawn = f.spawn
		} else {
			service.Spawn = spawn
		}
		ready <- service
	}
	go func() {
		done <- RunWingWithContext(options, ctx, nil, roost, "", "auto", "", "", nil, work, false, false, false, false, false, nil)
	}()
	var service *wingsession.Service
	select {
	case service = <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("wing failed before local readiness: %v", err)
	case <-t.Context().Done():
		cancel()
		t.Fatal(t.Context().Err())
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("wing shutdown: %v", err)
		}
	}
	t.Cleanup(stop)
	return &localOnlyRuntime{service: service, fixture: f, stop: stop}
}

func localOnlyClient(t *testing.T, state string, hello controlsocket.Hello) *controlsocket.Client {
	t.Helper()
	client, err := controlsocket.Dial(t.Context(), state, hello)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func localOnlyCall(t *testing.T, client *controlsocket.Client, tool string, arguments map[string]any) map[string]any {
	t.Helper()
	wire, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	result, denied, err := client.Call(t.Context(), tool, wire)
	if err != nil || denied {
		t.Fatalf("%s: %v denied=%v error=%v", tool, result, denied, err)
	}
	return result
}

func TestLocalOnlyWingBootsWithoutTokens(t *testing.T) {
	state, home, work := localOnlyState(t)
	// A configured relay is deliberately unusable, and no token exists.
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveWingConfig(state, &config.WingConfig{Roost: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	runtime := startLocalOnlyRuntime(t, home, work, "", EntryOptions{LocalOnly: true, RelayState: func(string, error) { t.Error("local-only wing attempted relay connectivity") }}, nil)
	client := localOnlyClient(t, state, controlsocket.Hello{})
	owner, err := config.EnsureLocalOwner(state, "")
	if err != nil {
		t.Fatal(err)
	}
	if client.Welcome.Principal != wingsession.UserPrincipal(owner.ID) {
		t.Fatalf("socket principal did not resolve from local owner: %+v", client.Welcome)
	}
	localOnlyCall(t, client, "wingthing_capabilities", map[string]any{})
	directory := localOnlyCall(t, client, "wing_list", map[string]any{})
	if directory["count"] != float64(1) || directory["wings"].([]any)[0].(map[string]any)["wing_id"] != runtime.service.Config.WingID {
		t.Fatalf("local wing directory: %v", directory)
	}
	for _, spec := range []struct {
		tool string
		args map[string]any
	}{
		{"terminal_start", map[string]any{"cwd": work, "label": "shell", "command": []string{"/bin/sh"}}},
		{"agent_start", map[string]any{"agent": "claude", "cwd": work, "label": "fake-agent"}},
	} {
		result := localOnlyCall(t, client, spec.tool, spec.args)
		id := result["session"].(string)
		fixture := runtime.fixture.egg(id)
		select {
		case attachment := <-fixture.attached:
			if attachment == nil || !attachment.ReadOnly || attachment.Claim {
				t.Fatal("local registration claimed the egg's writer")
			}
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
		if eggclient.ReadEggOwner(filepath.Join(state, "eggs", id)) != owner.ID {
			t.Fatal("egg lost its local owner")
		}
		listed := localOnlyCall(t, client, "terminal_list", map[string]any{})
		if len(listed["sessions"].([]any)) != 1 {
			t.Fatalf("locally registered session missing: %v", listed)
		}
		ec, err := egg.Dial(filepath.Join(state, "eggs", id, "egg.sock"), filepath.Join(state, "eggs", id, "egg.token"))
		if err != nil {
			t.Fatal(err)
		}
		stream, err := ec.AttachSessionWithOptions(t.Context(), id, egg.AttachOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Recv(); err != nil {
			t.Fatalf("egg no longer attachable: %v", err)
		}
		_ = ec.Close()
		foreign := localOnlyClient(t, state, controlsocket.Hello{Client: "another-client"})
		if listed := localOnlyCall(t, foreign, "terminal_list", map[string]any{}); len(listed["sessions"].([]any)) != 0 {
			t.Fatal("named client inherited default owner's egg")
		}
		localOnlyCall(t, client, "terminal_stop", map[string]any{"session": id})
		<-fixture.killed
	}
	for _, store := range []*auth.TokenStore{auth.NewTokenStore(state), auth.NewLocalTokenStore(state)} {
		if token, err := store.Load(); err != nil || token != nil {
			t.Fatalf("local-only boot created or required relay token: %+v %v", token, err)
		}
	}
}

func TestLocalOnlyWingSurvivesRelay401(t *testing.T) {
	state, home, work := localOnlyState(t)
	// Add an optional relay to a wing that originally booted without an account.
	first := startLocalOnlyRuntime(t, home, work, "", EntryOptions{LocalOnly: true}, nil)
	first.stop()
	if err := auth.NewTokenStore(state).Save(&auth.DeviceToken{Token: "rejected", UserID: "relay-user"}); err != nil {
		t.Fatal(err)
	}
	rejected := make(chan struct{})
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The local socket must already answer before the first relay request.
		client, err := controlsocket.Dial(r.Context(), state, controlsocket.Hello{})
		if err != nil {
			t.Error(err)
		} else {
			_ = client.Close()
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer relay.Close()
	runtime := startLocalOnlyRuntime(t, home, work, relay.URL, EntryOptions{RelayState: func(state string, err error) {
		if state == "auth_failed" {
			close(rejected)
		}
	}}, nil)
	select {
	case <-rejected:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	client := localOnlyClient(t, state, controlsocket.Hello{})
	localOnlyCall(t, client, "wingthing_capabilities", map[string]any{})
	localOnlyCall(t, client, "wing_list", map[string]any{})
	id := localOnlyCall(t, client, "terminal_start", map[string]any{"cwd": work, "command": []string{"/bin/sh"}})["session"].(string)
	localOnlyCall(t, client, "terminal_stop", map[string]any{"session": id})
	<-runtime.fixture.egg(id).killed
}

func TestLocalOnlyWingRestartReclaimsEggs(t *testing.T) {
	state, home, work := localOnlyState(t)
	first := startLocalOnlyRuntime(t, home, work, "", EntryOptions{LocalOnly: true}, nil)
	client := localOnlyClient(t, state, controlsocket.Hello{})
	id := localOnlyCall(t, client, "agent_start", map[string]any{"agent": "claude", "cwd": work})["session"].(string)
	fixture := first.fixture.egg(id)
	<-fixture.attached // first wing's observer is established
	firstOwner := eggclient.ReadEggOwner(filepath.Join(state, "eggs", id))
	first.stop()
	select {
	case <-fixture.killed:
		t.Fatal("wing shutdown killed its egg")
	default:
	}
	second := startLocalOnlyRuntime(t, home, work, "", EntryOptions{LocalOnly: true}, nil)
	<-fixture.attached // a fresh wing observer, established without relay reconnect
	reconnected := localOnlyClient(t, state, controlsocket.Hello{})
	if client.Welcome.WingID != reconnected.Welcome.WingID || client.Welcome.Principal != reconnected.Welcome.Principal {
		t.Fatal("restart changed wing or local principal identity")
	}
	if owner := eggclient.ReadEggOwner(filepath.Join(state, "eggs", id)); owner != firstOwner {
		t.Fatal("restart changed egg ownership")
	}
	listed := localOnlyCall(t, reconnected, "terminal_list", map[string]any{})
	if len(listed["sessions"].([]any)) != 1 || listed["sessions"].([]any)[0].(map[string]any)["id"] != id {
		t.Fatalf("reclaimed egg not listed: %v", listed)
	}
	if _, ok := sessionStates.Load(id); !ok {
		t.Fatal("wing did not reclaim egg idle tracking")
	}
	localOnlyCall(t, reconnected, "terminal_read", map[string]any{"session": id})
	localOnlyCall(t, reconnected, "terminal_stop", map[string]any{"session": id})
	<-fixture.killed
	second.stop()
}

func TestWingLocalAPISurvivesUnreachableRelay(t *testing.T) {
	state, home, work := localOnlyState(t)
	if err := auth.NewTokenStore(state).Save(&auth.DeviceToken{Token: "fixture", UserID: "fixture-owner"}); err != nil {
		t.Fatal(err)
	}
	relay := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := relay.URL
	relay.Close()
	unreachable := make(chan struct{})
	var observed sync.Once
	startLocalOnlyRuntime(t, home, work, url, EntryOptions{RelayState: func(state string, err error) {
		if state == "disconnected" && err != nil {
			observed.Do(func() { close(unreachable) })
		}
	}}, nil)
	<-unreachable
	client := localOnlyClient(t, state, controlsocket.Hello{})
	localOnlyCall(t, client, "wingthing_capabilities", map[string]any{})
	localOnlyCall(t, client, "wing_list", map[string]any{})
}

func TestRelayEnrollmentPreservesEggOwnership(t *testing.T) {
	state, home, work := localOnlyState(t)
	first := startLocalOnlyRuntime(t, home, work, "", EntryOptions{LocalOnly: true}, nil)
	client := localOnlyClient(t, state, controlsocket.Hello{})
	id := localOnlyCall(t, client, "agent_start", map[string]any{"agent": "claude", "cwd": work})["session"].(string)
	fixture := first.fixture.egg(id)
	<-fixture.attached
	ownerBefore := eggclient.ReadEggOwner(filepath.Join(state, "eggs", id))
	principalBefore := eggclient.ReadSessionPrincipal(filepath.Join(state, "eggs", id))
	first.stop()
	bound, err := config.BindLocalOwnerRelay(state, "https://relay.example", "account-owner")
	if err != nil {
		t.Fatal(err)
	}
	second := startLocalOnlyRuntime(t, home, work, "", EntryOptions{LocalOnly: true}, nil)
	<-fixture.attached
	// Relay and local adapters normalize to the same durable owner. The egg's
	// owner and logical principal files are never rewritten during enrollment.
	authority := wingsession.Authority{UserID: bound.OwnerForRelay("wss://relay.example/", "account-owner"), Principal: principalBefore}
	if _, err := second.service.Resolve(t.Context(), authority, id, false); err != nil {
		t.Fatalf("enrolled owner lost existing egg: %v", err)
	}
	if bound.ID != ownerBefore || eggclient.ReadEggOwner(filepath.Join(state, "eggs", id)) != ownerBefore {
		t.Fatal("enrollment replaced existing ownership")
	}
	foreign := authority
	foreign.Principal = "other-client"
	if _, err := second.service.Resolve(t.Context(), foreign, id, false); err == nil {
		t.Fatal("owner binding bypassed named-client isolation")
	}
	reconnected := localOnlyClient(t, state, controlsocket.Hello{})
	localOnlyCall(t, reconnected, "terminal_stop", map[string]any{"session": id})
	<-fixture.killed
}
