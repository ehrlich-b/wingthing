package relay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

// --- ClusterState tests ---

func TestClusterStateSync(t *testing.T) {
	cs := NewClusterState()

	// Edge-A sends 2 wings
	edgeA := []SyncWing{
		{WingID: "conn-a1", NodeID: "machine-a", Info: WingInfo{UserID: "user1", WingID: "wing-a1"}},
		{WingID: "conn-a2", NodeID: "machine-a", Info: WingInfo{UserID: "user2", WingID: "wing-a2"}},
	}
	others, all := cs.Sync("machine-a", edgeA)

	// First sync: no other nodes yet, so others should be empty
	if len(others) != 0 {
		t.Errorf("first sync: others = %d, want 0", len(others))
	}
	if len(all) != 2 {
		t.Errorf("first sync: all = %d, want 2", len(all))
	}

	// Edge-B sends 1 wing
	edgeB := []SyncWing{
		{WingID: "conn-b1", NodeID: "machine-b", Info: WingInfo{UserID: "user3", WingID: "wing-b1"}},
	}
	others, all = cs.Sync("machine-b", edgeB)

	// Edge-B should see edge-A's wings as others
	if len(others) != 2 {
		t.Errorf("edge-B sync: others = %d, want 2", len(others))
	}
	// All should include both edges
	if len(all) != 3 {
		t.Errorf("edge-B sync: all = %d, want 3", len(all))
	}

	// Edge-A syncs again — should see edge-B's wing
	others, _ = cs.Sync("machine-a", edgeA)
	if len(others) != 1 {
		t.Errorf("edge-A re-sync: others = %d, want 1", len(others))
	}
	if others[0].WingID != "conn-b1" {
		t.Errorf("edge-A re-sync: others[0].WingID = %q, want conn-b1", others[0].WingID)
	}
}

func TestClusterStateExpiry(t *testing.T) {
	cs := NewClusterState()

	// Manually inject a stale node
	cs.mu.Lock()
	cs.nodes["stale-machine"] = &nodeEntry{
		wings:    []SyncWing{{WingID: "old-conn", NodeID: "stale-machine"}},
		lastSync: time.Now().Add(-15 * time.Second), // 15s ago, beyond 10s expiry
	}
	cs.mu.Unlock()

	// A fresh sync should expire the stale node
	others, all := cs.Sync("fresh-machine", []SyncWing{
		{WingID: "new-conn", NodeID: "fresh-machine"},
	})

	// Stale node should be expired — only fresh node's wing in all
	if len(all) != 1 {
		t.Errorf("after expiry: all = %d, want 1", len(all))
	}
	if len(others) != 0 {
		t.Errorf("after expiry: others = %d, want 0", len(others))
	}
}

func TestClusterStateAll(t *testing.T) {
	cs := NewClusterState()

	cs.Sync("machine-a", []SyncWing{{WingID: "a1", NodeID: "machine-a"}})
	cs.Sync("machine-b", []SyncWing{{WingID: "b1", NodeID: "machine-b"}, {WingID: "b2", NodeID: "machine-b"}})

	all := cs.All()
	if len(all) != 3 {
		t.Errorf("All() = %d wings, want 3", len(all))
	}
}

func TestClusterStateNodeReplacement(t *testing.T) {
	cs := NewClusterState()

	// Edge-A first reports 2 wings
	cs.Sync("machine-a", []SyncWing{
		{WingID: "a1", NodeID: "machine-a"},
		{WingID: "a2", NodeID: "machine-a"},
	})

	// Edge-A now only has 1 wing (other disconnected)
	_, all := cs.Sync("machine-a", []SyncWing{
		{WingID: "a1", NodeID: "machine-a"},
	})

	if len(all) != 1 {
		t.Errorf("after replacement: all = %d, want 1", len(all))
	}
}

// --- PeerDirectory tests ---

