package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/control"
)

type mcpProtocolState struct {
	mu      sync.Mutex
	version string
}

func (s *mcpProtocolState) negotiate(params json.RawMessage) string {
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &init)
	version := init.ProtocolVersion
	if version != "2025-06-18" && version != "2025-03-26" {
		version = localMCPProtocolVersion
	}
	s.mu.Lock()
	s.version = version
	s.mu.Unlock()
	return version
}

func (s *mcpProtocolState) tasksEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version == "" || s.version == localMCPProtocolVersion
}

func mcpCapabilities(version string) map[string]any {
	caps := map[string]any{"tools": map[string]any{}}
	if version == localMCPProtocolVersion {
		caps["tasks"] = map[string]any{"list": map[string]any{}, "cancel": map[string]any{}, "requests": map[string]any{"tools": map[string]any{"call": map[string]any{}}}}
	}
	return caps
}

func mcpVersionTools(tools []control.Tool, tasks bool) []control.Tool {
	if !tasks {
		for i := range tools {
			tools[i].Execution = nil
		}
	}
	return tools
}

type taskTarget struct {
	TaskID string          `json:"taskId"`
	Meta   json.RawMessage `json:"_meta,omitempty"`
}
type taskListParams struct {
	Cursor string          `json:"cursor,omitempty"`
	Meta   json.RawMessage `json:"_meta,omitempty"`
}

type taskCaller func(context.Context, string, json.RawMessage) (map[string]any, bool, error)

func taskProtocolError(err error) *localMCPError {
	var rpcErr *control.MCPTaskError
	if errors.As(err, &rpcErr) {
		return &localMCPError{Code: rpcErr.Code, Message: rpcErr.Message}
	}
	return &localMCPError{Code: -32603, Message: err.Error()}
}

func relatedTask(id string) map[string]any {
	return map[string]any{control.MCPRelatedTask: map[string]any{"taskId": id}}
}

func mcpRequestKey(raw json.RawMessage) string {
	// Equivalent JSON spellings of a string ID select the same observer.
	var id string
	if json.Unmarshal(raw, &id) == nil {
		wire, _ := json.Marshal(id)
		return string(wire)
	}
	return string(raw)
}

// handleMCPTaskRequest translates native MCP envelopes, leaving execution and
// authorization in the wing even when this stdio process exits.
func handleMCPTaskRequest(ctx context.Context, request localMCPRequest, caller taskCaller, aggregate bool) localMCPResponse {
	response := localMCPResponse{JSONRPC: "2.0", ID: request.ID}
	operation := ""
	arguments := request.Params
	taskID := ""
	failParams := func(err error) localMCPResponse {
		response.Error = &localMCPError{Code: -32602, Message: err.Error()}
		return response
	}
	if request.Method == "tools/call" {
		var call localMCPToolCallParams
		if err := decodeStrict(request.Params, &call); err != nil {
			return failParams(err)
		}
		if call.Name != "agent_run" || call.Task == nil {
			response.Error = &localMCPError{Code: -32601, Message: "tool does not support task augmentation"}
			return response
		}
		if call.Task.TTL != nil && (*call.Task.TTL < 0 || *call.Task.TTL > int64((365*24*time.Hour)/time.Millisecond)) {
			return failParams(fmt.Errorf("task ttl must be between 0 and 31536000000 milliseconds"))
		}
		if len(call.Arguments) == 0 {
			call.Arguments = json.RawMessage(`{}`)
		}
		envelope := map[string]any{"name": call.Name, "arguments": call.Arguments, "task": call.Task}
		if aggregate {
			wing, args, err := control.SplitWingTarget(call.Arguments)
			if err != nil {
				return failParams(err)
			}
			envelope["wing_id"] = wing
			envelope["arguments"] = args
		}
		arguments, _ = json.Marshal(envelope)
		operation = control.MCPTaskCreate
	} else if request.Method == "tasks/list" {
		var params taskListParams
		if len(arguments) == 0 {
			arguments = json.RawMessage(`{}`)
		}
		if err := decodeStrict(arguments, &params); err != nil {
			return failParams(err)
		}
		operation = control.MCPTaskList
	} else {
		var target taskTarget
		if err := decodeStrict(arguments, &target); err != nil || target.TaskID == "" {
			if err == nil {
				err = fmt.Errorf("taskId is required")
			}
			return failParams(err)
		}
		taskID = target.TaskID
		envelope := map[string]any{"taskId": taskID}
		if aggregate {
			wing, run, err := control.SplitTaskID(taskID)
			if err != nil {
				return failParams(err)
			}
			envelope["wing_id"] = wing
			envelope["taskId"] = run
		}
		arguments, _ = json.Marshal(envelope)
		switch request.Method {
		case "tasks/get":
			operation = control.MCPTaskGet
		case "tasks/result":
			operation = control.MCPTaskResult
		case "tasks/cancel":
			operation = control.MCPTaskCancel
		default:
			response.Error = &localMCPError{Code: -32601, Message: "method not found"}
			return response
		}
	}
	data, isError, err := caller(ctx, operation, arguments)
	if err != nil {
		response.Error = taskProtocolError(err)
		return response
	}
	if isError && operation != control.MCPTaskResult {
		response.Error = &localMCPError{Code: -32603, Message: fmt.Sprint(data["error"])}
		return response
	}
	if operation == control.MCPTaskResult {
		// isError matches agent_result, whose payload describes run failure.
		response.Result = localMCPToolResult(data, isError)
		response.Result.(map[string]any)["_meta"] = relatedTask(taskID)
	} else {
		delete(data, "wing_id")
		delete(data, "idempotency_key")
		response.Result = data
		if operation == control.MCPTaskCreate {
			wire, _ := json.Marshal(data["task"])
			var task control.MCPTask
			_ = json.Unmarshal(wire, &task)
			data["_meta"] = relatedTask(task.TaskID)
		}
	}
	return response
}

