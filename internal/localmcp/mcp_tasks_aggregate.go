package localmcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/wingconnect"
)

func qualifyTaskResult(wing string, operation string, data map[string]any) map[string]any {
	if operation == control.MCPTaskCreate {
		wire, _ := json.Marshal(data["task"])
		var task control.MCPTask
		_ = json.Unmarshal(wire, &task)
		task.TaskID = control.QualifyTaskID(wing, task.TaskID)
		data["task"] = task
	} else if operation == control.MCPTaskGet || operation == control.MCPTaskCancel {
		id, _ := data["taskId"].(string)
		data["taskId"] = control.QualifyTaskID(wing, id)
	}
	return data
}

// Collect each wing's pages before global pagination. Errors are explicit:
// an offline wing must never silently erase tasks from the aggregate list.
func collectWingTasks(ctx context.Context, wing string, call taskCaller) ([]control.MCPTask, error) {
	tasks := []control.MCPTask{}
	cursor := ""
	seen := map[string]bool{}
	for {
		args, _ := json.Marshal(taskListParams{Cursor: cursor})
		data, denied, err := call(ctx, control.MCPTaskList, args)
		if err != nil {
			return nil, err
		}
		if denied {
			return nil, errors.New("wing denied task listing")
		}
		wire, _ := json.Marshal(data)
		var page struct {
			Tasks      []control.MCPTask `json:"tasks"`
			NextCursor string            `json:"nextCursor"`
		}
		if err := json.Unmarshal(wire, &page); err != nil {
			return nil, err
		}
		for _, task := range page.Tasks {
			task.TaskID = control.QualifyTaskID(wing, task.TaskID)
			tasks = append(tasks, task)
		}
		if page.NextCursor == "" {
			return tasks, nil
		}
		if seen[page.NextCursor] {
			return nil, errors.New("wing repeated task listing cursor")
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
}

func (m *rememberedWings) dispatchTasks(ctx context.Context, s *Server, hello controlsocket.Hello, request control.DirectRequest) control.DirectResponse {
	response := control.DirectResponse{Version: control.ContractVersion, ID: request.ID}
	fail := func(err error) control.DirectResponse {
		rpcErr := taskProtocolError(err)
		response.Error = rpcErr.Message
		response.RPCErrorCode = rpcErr.Code
		return response
	}
	tool, _ := control.MCPTaskTool(request.Tool)
	if !s.toolAllowed(tool.Name) {
		return fail(&control.MCPTaskError{Code: -32602, Message: "task operation not permitted"})
	}
	if request.Tool == control.MCPTaskList {
		var params taskListParams
		if err := decodeStrict(request.Arguments, &params); err != nil {
			return fail(&control.MCPTaskError{Code: -32602, Message: err.Error()})
		}
		if _, err := control.PageTasks(nil, params.Cursor); err != nil {
			return fail(&control.MCPTaskError{Code: -32602, Message: err.Error()})
		}
		tasks, err := collectWingTasks(ctx, m.wingID, s.callTaskControl)
		if err != nil {
			return fail(err)
		}
		p, err := m.pool(hello)
		if err != nil {
			return fail(err)
		}
		entries, registryError := p.Entries()
		if registryError != "" {
			return fail(errors.New(registryError))
		}
		for _, entry := range entries {
			wing, _ := entry["wing_id"].(string)
			if wing == "" {
				continue
			}
			remote, err := collectWingTasks(ctx, wing, func(ctx context.Context, op string, args json.RawMessage) (map[string]any, bool, error) {
				return p.Call(ctx, wing, op, args)
			})
			if err != nil {
				return fail(err)
			}
			tasks = append(tasks, remote...)
		}
		response.Result, err = control.PageTasks(tasks, params.Cursor)
		if err != nil {
			return fail(err)
		}
		return response
	}
	wing, args, err := control.SplitWingTarget(request.Arguments)
	if err != nil {
		return fail(&control.MCPTaskError{Code: -32602, Message: err.Error()})
	}
	if wing != m.wingID {
		p, err := m.pool(hello)
		if err != nil {
			return fail(err)
		}
		data, denied, err := p.Call(ctx, wing, request.Tool, args)
		if err != nil {
			return fail(err)
		}
		response.Result = qualifyTaskResult(wing, request.Tool, data)
		response.IsError = denied
		return response
	}
	if request.Tool == control.MCPTaskCreate {
		args, _, err = control.TaskAdmissionArguments(args, wingconnect.AdmissionArguments)
		if err != nil {
			return fail(&control.MCPTaskError{Code: -32602, Message: err.Error()})
		}
	}
	request.Arguments = args
	response = s.handleDirectRequest(ctx, request)
	if response.Result != nil {
		response.Result = qualifyTaskResult(wing, request.Tool, response.Result)
	}
	return response
}

func (s *ConnectMCPServer) callTaskControl(ctx context.Context, operation string, arguments json.RawMessage) (map[string]any, bool, error) {
	if operation == control.MCPTaskList {
		var params taskListParams
		if err := decodeStrict(arguments, &params); err != nil {
			return nil, true, &control.MCPTaskError{Code: -32602, Message: err.Error()}
		}
		if _, err := control.PageTasks(nil, params.Cursor); err != nil {
			return nil, true, &control.MCPTaskError{Code: -32602, Message: err.Error()}
		}
		wings, err := s.Tunnel.ListWings(ctx)
		if err != nil {
			return nil, true, err
		}
		tasks := []control.MCPTask{}
		for _, wing := range wings {
			client, err := s.controlClient(ctx, wing.WingID)
			if err != nil {
				return nil, true, err
			}
			page, err := collectWingTasks(ctx, wing.WingID, client.Call)
			if err != nil {
				return nil, true, err
			}
			tasks = append(tasks, page...)
		}
		data, err := control.PageTasks(tasks, params.Cursor)
		return data, err != nil, err
	}
	wing, args, err := control.SplitWingTarget(arguments)
	if err != nil {
		return nil, true, &control.MCPTaskError{Code: -32602, Message: err.Error()}
	}
	client, err := s.controlClient(ctx, wing)
	if err != nil {
		return nil, true, err
	}
	data, denied, err := client.Call(ctx, operation, args)
	if err != nil {
		if client.Closed() {
			s.evictControl(wing, client)
		}
		return nil, true, err
	}
	data = control.QualifyResult(wing, data)
	return qualifyTaskResult(wing, operation, data), denied, nil
}