func TestPeerDirectoryReplace(t *testing.T) {
	pd := NewPeerDirectory()

	initial := []*PeerWing{
		{WingID: "conn-a", MachineID: "m1", WingInfo: &WingInfo{UserID: "u1", WingID: "wing-a"}},
		{WingID: "conn-b", MachineID: "m2", WingInfo: &WingInfo{UserID: "u2", WingID: "wing-b"}},
	}
	added, removed := pd.Replace(initial)
	if len(added) != 2 {
		t.Errorf("initial replace: added = %d, want 2", len(added))
	}
	if len(removed) != 0 {
		t.Errorf("initial replace: removed = %d, want 0", len(removed))
	}

	// Replace with one removed, one added
	updated := []*PeerWing{
		{WingID: "conn-a", MachineID: "m1", WingInfo: &WingInfo{UserID: "u1", WingID: "wing-a"}},
		{WingID: "conn-c", MachineID: "m3", WingInfo: &WingInfo{UserID: "u3", WingID: "wing-c"}},
	}
	added, removed = pd.Replace(updated)
	if len(added) != 1 || added[0].WingID != "conn-c" {
		t.Errorf("update replace: added = %v, want [conn-c]", added)
	}
	if len(removed) != 1 || removed[0].WingID != "conn-b" {
		t.Errorf("update replace: removed = %v, want [conn-b]", removed)
	}
}

func TestPeerDirectoryFindByWingID(t *testing.T) {
	pd := NewPeerDirectory()
	pd.Replace([]*PeerWing{
		{WingID: "conn-a", MachineID: "m1", WingInfo: &WingInfo{UserID: "u1", WingID: "stable-wing-id"}},
	})

	// FindWing uses map key (connection UUID)
	pw := pd.FindWing("conn-a")
	if pw == nil {
		t.Fatal("FindWing(conn-a) = nil, want non-nil")
	}

	// FindByWingID searches by stable wing_id
	pw = pd.FindByWingID("stable-wing-id")
	if pw == nil {
		t.Fatal("FindByWingID(stable-wing-id) = nil, want non-nil")
	}
	if pw.WingID != "conn-a" {
		t.Errorf("FindByWingID: WingID = %q, want conn-a", pw.WingID)
	}

	// Not found
	if pd.FindByWingID("nonexistent") != nil {
		t.Error("FindByWingID(nonexistent) should be nil")
	}
}

func TestPeerDirectoryCountForUser(t *testing.T) {
	pd := NewPeerDirectory()
	pd.Replace([]*PeerWing{
		{WingID: "a", WingInfo: &WingInfo{UserID: "u1"}},
		{WingID: "b", WingInfo: &WingInfo{UserID: "u1"}},
		{WingID: "c", WingInfo: &WingInfo{UserID: "u2"}},
	})

	if n := pd.CountForUser("u1"); n != 2 {
		t.Errorf("CountForUser(u1) = %d, want 2", n)
	}
	if n := pd.CountForUser("u2"); n != 1 {
		t.Errorf("CountForUser(u2) = %d, want 1", n)
	}
	if n := pd.CountForUser("nobody"); n != 0 {
		t.Errorf("CountForUser(nobody) = %d, want 0", n)
	}
}

// --- SyncWing ↔ PeerWing conversion tests ---

func TestConnectedToSync(t *testing.T) {
	wing := &ConnectedWing{
		ID:       "conn-uuid-123",
		UserID:   "user-1",
		WingID:   "stable-wing-abc",
		Hostname: "macbook",
		Platform: "darwin",
		Version:  "0.31.0",
		Agents:   []string{"claude", "ollama"},
		Labels:   []string{"dev"},
		Projects: []ws.WingProject{{Name: "myproject", Path: "/home/user/myproject"}},
		OrgID:    "myorg",
	}

	sw := connectedToSync("machine-42", wing)

	if sw.WingID != "conn-uuid-123" {
		t.Errorf("SyncWing.WingID = %q, want conn-uuid-123 (connection UUID)", sw.WingID)
	}
	if sw.NodeID != "machine-42" {
		t.Errorf("SyncWing.NodeID = %q, want machine-42", sw.NodeID)
	}
	if sw.Info.WingID != "stable-wing-abc" {
		t.Errorf("SyncWing.Info.WingID = %q, want stable-wing-abc", sw.Info.WingID)
	}
	if sw.Info.UserID != "user-1" {
		t.Errorf("SyncWing.Info.UserID = %q, want user-1", sw.Info.UserID)
	}
	if sw.Info.OrgID != "myorg" {
		t.Errorf("SyncWing.Info.OrgID = %q, want myorg", sw.Info.OrgID)
	}
}

