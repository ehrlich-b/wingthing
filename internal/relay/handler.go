package relay

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/google/uuid"
	"nhooyr.io/websocket"
)

const (
	writeTimeout     = 10 * time.Second
	authTimeout      = 10 * time.Second
	deviceCodeExpiry = 15 * time.Minute
	userCodeChars    = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no I/O/0/1 for clarity
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAuthDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MachineID string `json:"machine_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.MachineID == "" {
		writeError(w, http.StatusBadRequest, "machine_id is required")
		return
	}

	deviceCode := uuid.New().String()
	userCode := generateUserCode(6)
	expiresAt := time.Now().Add(deviceCodeExpiry)

	if err := s.Store.CreateDeviceCode(deviceCode, userCode, req.MachineID, expiresAt); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// TODO: rate limiting on this endpoint
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":      deviceCode,
		"user_code":        userCode,
		"verification_url": "/auth/claim",
		"expires_in":       int(deviceCodeExpiry.Seconds()),
		"interval":         5,
	})
}

func (s *Server) handleAuthToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.DeviceCode == "" {
		writeError(w, http.StatusBadRequest, "device_code is required")
		return
	}

	dc, err := s.Store.GetDeviceCode(req.DeviceCode)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if dc == nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": "invalid_code"})
		return
	}
	if time.Now().After(dc.ExpiresAt) {
		writeJSON(w, http.StatusOK, map[string]string{"error": "expired_code"})
		return
	}
	if !dc.Claimed || dc.UserID == nil {
		// TODO: rate limiting — could return slow_down
		writeJSON(w, http.StatusOK, map[string]string{"error": "authorization_pending"})
		return
	}

	token := uuid.New().String()
	if err := s.Store.CreateDeviceToken(token, *dc.UserID, dc.DeviceID, nil); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.Store.AppendAudit(*dc.UserID, "token_issued", strPtr(fmt.Sprintf("device=%s", dc.DeviceID)))

	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_at": 0,
	})
}

func (s *Server) handleAuthClaim(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserCode string `json:"user_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.UserCode == "" {
		writeError(w, http.StatusBadRequest, "user_code is required")
		return
	}

	// Find device code by user_code
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	var code string
	err := s.Store.db.QueryRow(
		"SELECT code FROM device_codes WHERE user_code = ? AND claimed = 0 AND expires_at > ?",
		strings.ToUpper(req.UserCode), now,
	).Scan(&code)
	if err != nil {
		writeError(w, http.StatusNotFound, "invalid or expired user code")
		return
	}

	// Auto-create user for now (no OAuth)
	userID := uuid.New().String()
	if err := s.Store.CreateUser(userID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := s.Store.ClaimDeviceCode(code, userID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.Store.AppendAudit(userID, "device_claimed", strPtr(fmt.Sprintf("code=%s", code)))

	writeJSON(w, http.StatusOK, map[string]any{
		"claimed": true,
		"user_id": userID,
	})
}

func (s *Server) handleAuthRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}

	userID, deviceID, err := s.Store.ValidateToken(req.Token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}

	// Delete old token, issue new one
	if err := s.Store.DeleteToken(req.Token); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	newToken := uuid.New().String()
	if err := s.Store.CreateDeviceToken(newToken, userID, deviceID, nil); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.Store.AppendAudit(userID, "token_refreshed", strPtr(fmt.Sprintf("device=%s", deviceID)))

	writeJSON(w, http.StatusOK, map[string]any{
		"token":      newToken,
		"expires_at": 0,
	})
}

