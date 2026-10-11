// Package runinspect explains frozen Wingthing state without contacting a wing,
// opening a provider, or changing a record. It never uses ambient state paths.
package runinspect

import "time"

const ReportVersion = 1
const ManifestName = "snapshot.json"

// Manifest is written by the snapshot supplier after completing a local copy.
// SQLite must be a consolidated backup: WAL/journal sidecars are refused.
// Every regular file except snapshot.json must occur exactly once in Files.
type Manifest struct {
	Version    int       `json:"version"`
	Kind       string    `json:"kind"` // completed_copy or fixture
	Completed  bool      `json:"completed"`
	CapturedAt time.Time `json:"captured_at"`
	Files      []File    `json:"files"`
}

type File struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type Options struct {
	WorkspaceRoot string
	PreviewBytes  int // 0 = metadata only; at most 4096 input bytes per output
}

type Ref struct {
	Path   string `json:"path"`
	Record string `json:"record,omitempty"`
	Field  string `json:"field,omitempty"`
}

type Issue struct {
	Code    string   `json:"code"`
	IDs     []string `json:"ids,omitempty"`
	Sources []Ref    `json:"sources"`
}

type Edge struct {
	Kind    string `json:"kind"`
	From    string `json:"from"`
	To      string `json:"to"`
	State   string `json:"state"` // observed, absent, inconsistent
	Sources []Ref  `json:"sources"`
}

type Output struct {
	Source       Ref    `json:"source"`
	Available    bool   `json:"available"`
	Bytes        int64  `json:"bytes"`
	SHA256       string `json:"sha256,omitempty"`
	OmittedBytes int64  `json:"omitted_bytes"`
	Preview      string `json:"preview,omitempty"` // quoted, escaped untrusted data
}

type Action struct {
	Kind     string   `json:"kind"`
	Basis    []Ref    `json:"basis"`
	Requires []string `json:"requires"`
}

type Checkpoint struct {
	State              string `json:"state"`
	Revision           *int64 `json:"revision,omitempty"`
	AcknowledgedCursor *int64 `json:"acknowledged_cursor,omitempty"`
	EventHead          *int64 `json:"event_head,omitempty"`
	PendingEvents      *int64 `json:"pending_events,omitempty"`
	WakeDeliveryCursor *int64 `json:"wake_delivery_cursor,omitempty"`
	Content            Output `json:"content"`
}

type Archive struct {
	Agent             string `json:"agent,omitempty"`
	ProviderSessionID string `json:"provider_session_id,omitempty"`
	CompressedBytes   int64  `json:"compressed_bytes"`
	DecodedBytes      int64  `json:"decoded_bytes"`
	RotationPending   bool   `json:"rotation_pending"`
	SessionID         string `json:"session_id"`
	State             string `json:"state"`
	Sources           []Ref  `json:"sources"`
}

type Run struct {
	EvidenceState    string   `json:"evidence_state"`
	ID               string   `json:"run_id"`
	SessionID        string   `json:"session_id"`
	ParentID         string   `json:"parent_id,omitempty"`
	ConversationID   string   `json:"conversation_id,omitempty"`
	CurrentSessionID string   `json:"current_session_id,omitempty"`
	Phase            string   `json:"phase"`
	WingStatus       string   `json:"wing_status"`
	EggStatus        string   `json:"egg_status"`
	Diagnosis        string   `json:"diagnosis"`
	Acknowledgement  string   `json:"acknowledgement"`
	Output           []Output `json:"outputs"`
	Sources          []Ref    `json:"sources"`
	Issues           []Issue  `json:"issues"`
	Actions          []Action `json:"suggested_actions"`
}

type Conversation struct {
	ID         string     `json:"conversation_id"`
	ParentID   string     `json:"parent_conversation_id,omitempty"`
	RootID     string     `json:"root_conversation_id"`
	SessionID  string     `json:"session_id"`
	Executions []string   `json:"executions"`
	Checkpoint Checkpoint `json:"checkpoint"`
	Sources    []Ref      `json:"sources"`
	Issues     []Issue    `json:"issues"`
}

type Report struct {
	CheckpointAttempts       []CheckpointAttempt `json:"checkpoint_attempts"`
	Version                  int                 `json:"version"`
	CapturedAt               time.Time           `json:"captured_at"`
	InputFiles               []File              `json:"input_files"`
	Runs                     []Run               `json:"runs"`
	Conversations            []Conversation      `json:"conversations"`
	Archives                 []Archive           `json:"archives"`
	Edges                    []Edge              `json:"edges"`
	Issues                   []Issue             `json:"issues"`
	RequiredHostObservations []string            `json:"required_host_observations"`
}