func TestSyncToPeer(t *testing.T) {
	sw := SyncWing{
		WingID: "conn-uuid-123",
		NodeID: "machine-42",
		Info: WingInfo{
			UserID: "user-1",
			WingID: "stable-wing-abc",
			OrgID:  "myorg",
		},
	}

	pw := syncToPeer(sw)

	if pw.WingID != "conn-uuid-123" {
		t.Errorf("PeerWing.WingID = %q, want conn-uuid-123", pw.WingID)
	}
	if pw.MachineID != "machine-42" {
		t.Errorf("PeerWing.MachineID = %q, want machine-42", pw.MachineID)
	}
	if pw.WingInfo.UserID != "user-1" {
		t.Errorf("PeerWing.WingInfo.UserID = %q, want user-1", pw.WingInfo.UserID)
	}
	if pw.WingInfo.WingID != "stable-wing-abc" {
		t.Errorf("PeerWing.WingInfo.WingID = %q, want stable-wing-abc", pw.WingInfo.WingID)
	}
}

// --- isPrivateIP tests ---

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		// RFC 1918 private ranges
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.0.1", true},
		{"192.168.255.255", true},

		// NOT private (common false-positive cases from old prefix matching)
		{"172.2.0.1", false},
		{"172.3.0.1", false},
		{"172.32.0.1", false},
		{"172.15.255.255", false},
		{"11.0.0.1", false},
		{"192.169.0.1", false},

		// Loopback
		{"127.0.0.1", true},
		{"127.255.255.255", true},
		{"::1", true},

		// IPv6 ULA
		{"fc00::1", true},
		{"fd00::1", true},

		// Fly.io 6PN
		{"fdaa::1", true},
		{"fdaa:0:1:2::3", true},

		// Public IPs
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"2001:db8::1", false},

		// Invalid
		{"not-an-ip", false},
		{"", false},
	}

	for _, tt := range tests {
		got := isPrivateIP(tt.ip)
		if got != tt.want {
			t.Errorf("isPrivateIP(%q) = %v, want %v", tt.ip, got, tt.want)
		}
	}
}

// --- Internal API auth tests ---

func TestInternalAuthFlyPort(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{NodeRole: "login", FlyMachineID: "test-machine"})

	var called bool
	handler := srv.withInternalAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})

	// With Fly-Forwarded-Port header → allowed
	req := httptest.NewRequest("GET", "/internal/status", nil)
	req.Header.Set("Fly-Forwarded-Port", "8080")
	w := httptest.NewRecorder()
	handler(w, req)
	if !called {
		t.Error("expected handler to be called with Fly-Forwarded-Port header")
	}
}

func TestInternalAuthJWTSecret(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{NodeRole: "login", JWTSecret: "test-secret"})

	var called bool
	handler := srv.withInternalAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})

	// With correct X-Internal-Secret header → allowed
	req := httptest.NewRequest("GET", "/internal/status", nil)
	req.Header.Set("X-Internal-Secret", "test-secret")
	w := httptest.NewRecorder()
	handler(w, req)
	if !called {
		t.Error("expected handler to be called with correct secret")
	}
}

