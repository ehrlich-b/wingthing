package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
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

	task, err := s.SubmitRelayTask(r.Context(), userID, req.Prompt, req.Skill, req.Agent, req.Isolation, req.Target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"task_id": task.ID,
		"status":  task.Status,
	})
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	userID := s.requireUser(w, r)
	if userID == "" {
		return
	}

	tasks, err := s.Store.ListRelayTasksForUser(userID, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	userID := s.requireUser(w, r)
	if userID == "" {
		return
	}

	taskID := r.PathValue("id")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task id required")
		return
	}

	task, err := s.Store.GetRelayTask(taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if task.UserID != userID {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	writeJSON(w, http.StatusOK, task)
}

// handleTaskStream serves SSE for live task output.
func (s *Server) handleTaskStream(w http.ResponseWriter, r *http.Request) {
	userID := s.requireUser(w, r)
	if userID == "" {
		return
	}

	taskID := r.PathValue("id")
	task, err := s.Store.GetRelayTask(taskID)
	if err != nil || task == nil || task.UserID != userID {
		writeError(w, http.StatusNotFound, "task not found")
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

	// If task is already done, send the output and close
	if task.Status == "done" || task.Status == "failed" {
		fmt.Fprintf(w, "data: %s\n\n", task.Output)
		fmt.Fprintf(w, "event: done\ndata: %s\n\n", task.Status)
		flusher.Flush()
		return
	}

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
				// Channel closed — task done or errored
				updated, _ := s.Store.GetRelayTask(taskID)
				status := "done"
				if updated != nil {
					status = updated.Status
				}
				fmt.Fprintf(w, "event: done\ndata: %s\n\n", status)
				flusher.Flush()
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", text)
			flusher.Flush()
		}
	}
}
