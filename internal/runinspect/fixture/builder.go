//go:build runinspectfixture

// Package fixture builds disposable durable snapshots with the committed
// serializers. It is excluded from ordinary builds and never reads live state.
package fixture

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/runinspect"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

var CapturedAt = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

const PrivateOutput = "SERIALIZER-OUTPUT-Ω\n\x1b\x00\u202e"
const PrivateCheckpoint = "SERIALIZER-PRIVATE-CHECKPOINT"
const PrivateArchive = "SERIALIZER-PRIVATE-ARCHIVE"
const PrivatePrompt = "SERIALIZER-PRIVATE-PROMPT"

// Builder owns only a fresh, explicitly named scratch destination.
type Builder struct {
	Path, Workspace string
	Store           *store.Store
	t               testing.TB
	sealed          bool
}

// Open refuses existing destinations (including empty directories). Parent
// directories must already exist, be unaliased, and be under an explicit
// .scratch component. There is no collector or ambient-state reader.
func Open(destination string) (*Builder, error) {
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return nil, errors.New("new absolute scratch destination required")
	}
	parent := filepath.Dir(destination)
	scratch := false
	for _, part := range strings.Split(filepath.ToSlash(parent), "/") {
		if part == ".scratch" {
			scratch = true
		}
	}
	canonical, err := filepath.EvalSymlinks(parent)
	if !scratch || err != nil || canonical != parent {
		return nil, errors.New("existing unaliased scratch parent required")
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return nil, err
	}
	db, err := store.Open(filepath.Join(destination, "wt.db"))
	if err != nil {
		return nil, err
	}
	return &Builder{Path: destination, Workspace: parent, Store: db}, nil
}

func New(t testing.TB, destination string) *Builder {
	t.Helper()
	b, err := Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	b.t = t
	t.Cleanup(func() {
		if b.Store != nil {
			_ = b.Store.Close()
			b.Store = nil
		}
	})
	return b
}
func (b *Builder) must(err error) {
	b.t.Helper()
	if err != nil {
		b.t.Fatal(err)
	}
}
func (b *Builder) SQL(query string, args ...any) {
	b.t.Helper()
	if b.sealed {
		b.t.Fatal("fixture is sealed")
	}
	_, err := b.Store.DB().Exec(query, args...)
	b.must(err)
}
func (b *Builder) Write(relative string, data []byte) {
	b.t.Helper()
	if b.sealed || !fs.ValidPath(relative) || strings.Contains(relative, "\\") {
		b.t.Fatal("invalid fixture write")
	}
	path := filepath.Join(b.Path, filepath.FromSlash(relative))
	b.must(os.MkdirAll(filepath.Dir(path), 0700))
	b.must(os.WriteFile(path, data, 0600))
}

func Result(id, session, status, text string) egg.RunTurnResult {
	result := egg.RunTurnResult{RunID: id, SessionID: session, Status: status, Text: text, Deadline: CapturedAt.Add(time.Minute)}
	if status != "pending" {
		result.StartedAt = CapturedAt.Add(-time.Minute)
		result.ProviderSessionID = "native-" + session
		result.TurnID = "turn-" + id
	}
	if result.Terminal() {
		result.EndedAt = CapturedAt
	}
	switch status {
	case "timeout":
		result.FailureKind = agent.Timeout
	case "stopped":
		result.FailureKind = agent.Stopped
	case "failed":
		result.FailureKind = agent.ProviderExit
	}
	return result
}

func (b *Builder) Wing(id, session, parent, phase, status, text string) {
	b.t.Helper()
	result := Result(id, session, status, text)
	run := wingsession.Run{ID: id, SessionID: session, SpecHash: "wing-spec-" + id, Prompt: PrivatePrompt, OriginalPrompt: PrivatePrompt, Agent: "claude", CWD: "/fixture", CreatedAt: CapturedAt.Add(-time.Minute), ParentID: parent, Phase: phase, TimeoutSeconds: 120, Launch: wingsession.RunLaunch{Authority: wingsession.Authority{Principal: "owner", UserID: "owner"}}, Result: result}
	wire, err := json.Marshal(run)
	b.must(err)
	task := &store.Task{ID: id, Type: "agent_run", What: run.Prompt, Agent: run.Agent, CWD: run.CWD, Principal: "owner", RunAt: run.CreatedAt, CreatedAt: run.CreatedAt, Status: status, TimeoutSeconds: 120}
	if parent != "" {
		task.ParentID = &parent
	}
	row, fresh, err := b.Store.AdmitAgentRun(&store.AgentRun{ID: id, SessionID: session, Principal: "owner", SpecHash: run.SpecHash, Record: wire}, task)
	b.must(err)
	if !fresh {
		b.t.Fatal("fixture admission was not fresh")
	}
	if result.Terminal() {
		task.Output = &result.Text
		task.FinishedAt = &result.EndedAt
	}
	b.must(b.Store.SaveAgentRun(row, task, status, ""))
}
func (b *Builder) Egg(id, session, status, text string) {
	b.t.Helper()
	dir := filepath.Join(b.Path, "eggs", session)
	b.must(os.MkdirAll(dir, 0700))
	req := egg.RunTurnRequest{RunID: id, Prompt: PrivatePrompt, Deadline: CapturedAt.Add(time.Minute)}
	wire, err := json.Marshal(req)
	b.must(err)
	b.must(egg.FixturePersistRunTurn(dir, fmt.Sprintf("%x", sha256.Sum256(wire)), Result(id, session, status, text)))
}
func EggPath(session, id string) string {
	return fmt.Sprintf("eggs/%s/run.%x.json", session, sha256.Sum256([]byte(id)))
}

