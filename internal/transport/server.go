package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/thread"
)

var taskCounter uint64

func genTaskID() string {
	n := atomic.AddUint64(&taskCounter, 1)
	return fmt.Sprintf("t-%s-%03d", time.Now().Format("20060102"), n)
}

type Server struct {
	store      *store.Store
	socketPath string
}

func NewServer(s *store.Store, socketPath string) *Server {
	return &Server{store: s, socketPath: socketPath}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	// Clean up stale socket.
	os.Remove(s.socketPath)

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", s.socketPath, err)
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	srv := &http.Server{Handler: mux}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
		os.Remove(s.socketPath)
		return nil
	case err := <-errCh:
		os.Remove(s.socketPath)
		return err
	}
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /tasks", s.handleCreateTask)
	mux.HandleFunc("GET /tasks", s.handleListTasks)
	mux.HandleFunc("GET /tasks/{id}", s.handleGetTask)
	mux.HandleFunc("POST /tasks/{id}/retry", s.handleRetryTask)
	mux.HandleFunc("GET /thread", s.handleGetThread)
	mux.HandleFunc("GET /agents", s.handleListAgents)
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("GET /log/{taskId}", s.handleGetLog)
}

// Request/response types

type createTaskRequest struct {
	What  string `json:"what"`
	Type  string `json:"type"`
	Agent string `json:"agent"`
	RunAt string `json:"run_at"`
}

type taskResponse struct {
	ID         string  `json:"id"`
	Type       string  `json:"type"`
	What       string  `json:"what"`
	RunAt      string  `json:"run_at"`
	Agent      string  `json:"agent"`
	Isolation  string  `json:"isolation"`
	Status     string  `json:"status"`
	CreatedAt  string  `json:"created_at"`
	StartedAt  *string `json:"started_at,omitempty"`
	FinishedAt *string `json:"finished_at,omitempty"`
	Output     *string `json:"output,omitempty"`
	Error      *string `json:"error,omitempty"`
	ParentID   *string `json:"parent_id,omitempty"`
}

type statusResponse struct {
	Pending    int `json:"pending"`
	Running    int `json:"running"`
	Agents     int `json:"agents"`
	TokensToday int `json:"tokens_today"`
	TokensWeek  int `json:"tokens_week"`
}

func taskToResponse(t *store.Task) taskResponse {
	r := taskResponse{
		ID:        t.ID,
		Type:      t.Type,
		What:      t.What,
		RunAt:     t.RunAt.UTC().Format(time.RFC3339),
		Agent:     t.Agent,
		Isolation: t.Isolation,
		Status:    t.Status,
		CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339),
		Output:    t.Output,
		Error:     t.Error,
		ParentID:  t.ParentID,
	}
	if t.StartedAt != nil {
		s := t.StartedAt.UTC().Format(time.RFC3339)
		r.StartedAt = &s
	}
	if t.FinishedAt != nil {
		s := t.FinishedAt.UTC().Format(time.RFC3339)
		r.FinishedAt = &s
	}
	return r
}