func TestInternalAuthPrivateIP(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{NodeRole: "login"})

	var called bool
	handler := srv.withInternalAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})

	// Private IP → allowed
	req := httptest.NewRequest("GET", "/internal/status", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	w := httptest.NewRecorder()
	handler(w, req)
	if !called {
		t.Error("expected handler to be called from private IP")
	}

	// Public IP → forbidden
	called = false
	req = httptest.NewRequest("GET", "/internal/status", nil)
	req.RemoteAddr = "8.8.8.8:1234"
	w = httptest.NewRecorder()
	handler(w, req)
	if called {
		t.Error("expected handler NOT to be called from public IP")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("public IP status = %d, want 403", w.Code)
	}
}

func TestInternalAuthSingleNode(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{}) // no NodeRole

	var called bool
	handler := srv.withInternalAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})

	// Single-node (no NodeRole): internal auth is bypassed
	req := httptest.NewRequest("GET", "/internal/status", nil)
	req.RemoteAddr = "8.8.8.8:1234"
	w := httptest.NewRecorder()
	handler(w, req)
	if !called {
		t.Error("single-node should bypass internal auth")
	}
}

// --- Sync protocol integration test ---

func TestSyncProtocolRoundTrip(t *testing.T) {
	store := testStore(t)
	loginSrv := NewServer(store, ServerConfig{NodeRole: "login", FlyMachineID: "login-machine"})
	loginSrv.Cluster = NewClusterState()
	loginSrv.Peers = NewPeerDirectory()
	loginSrv.Bandwidth = NewBandwidthMeter(64*1024, 1*1024*1024, store.DB())

	ts := httptest.NewServer(loginSrv)
	defer ts.Close()

	// Simulate edge sync: POST /internal/sync
	edgeWings := []SyncWing{
		{WingID: "edge-conn-1", NodeID: "edge-machine-1", Info: WingInfo{
			UserID: "user1", WingID: "wing-001", Hostname: "laptop",
		}},
	}
	syncReq := SyncRequest{
		NodeID:    "edge-machine-1",
		Wings:     edgeWings,
		Bandwidth: map[string]int64{"user1": 1024},
	}
	body, _ := json.Marshal(syncReq)

	resp2, err := http.Post(ts.URL+"/internal/sync", "application/json",
		bytes.NewReader(body))
	if err != nil {
		t.Fatalf("sync request failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != 200 {
		t.Fatalf("sync status = %d, want 200", resp2.StatusCode)
	}

	var syncResp SyncResponse
	json.NewDecoder(resp2.Body).Decode(&syncResp)

	// Login should return its own local wings in the response (none in this test)
	// but the edge wing should now be in the cluster state
	all := loginSrv.Cluster.All()
	if len(all) != 1 {
		t.Errorf("cluster state: %d wings, want 1", len(all))
	}

	// Bandwidth should have been absorbed
	usage := loginSrv.Bandwidth.MonthlyUsage("user1")
	if usage != 1024 {
		t.Errorf("bandwidth usage = %d, want 1024", usage)
	}

	// PeerDirectory should have the edge wing
	peers := loginSrv.Peers.AllWings()
	if len(peers) != 1 {
		t.Errorf("peers: %d wings, want 1", len(peers))
	}
}

// --- Bandwidth drain/requeue tests ---

func TestBandwidthDrainRequeue(t *testing.T) {
	bw := NewBandwidthMeter(64*1024, 1*1024*1024, nil)

	// Simulate usage
	bw.AddUsage("user1", 500)
	bw.AddUsage("user2", 300)

	// Drain
	drained := bw.DrainCounters()
	if drained["user1"] != 500 {
		t.Errorf("drained[user1] = %d, want 500", drained["user1"])
	}
	if drained["user2"] != 300 {
		t.Errorf("drained[user2] = %d, want 300", drained["user2"])
	}

	// After drain, counters should be 0
	if bw.MonthlyUsage("user1") != 0 {
		t.Errorf("after drain: user1 = %d, want 0", bw.MonthlyUsage("user1"))
	}

	// Simulate sync failure: requeue
	for userID, n := range drained {
		bw.AddUsage(userID, n)
	}

	// Counters should be restored
	if bw.MonthlyUsage("user1") != 500 {
		t.Errorf("after requeue: user1 = %d, want 500", bw.MonthlyUsage("user1"))
	}
}

func TestBandwidthExceeded(t *testing.T) {
	bw := NewBandwidthMeter(64*1024, 1*1024*1024, nil)

	// Set exceeded from sync response
	bw.SetExceeded([]string{"banned-user"})

	if !bw.IsExceeded("banned-user") {
		t.Error("expected banned-user to be exceeded")
	}
	if bw.IsExceeded("normal-user") {
		t.Error("expected normal-user NOT to be exceeded")
	}

	// Clear exceeded
	bw.SetExceeded(nil)
	if bw.IsExceeded("banned-user") {
		t.Error("expected banned-user to be cleared after SetExceeded(nil)")
	}
}

func TestBandwidthExceededUsers(t *testing.T) {
	bw := NewBandwidthMeter(64*1024, 1*1024*1024, nil)
	bw.SetTierLookup(func(userID string) string {
		if userID == "pro-user" {
			return "pro"
		}
		return "free"
	})

	// Add usage over the 1 GiB cap for both users
	bw.AddUsage("free-user", freeMonthlyCap+1)
	bw.AddUsage("pro-user", freeMonthlyCap+1)

	exceeded := bw.ExceededUsers()
	found := false
	for _, u := range exceeded {
		if u == "free-user" {
			found = true
		}
		if u == "pro-user" {
			t.Error("pro-user should not be in exceeded list")
		}
	}
	if !found {
		t.Error("free-user should be in exceeded list")
	}
}

// --- fly-replay routing tests ---

func TestReplayToWingEdge(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{FlyMachineID: "login-machine", NodeRole: "login"})
	srv.Peers = NewPeerDirectory()

	// Add a peer wing on edge-machine
	srv.Peers.Replace([]*PeerWing{
		{WingID: "conn-123", MachineID: "edge-machine", WingInfo: &WingInfo{UserID: "u1", WingID: "wing-abc"}},
	})

	// replayToWingEdge with connection UUID
	w := httptest.NewRecorder()
	replayed := srv.replayToWingEdge(w, "conn-123")
	if !replayed {
		t.Fatal("expected replay for peer wing")
	}
	if w.Header().Get("fly-replay") != "instance=edge-machine" {
		t.Errorf("fly-replay = %q, want instance=edge-machine", w.Header().Get("fly-replay"))
	}

	// Local wing should not be replayed
	w = httptest.NewRecorder()
	replayed = srv.replayToWingEdge(w, "nonexistent")
	if replayed {
		t.Error("should not replay nonexistent wing")
	}
}

