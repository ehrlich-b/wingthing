package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

func (s *Server) handleSubmitTask(w http.ResponseWriter, r *http.Request) {
	userID := s.requireUser(w, r)
	if userID == "" {
		return
	}

	var req struct {
		Prompt    string `json:"prompt"`
		Skill     string `json:"skill"`
		Agent     string `json:"agent"`
		Isolation string `json:"isolation"`
		Target    string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Prompt == "" && req.Skill == "" {
		writeError(w, http.StatusBadRequest, "prompt or skill is required")
		return
	}

	taskID := fmt.Sprintf("rt-%s", time.Now().Format("20060102-150405"))

	// Build the opaque payload — this is what the wing receives.
	// The relay forwards it as-is without inspecting content.
	submit := ws.TaskSubmit{
		Type:      ws.TypeTaskSubmit,
		TaskID:    taskID,
		Prompt:    req.Prompt,
		Skill:     req.Skill,
		Agent:     req.Agent,
		Isolation: req.Isolation,
	}
	payload, _ := json.Marshal(submit)

	identity := req.Target
	if identity == "" {
		identity = userID
	}

	err := s.SubmitTask(r.Context(), userID, identity, taskID, payload)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no wing available: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"task_id": taskID,
	})
}

// handleTaskStream serves SSE for live task output. No DB reads — purely in-memory.
func (s *Server) handleTaskStream(w http.ResponseWriter, r *http.Request) {
	userID := s.requireUser(w, r)
	if userID == "" {
		return
	}

	taskID := r.PathValue("id")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task id required")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Subscribe to live chunks
	ch := make(chan string, 64)
	s.streamMu.Lock()
	s.streamSubs[taskID] = append(s.streamSubs[taskID], ch)
	s.streamMu.Unlock()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case text, ok := <-ch:
			if !ok {
				fmt.Fprintf(w, "event: done\ndata: done\n\n")
				flusher.Flush()
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", text)
			flusher.Flush()
		}
	}
}