// Handlers

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.What == "" {
		writeError(w, http.StatusBadRequest, "what is required")
		return
	}
	if req.Type == "" {
		req.Type = "prompt"
	}
	if req.Agent == "" {
		req.Agent = "claude"
	}
	runAt := time.Now().UTC()
	if req.RunAt != "" {
		parsed, err := time.Parse(time.RFC3339, req.RunAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid run_at: "+err.Error())
			return
		}
		runAt = parsed.UTC()
	}

	t := &store.Task{
		ID:    genTaskID(),
		Type:  req.Type,
		What:  req.What,
		RunAt: runAt,
		Agent: req.Agent,
	}
	if err := s.store.CreateTask(t); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Re-read to get defaults filled by store.
	created, err := s.store.GetTask(t.ID)
	if err != nil || created == nil {
		// Fallback to what we have.
		writeJSON(w, http.StatusCreated, taskToResponse(t))
		return
	}
	writeJSON(w, http.StatusCreated, taskToResponse(created))
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limitStr := r.URL.Query().Get("limit")
	limit := 20
	if limitStr != "" {
		n, err := strconv.Atoi(limitStr)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = n
	}

	// Fetch more than needed so we can filter by status.
	fetchLimit := limit
	if status != "" {
		fetchLimit = limit * 5
		if fetchLimit < 100 {
			fetchLimit = 100
		}
	}
	tasks, err := s.store.ListRecent(fetchLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var result []taskResponse
	for _, t := range tasks {
		if status != "" && t.Status != status {
			continue
		}
		result = append(result, taskToResponse(t))
		if len(result) >= limit {
			break
		}
	}
	if result == nil {
		result = []taskResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := s.store.GetTask(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if t == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, taskToResponse(t))
}

func (s *Server) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := s.store.GetTask(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if t == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if t.Status != "failed" {
		writeError(w, http.StatusBadRequest, "only failed tasks can be retried")
		return
	}

	// Reset to pending with run_at = now and clear error.
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	_, err = s.store.DB().Exec(
		"UPDATE tasks SET status = 'pending', run_at = ?, error = NULL, started_at = NULL, finished_at = NULL WHERE id = ?",
		now, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	t, _ = s.store.GetTask(id)
	if t != nil {
		writeJSON(w, http.StatusOK, taskToResponse(t))
	} else {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func (s *Server) handleGetThread(w http.ResponseWriter, r *http.Request) {
	dateStr := r.URL.Query().Get("date")
	budgetStr := r.URL.Query().Get("budget")

	date := time.Now().UTC()
	if dateStr != "" {
		parsed, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid date format, use YYYY-MM-DD")
			return
		}
		date = parsed
	}

	budget := 0
	if budgetStr != "" {
		n, err := strconv.Atoi(budgetStr)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid budget")
			return
		}
		budget = n
	}

	rendered, err := thread.RenderDay(s.store, date, budget)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"thread": rendered})
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.ListAgents()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type agentResp struct {
		Name             string  `json:"name"`
		Adapter          string  `json:"adapter"`
		Command          string  `json:"command"`
		ContextWindow    int     `json:"context_window"`
		DefaultIsolation string  `json:"default_isolation"`
		Healthy          bool    `json:"healthy"`
		HealthChecked    *string `json:"health_checked,omitempty"`
	}
	result := make([]agentResp, 0, len(agents))
	for _, a := range agents {
		ar := agentResp{
			Name:             a.Name,
			Adapter:          a.Adapter,
			Command:          a.Command,
			ContextWindow:    a.ContextWindow,
			DefaultIsolation: a.DefaultIsolation,
			Healthy:          a.Healthy,
		}
		if a.HealthChecked != nil {
			s := a.HealthChecked.UTC().Format(time.RFC3339)
			ar.HealthChecked = &s
		}
		result = append(result, ar)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	var pending, running int
	row := s.store.DB().QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'pending'")
	row.Scan(&pending)
	row = s.store.DB().QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'running'")
	row.Scan(&running)
	agents, _ := s.store.ListAgents()

	now := time.Now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	weekStart := todayStart.AddDate(0, 0, -6)
	tomorrow := todayStart.AddDate(0, 0, 1)

	tokensToday, _ := s.store.SumTokensByDateRange(todayStart, tomorrow)
	tokensWeek, _ := s.store.SumTokensByDateRange(weekStart, tomorrow)

	writeJSON(w, http.StatusOK, statusResponse{
		Pending:     pending,
		Running:     running,
		Agents:      len(agents),
		TokensToday: tokensToday,
		TokensWeek:  tokensWeek,
	})
}

func (s *Server) handleGetLog(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	entries, err := s.store.ListLogByTask(taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type logResp struct {
		ID        int64   `json:"id"`
		TaskID    string  `json:"task_id"`
		Timestamp string  `json:"timestamp"`
		Event     string  `json:"event"`
		Detail    *string `json:"detail,omitempty"`
	}
	result := make([]logResp, 0, len(entries))
	for _, e := range entries {
		result = append(result, logResp{
			ID:        e.ID,
			TaskID:    e.TaskID,
			Timestamp: e.Timestamp.UTC().Format(time.RFC3339),
			Event:     e.Event,
			Detail:    e.Detail,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

// Helpers

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