func (s *Server) handleDaemonWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusInternalError, "unexpected close")

	// Read auth message
	authCtx, authCancel := context.WithTimeout(r.Context(), authTimeout)
	_, data, err := conn.Read(authCtx)
	authCancel()
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "auth timeout")
		return
	}

	var msg ws.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		conn.Close(websocket.StatusPolicyViolation, "invalid message")
		return
	}
	if msg.Type != ws.MsgAuth {
		conn.Close(websocket.StatusPolicyViolation, "expected auth message")
		return
	}

	var authPayload ws.AuthPayload
	if err := msg.ParsePayload(&authPayload); err != nil {
		conn.Close(websocket.StatusPolicyViolation, "invalid auth payload")
		return
	}

	userID, deviceID, err := s.Store.ValidateToken(authPayload.DeviceToken)
	if err != nil {
		reply, _ := ws.NewMessage(ws.MsgAuthResult, ws.AuthResultPayload{Success: false, Error: "invalid token"})
		replyData, _ := json.Marshal(reply)
		conn.Write(r.Context(), websocket.MessageText, replyData)
		conn.Close(websocket.StatusPolicyViolation, "auth failed")
		return
	}

	// Auth success
	reply, _ := ws.NewMessage(ws.MsgAuthResult, ws.AuthResultPayload{Success: true})
	reply.ReplyTo = msg.ID
	replyData, _ := json.Marshal(reply)
	if err := conn.Write(r.Context(), websocket.MessageText, replyData); err != nil {
		return
	}

	dc := s.sessions.AddDaemon(userID, deviceID, conn)
	defer s.sessions.RemoveDaemon(userID, deviceID)

	s.Store.AppendAudit(userID, "daemon_connected", strPtr(fmt.Sprintf("device=%s", deviceID)))

	ctx := r.Context()

	// Writer goroutine: sends messages from the Send channel to the WS connection
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-dc.Send:
				if !ok {
					return
				}
				data, err := json.Marshal(msg)
				if err != nil {
					continue
				}
				writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
				err = conn.Write(writeCtx, websocket.MessageText, data)
				cancel()
				if err != nil {
					return
				}
			}
		}
	}()

	// Reader loop: reads messages from daemon and broadcasts to clients
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			break
		}

		var incoming ws.Message
		if err := json.Unmarshal(data, &incoming); err != nil {
			continue
		}

		// Daemon sends results/status back — broadcast to connected clients
		switch incoming.Type {
		case ws.MsgTaskResult, ws.MsgTaskStatus, ws.MsgStatus, ws.MsgSyncResponse, ws.MsgPong:
			s.sessions.BroadcastToClients(userID, &incoming)
		case ws.MsgPing:
			pong, err := ws.NewMessage(ws.MsgPong, nil)
			if err == nil {
				pongData, _ := json.Marshal(pong)
				writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
				conn.Write(writeCtx, websocket.MessageText, pongData)
				cancel()
			}
		}
	}

	<-done
	conn.Close(websocket.StatusNormalClosure, "closing")
}

func (s *Server) handleClientWS(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "token required", http.StatusUnauthorized)
		return
	}

	userID, _, err := s.Store.ValidateToken(token)
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusInternalError, "unexpected close")

	cc := s.sessions.AddClient(userID, conn)
	defer s.sessions.RemoveClient(userID, conn)

	s.Store.AppendAudit(userID, "client_connected", nil)

	ctx := r.Context()

	// Writer goroutine: sends messages from the Send channel to the WS connection
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-cc.Send:
				if !ok {
					return
				}
				data, err := json.Marshal(msg)
				if err != nil {
					continue
				}
				writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
				err = conn.Write(writeCtx, websocket.MessageText, data)
				cancel()
				if err != nil {
					return
				}
			}
		}
	}()

	// Reader loop: reads messages from client and routes to daemon
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			break
		}

		var incoming ws.Message
		if err := json.Unmarshal(data, &incoming); err != nil {
			continue
		}

		// Client sends commands — route to daemon
		switch incoming.Type {
		case ws.MsgTaskSubmit, ws.MsgSyncRequest, ws.MsgPing:
			if incoming.Type == ws.MsgPing {
				pong, err := ws.NewMessage(ws.MsgPong, nil)
				if err == nil {
					pongData, _ := json.Marshal(pong)
					writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
					conn.Write(writeCtx, websocket.MessageText, pongData)
					cancel()
				}
				continue
			}
			if err := s.sessions.RouteToUser(userID, &incoming); err != nil {
				errMsg, _ := ws.NewMessage(ws.MsgError, ws.ErrorPayload{
					Code:    "no_daemon",
					Message: "no daemon connected",
				})
				errData, _ := json.Marshal(errMsg)
				writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
				conn.Write(writeCtx, websocket.MessageText, errData)
				cancel()
			}
		}
	}

	<-done
	conn.Close(websocket.StatusNormalClosure, "closing")
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

func generateUserCode(n int) string {
	b := make([]byte, n)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(userCodeChars))))
		b[i] = userCodeChars[idx.Int64()]
	}
	return string(b)
}

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	q := r.URL.Query().Get("q")

	var skills []*SkillRow
	var err error
	if q != "" {
		skills, err = s.Store.SearchSkills(q)
	} else {
		skills, err = s.Store.ListSkills(category)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type skillMeta struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Category    string `json:"category"`
		Agent       string `json:"agent"`
		Tags        string `json:"tags"`
		SHA256      string `json:"sha256"`
		Publisher   string `json:"publisher"`
	}

	out := make([]skillMeta, len(skills))
	for i, sk := range skills {
		out[i] = skillMeta{
			Name:        sk.Name,
			Description: sk.Description,
			Category:    sk.Category,
			Agent:       sk.Agent,
			Tags:        sk.Tags,
			SHA256:      sk.SHA256,
			Publisher:   sk.Publisher,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	sk, err := s.Store.GetSkill(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sk == nil {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"name":        sk.Name,
		"description": sk.Description,
		"category":    sk.Category,
		"agent":       sk.Agent,
		"tags":        sk.Tags,
		"content":     sk.Content,
		"sha256":      sk.SHA256,
		"publisher":   sk.Publisher,
	})
}

func (s *Server) handleGetSkillRaw(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	sk, err := s.Store.GetSkill(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sk == nil {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(sk.Content))
}

func strPtr(s string) *string {
	return &s
}
