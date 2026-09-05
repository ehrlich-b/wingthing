//go:build integration

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/reviewjob"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
)

func TestReviewJobDirectTransportReconnectIsolation(t *testing.T) {
	s, _ := reviewServerFixture(t)
	dir := filepath.Join(s.cfg.Dir, "review-jobs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	id := "j-" + strings.Repeat("a", 32)
	job := reviewjob.Job{ID: id, Owner: s.principal, Status: "succeeded", Stage: "terminal", CreatedAt: time.Now(), Rounds: []reviewjob.Round{{Number: 0, Candidate: &reviewjob.Candidate{BaseCommit: strings.Repeat("b", 40), Patch: "exact-patch", SHA256: "digest"}, Review: &reviewjob.Review{Verdict: "pass", PatchSHA256: "digest", Summary: "independent review"}}}}
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	wingCfg := &config.WingConfig{}
	client, ctx := connectDirectMCPTestClient(t, s.cfg, wingCfg, t.TempDir(), false, webrtcpkg.PeerIdentity{UserID: "alice", Email: "alice@example.test", OrgRole: "owner"})
	args, _ := json.Marshal(map[string]any{"job_id": id, "artifact": "patch"})
	result, bad, err := client.Call(ctx, "review_job_result", args)
	if err != nil || bad || result["patch"] != "exact-patch" {
		t.Fatalf("first retrieval: %v %v %v", result, bad, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	reconnected, ctx := connectDirectMCPTestClient(t, s.cfg, wingCfg, t.TempDir(), false, webrtcpkg.PeerIdentity{UserID: "alice", Email: "alice@example.test", OrgRole: "owner"})
	result, bad, err = reconnected.Call(ctx, "review_job_result", args)
	if err != nil || bad || result["patch"] != "exact-patch" {
		t.Fatalf("reconnect retrieval: %v %v %v", result, bad, err)
	}
	other, otherCtx := connectDirectMCPTestClient(t, s.cfg, wingCfg, t.TempDir(), false, webrtcpkg.PeerIdentity{UserID: "bob", Email: "bob@example.test", OrgRole: "owner"})
	result, bad, err = other.Call(otherCtx, "review_job_result", args)
	if err == nil && !bad {
		t.Fatalf("cross-owner leak: %v", result)
	}
	shared, sharedCtx := connectDirectMCPTestClient(t, s.cfg, wingCfg, t.TempDir(), true, webrtcpkg.PeerIdentity{UserID: "alice", Email: "alice@example.test", OrgRole: "owner"})
	result, bad, err = shared.Call(sharedCtx, "review_job_result", args)
	if err == nil && !bad {
		t.Fatalf("shared-host delegation enabled: %v", result)
	}
}
