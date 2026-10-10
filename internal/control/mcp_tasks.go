package control

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// These control operations are transport plumbing, never entries in tools/list.
const (
	MCPTaskCreate   = "mcp/tasks/create"
	MCPTaskGet      = "mcp/tasks/get"
	MCPTaskResult   = "mcp/tasks/result"
	MCPTaskList     = "mcp/tasks/list"
	MCPTaskCancel   = "mcp/tasks/cancel"
	MCPRelatedTask  = "io.modelcontextprotocol/related-task"
	MCPTaskPageSize = 50
)

type MCPTaskParams struct {
	TTL *int64 `json:"ttl,omitempty"`
}

type MCPTaskError struct {
	Code    int
	Message string
}

func (e *MCPTaskError) Error() string { return e.Message }

// MCPTask is the MCP 2025-11-25 Task wire type. A nil TTL means unlimited.
type MCPTask struct {
	TaskID        string `json:"taskId"`
	Status        string `json:"status"`
	StatusMessage string `json:"statusMessage,omitempty"`
	CreatedAt     string `json:"createdAt"`
	LastUpdatedAt string `json:"lastUpdatedAt"`
	TTL           *int64 `json:"ttl"`
	PollInterval  int    `json:"pollInterval,omitempty"`
}

func MCPTaskTool(operation string) (Tool, bool) {
	name := ""
	switch operation {
	case MCPTaskCreate:
		name = "agent_run"
	case MCPTaskGet, MCPTaskResult, MCPTaskList:
		name = "agent_result"
	case MCPTaskCancel:
		name = "agent_stop"
	default:
		return Tool{}, false
	}
	return Lookup(name)
}

func QualifyTaskID(wingID, runID string) string {
	return "wt:" + base64.RawURLEncoding.EncodeToString([]byte(wingID)) + ":" + base64.RawURLEncoding.EncodeToString([]byte(runID))
}

func SplitTaskID(id string) (string, string, error) {
	parts := strings.Split(id, ":")
	if len(parts) != 3 || parts[0] != "wt" {
		return "", "", fmt.Errorf("invalid qualified taskId")
	}
	wing, e1 := base64.RawURLEncoding.DecodeString(parts[1])
	run, e2 := base64.RawURLEncoding.DecodeString(parts[2])
	if e1 != nil || e2 != nil || len(wing) == 0 || len(run) == 0 {
		return "", "", fmt.Errorf("invalid qualified taskId")
	}
	return string(wing), string(run), nil
}

// PageTasks uses stable IDs rather than offsets, so removals cannot skip the
// next page. Cursors are opaque and versioned, and work after reconnect.
func PageTasks(tasks []MCPTask, cursor string) (map[string]any, error) {
	after := ""
	if cursor != "" {
		wire, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || !strings.HasPrefix(string(wire), "tasks-v1:") || len(wire) == len("tasks-v1:") {
			return nil, fmt.Errorf("invalid tasks cursor")
		}
		after = strings.TrimPrefix(string(wire), "tasks-v1:")
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })
	start := sort.Search(len(tasks), func(i int) bool { return tasks[i].TaskID > after })
	end := min(start+MCPTaskPageSize, len(tasks))
	page := append([]MCPTask{}, tasks[start:end]...)
	data := map[string]any{"tasks": page}
	if end < len(tasks) {
		data["nextCursor"] = base64.RawURLEncoding.EncodeToString([]byte("tasks-v1:" + tasks[end-1].TaskID))
	}
	return data, nil
}

// TaskAdmissionArguments puts the retry key inside the original tool arguments.
func TaskAdmissionArguments(args json.RawMessage, admit func(json.RawMessage) (json.RawMessage, string, error)) (json.RawMessage, string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil || fields == nil {
		return nil, "", fmt.Errorf("task arguments must be an object")
	}
	toolArgs, key, err := admit(fields["arguments"])
	if err != nil {
		return nil, "", err
	}
	fields["arguments"] = toolArgs
	wire, err := json.Marshal(fields)
	return wire, key, err
}