func (s *Server) callTaskControl(ctx context.Context, operation string, arguments json.RawMessage) (map[string]any, bool, error) {
	fail := func(code int, err error) (map[string]any, bool, error) {
		return nil, true, &control.MCPTaskError{Code: code, Message: err.Error()}
	}
	tool, ok := control.MCPTaskTool(operation)
	if !ok || !tool.Supports(s.controlSurface()) {
		return fail(-32601, errors.New("task operation unavailable"))
	}
	if !s.toolAllowed(tool.Name) {
		return fail(-32602, errors.New("task operation not permitted"))
	}
	if s.Sessions == nil || s.Sessions.RunManager == nil {
		return fail(-32603, errors.New("wing run service is not ready"))
	}
	m := s.Sessions.RunManager
	a := s.sessionAuthority()
	if operation == control.MCPTaskCreate {
		var call localMCPToolCallParams
		if err := decodeStrict(arguments, &call); err != nil || call.Name != "agent_run" || call.Task == nil {
			return fail(-32602, errors.New("invalid task admission"))
		}
		if call.Task.TTL != nil && (*call.Task.TTL < 0 || *call.Task.TTL > 31536000000) {
			return fail(-32602, errors.New("invalid task ttl"))
		}
		if s.mailboxLocked {
			return fail(-32602, errors.New("the wing is locked"))
		}
		if err := s.checkBoundSessionTarget("agent_run", call.Arguments); err != nil {
			return fail(-32602, err)
		}
		data, err := s.toolAgentRunTask(call.Arguments, call.Task)
		decision := "allowed"
		if err != nil {
			decision = "error"
		}
		_ = s.auditToolCall("agent_run", call.Arguments, data, decision)
		if err != nil {
			return fail(-32602, err)
		}
		id, _ := data["run_id"].(string)
		run, err := m.Get(a, id)
		if err != nil {
			return fail(-32602, err)
		}
		// Admission always returns the initial working snapshot, even if the
		// provider finished before the response was encoded.
		task := control.MCPTask{TaskID: id, Status: "working", CreatedAt: run.CreatedAt.UTC().Format(time.RFC3339Nano), LastUpdatedAt: run.CreatedAt.UTC().Format(time.RFC3339Nano), PollInterval: 5000}
		// A replay of already terminal work must never move its task back to
		// working. Fresh admission uses the original snapshot above.
		if data["status"] == "done" || data["status"] == "failed" || data["status"] == "timeout" || data["status"] == "stopped" || run.MCPTask != nil && !run.MCPTask.CancelledAt.IsZero() {
			task, err = m.Task(a, id)
			if err != nil {
				return fail(-32602, err)
			}
		}
		return map[string]any{"task": task}, false, nil
	}
	if operation == control.MCPTaskList {
		var params taskListParams
		if err := decodeStrict(arguments, &params); err != nil {
			return fail(-32602, err)
		}
		data, err := control.PageTasks(m.Tasks(a), params.Cursor)
		if err != nil {
			return fail(-32602, err)
		}
		return data, false, nil
	}
	var target taskTarget
	if err := decodeStrict(arguments, &target); err != nil || target.TaskID == "" {
		return fail(-32602, errors.New("taskId is required"))
	}
	if err := s.checkBoundSessionTarget(tool.Name, runArgsJSON(target.TaskID)); err != nil {
		return fail(-32602, err)
	}
	task, err := m.Task(a, target.TaskID)
	if err != nil {
		return fail(-32602, err)
	}
	switch operation {
	case control.MCPTaskResult:
		if err := m.WaitTask(ctx, a, target.TaskID); err != nil {
			return fail(-32603, err)
		}
		data, isError, protocolErr := s.callTool(ctx, "agent_result", runArgsJSON(target.TaskID))
		if protocolErr != nil {
			return fail(protocolErr.Code, errors.New(protocolErr.Message))
		}
		return data, isError, nil
	case control.MCPTaskCancel:
		if s.mailboxLocked {
			return fail(-32602, errors.New("the wing is locked"))
		}
		task, err = m.CancelTask(a, target.TaskID)
		if err != nil {
			return fail(-32602, err)
		}
		_ = s.auditToolCall("agent_stop", runArgsJSON(target.TaskID), map[string]any{"run_id": target.TaskID}, "allowed")
	}
	return taskMap(task), false, nil
}

func runArgsJSON(id string) json.RawMessage {
	wire, _ := json.Marshal(map[string]any{"run_id": id})
	return wire
}
func taskMap(task control.MCPTask) map[string]any {
	wire, _ := json.Marshal(task)
	var data map[string]any
	_ = json.Unmarshal(wire, &data)
	return data
}
