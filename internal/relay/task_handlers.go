package relay

import (
	"encoding/json"
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