func TestReplayToWingEdgeByWingID(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{FlyMachineID: "login-machine", NodeRole: "login"})
	srv.Peers = NewPeerDirectory()

	srv.Peers.Replace([]*PeerWing{
		{WingID: "conn-123", MachineID: "edge-machine", WingInfo: &WingInfo{UserID: "u1", WingID: "wing-abc"}},
	})

	// By stable wing_id
	w := httptest.NewRecorder()
	replayed := srv.replayToWingEdgeByWingID(w, "wing-abc")
	if !replayed {
		t.Fatal("expected replay for peer wing by wing_id")
	}
	if w.Header().Get("fly-replay") != "instance=edge-machine" {
		t.Errorf("fly-replay = %q, want instance=edge-machine", w.Header().Get("fly-replay"))
	}
}

func TestReplaySkippedSingleNode(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{}) // no FlyMachineID, no Peers

	w := httptest.NewRecorder()
	if srv.replayToWingEdge(w, "anything") {
		t.Error("single-node should never replay")
	}
	if srv.replayToWingEdgeByWingID(w, "anything") {
		t.Error("single-node should never replay")
	}
}

// --- Wing label cross-node tests ---

func TestWingLabelScopeLocal(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{})

	// Add a local wing
	srv.Wings.Add(&ConnectedWing{
		ID:     "conn-1",
		UserID: "user-1",
		WingID: "wing-abc",
	})

	// Owner can label
	orgID, isOwner := srv.wingLabelScope("user-1", "wing-abc")
	if !isOwner {
		t.Error("owner should be able to label their wing")
	}
	if orgID != "" {
		t.Errorf("orgID = %q, want empty for personal wing", orgID)
	}

	// Non-owner cannot label
	_, isOwner = srv.wingLabelScope("user-2", "wing-abc")
	if isOwner {
		t.Error("non-owner should NOT be able to label")
	}
}