func (b *Builder) Conversation(id, session, parent string) *store.Conversation {
	b.t.Helper()
	c, fresh, err := b.Store.ReserveConversation(store.Conversation{ID: id, OwnerID: "owner", SessionID: session, ParentID: parent, LaunchKey: "launch-" + id, SpecDigest: "spec-" + id, Agent: "claude", CWD: "/fixture"})
	b.must(err)
	if !fresh {
		b.t.Fatal("fixture conversation was not fresh")
	}
	b.must(b.Store.SetConversationLaunch(id, "started", ""))
	return c
}
func (b *Builder) Event(c *store.Conversation, cursor int64) int64 {
	b.t.Helper()
	b.must(b.Store.RecordConversationState(c, cursor, "completed", "native"))
	events, err := b.Store.ConversationEvents("owner", c.RootID, 0, 100)
	b.must(err)
	return events[len(events)-1].Sequence
}
func (b *Builder) Checkpoint(id string, revision, after int64) {
	b.t.Helper()
	b.must(b.Store.CheckpointConversation("owner", id, revision, after, PrivateCheckpoint))
}
func (b *Builder) Journal(session, request, conversation string, revision, after int64) {
	b.t.Helper()
	payload, err := json.Marshal(map[string]any{"method": "tools/call", "params": map[string]any{"name": "conversation_checkpoint", "arguments": map[string]any{"conversation_id": conversation, "expected_revision": revision, "after_cursor": after, "checkpoint": PrivateCheckpoint}}})
	b.must(err)
	dir := filepath.Join(b.Path, "conversation-brokers", session)
	b.must(os.MkdirAll(filepath.Join(dir, "journal"), 0700))
	b.must(localmcp.FixturePersistCheckpoint(dir, request, "dispatching", "", payload, fmt.Sprintf("%x", sha256.Sum256(payload))))
}

func (b *Builder) Archive(session, agentName, native string) {
	b.t.Helper()
	dir := filepath.Join(b.Path, "eggs", session)
	b.must(os.MkdirAll(dir, 0700))
	home := filepath.Join(b.Workspace, "provider-"+filepath.Base(b.Path)+"-"+session)
	relative := filepath.Join(".claude", "projects", "-fixture")
	line := map[string]any{"type": "assistant", "sessionId": native, "message": map[string]any{"content": PrivateArchive}}
	if agentName == "codex" {
		relative = filepath.Join(".codex", "sessions")
		line = map[string]any{"type": "session_meta", "payload": map[string]any{"id": native}}
	}
	b.must(os.MkdirAll(filepath.Join(home, relative), 0700))
	wire, err := json.Marshal(line)
	b.must(err)
	b.must(os.WriteFile(filepath.Join(home, relative, native+".jsonl"), append(wire, '\n'), 0600))
	b.Write("eggs/"+session+"/egg.meta", []byte("agent="+agentName+"\nprovider_session_id="+native+"\ncwd=/fixture\n"))
	b.must(egg.CaptureSessionHistory(agentName, "/fixture", dir, home, time.Time{}, native))
}

// Seal closes the writable database before writing the inventory. The manifest
// is exclusively created, so even the fixture writer cannot replace a receipt.
func (b *Builder) Seal() {
	b.t.Helper()
	if b.sealed {
		b.t.Fatal("fixture is already sealed")
	}
	// Normalize only writer-default timestamps. Run receipt timestamps are
	// supplied by Result; no production writer is changed for deterministic tests.
	for _, pair := range [][2]string{{"schema_migrations", "applied_at"}, {"task_log", "timestamp"}, {"conversations", "created_at"}, {"conversations", "updated_at"}, {"conversation_executions", "created_at"}, {"conversation_events", "created_at"}} {
		rows, err := b.Store.DB().Query("PRAGMA table_info(" + pair[0] + ")")
		b.must(err)
		found := false
		for rows.Next() {
			var cid, notnull, pk int
			var name, kind string
			var def any
			b.must(rows.Scan(&cid, &name, &kind, &notnull, &def, &pk))
			found = found || name == pair[1]
		}
		b.must(rows.Err())
		b.must(rows.Close())
		if found {
			b.SQL("UPDATE "+pair[0]+" SET "+pair[1]+"=?", CapturedAt)
		}
	}
	b.must(b.Store.Close())
	b.Store = nil
	b.must(WriteManifest(b.Path))
	b.sealed = true
}

func WriteManifest(path string) error {
	manifest := runinspect.Manifest{Version: 1, Kind: "fixture", Completed: true, CapturedAt: CapturedAt, Files: []runinspect.File{}}
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return errors.New("fixture contains nonregular artifact")
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		if rel == runinspect.ManifestName {
			return errors.New("manifest already exists")
		}
		if strings.HasSuffix(rel, "-wal") || strings.HasSuffix(rel, "-shm") || strings.HasSuffix(rel, "-journal") {
			return errors.New("fixture database has sidecars")
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, runinspect.File{Path: filepath.ToSlash(rel), Bytes: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data))})
		return nil
	})
	if err != nil {
		return err
	}
	wire, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(path, runinspect.ManifestName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(wire, '\n'))
	return errors.Join(writeErr, file.Close())
}