func TestWingLabelScopePeer(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{NodeRole: "login", FlyMachineID: "login-machine"})
	srv.Peers = NewPeerDirectory()

	// Wing on an edge node, not locally connected
	srv.Peers.Replace([]*PeerWing{
		{WingID: "conn-edge-1", MachineID: "edge-m", WingInfo: &WingInfo{
			UserID: "user-1",
			WingID: "wing-xyz",
		}},
	})

	// Owner can label via PeerDirectory
	_, isOwner := srv.wingLabelScope("user-1", "wing-xyz")
	if !isOwner {
		t.Error("owner should be able to label peer wing")
	}

	// Non-owner cannot
	_, isOwner = srv.wingLabelScope("user-2", "wing-xyz")
	if isOwner {
		t.Error("non-owner should NOT be able to label peer wing")
	}
}

// --- WaitForWing tests ---

func TestWaitForWingLocal(t *testing.T) {
	reg := NewWingRegistry()
	reg.Add(&ConnectedWing{ID: "conn-1", UserID: "u1", WingID: "wing-1"})

	machineID, found := WaitForWing(t.Context(), reg, nil, "conn-1", 100*time.Millisecond)
	if !found {
		t.Fatal("expected to find local wing")
	}
	if machineID != "" {
		t.Errorf("machineID = %q, want empty for local wing", machineID)
	}
}

func TestWaitForWingPeer(t *testing.T) {
	reg := NewWingRegistry()
	peers := NewPeerDirectory()
	peers.Replace([]*PeerWing{
		{WingID: "conn-peer", MachineID: "edge-42", WingInfo: &WingInfo{UserID: "u1"}},
	})

	machineID, found := WaitForWing(t.Context(), reg, peers, "conn-peer", 100*time.Millisecond)
	if !found {
		t.Fatal("expected to find peer wing")
	}
	if machineID != "edge-42" {
		t.Errorf("machineID = %q, want edge-42", machineID)
	}
}

func TestWaitForWingTimeout(t *testing.T) {
	reg := NewWingRegistry()
	_, found := WaitForWing(t.Context(), reg, nil, "nonexistent", 100*time.Millisecond)
	if found {
		t.Error("expected timeout, not found")
	}
}

// --- Edge proxy routing test ---

func TestEdgeProxyRouting(t *testing.T) {
	// Edge nodes should serve WebSocket/static/internal/wing-api locally
	// and proxy everything else to login
	store := testStore(t)
	srv := NewServer(store, ServerConfig{NodeRole: "edge", LoginNodeAddr: "http://localhost:9999"})

	var proxyHit bool
	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHit = true
		w.WriteHeader(200)
	})
	srv.SetLoginProxy(proxy)
	srv.SetSessionCache(NewSessionCache())

	localPaths := []string{
		"/ws/wing",
		"/ws/pty",
		"/app/index.html",
		"/assets/main.js",
		"/internal/status",
		"/health",
	}
	for _, path := range localPaths {
		proxyHit = false
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		srv.ServeHTTP(w, r)
		if proxyHit {
			t.Errorf("path %q should be served locally, but was proxied", path)
		}
	}

	proxyPaths := []string{
		"/",
		"/login",
		"/auth/github",
		"/api/app/me",
		"/api/app/usage",
		"/api/orgs/myorg",
		"/api/app/wings/xyz/label",
	}
	for _, path := range proxyPaths {
		proxyHit = false
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		srv.ServeHTTP(w, r)
		if !proxyHit {
			t.Errorf("path %q should be proxied to login, but was served locally", path)
		}
	}
}
