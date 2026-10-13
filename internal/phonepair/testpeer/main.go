// testpeer is an isolated cross-language protocol fixture; it never opens real wing state.
package main

import (
	"bufio"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/phonepair"
)

type fixture struct {
	mu                        sync.Mutex
	fleet                     bool
	wing                      string
	input                     []string
	stops                     int
	dropInput, inputReplyLost bool
}

func (f *fixture) Execute(_ context.Context, _ string, op, id, input string) (any, error) {
	return f.execute(op, f.wing, id, input)
}
func (f *fixture) ExecuteOrigin(_ context.Context, _ string, op, origin, id, input string) (any, error) {
	if !f.fleet || f.wing != "fixture-main-wing" || origin != "fixture-remote-wing" {
		return nil, errors.New("unknown origin")
	}
	return f.execute(op, origin, id, input)
}
func (f *fixture) execute(op, origin, id, input string) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.fleet {
		switch op {
		case "sessions":
			return []map[string]any{{"id": "fixture-shell", "name": "Disposable shell", "agent": "shell", "status": "idle", "blocked": true, "unread": true}}, nil
		case "read":
			return map[string]any{"output": "fixture output\r\n", "ended": false}, nil
		case "input":
			return map[string]any{"ok": input == "hello\r"}, nil
		default:
			return map[string]bool{"ok": true}, nil
		}
	}
	row := func(id, name, machine, wing, agent string, pin bool) map[string]any {
		return map[string]any{"id": id, "name": name, "machine": machine, "origin_wing_id": wing, "agent": agent, "status": "working", "blocked": !pin, "unread": true, "cwd": "/workspace/wingthing", "pinned": pin, "role": map[bool]string{true: "coordinator", false: ""}[pin]}
	}
	switch op {
	case "sessions":
		if f.wing == "fixture-main-wing" {
			unknown := row("shared", "DNS helper", "forge", "", "codex", false)
			delete(unknown, "origin_wing_id")
			return []map[string]any{row("shared", "QR pairing", "EhrlichMac", f.wing, "claude", false), row("coordinator", "wing", "forge", "fixture-remote-wing", "claude", true), unknown}, nil
		}
		return []map[string]any{row("coordinator", "wing", "forge", f.wing, "claude", true), row("shared", "DNS helper", "forge", f.wing, "codex", false)}, nil
	case "thread":
		var page struct {
			After int64 `json:"after"`
		}
		if json.Unmarshal([]byte(input), &page) != nil {
			return nil, errors.New("bad page")
		}
		events := []map[string]any{{"sequence": int64(1), "type": "message", "role": "assistant", "text": "I checked the session inventory. Your Mac and home server are connected. The DNS helper is ready for your review.", "timestamp": "2026-10-12T21:00:00Z"}}
		for i, text := range f.input {
			events = append(events, map[string]any{"sequence": int64(2 + i*2), "type": "message", "role": "user", "text": strings.TrimSuffix(strings.SplitN(text, "|", 3)[2], "\r")}, map[string]any{"sequence": int64(3 + i*2), "type": "message", "role": "assistant", "text": "I received your direction. I will run the pairing tests first and report the result here."})
		}
		head := int64(len(events))
		filtered := []map[string]any{}
		for _, e := range events {
			if e["sequence"].(int64) > page.After {
				filtered = append(filtered, e)
			}
		}
		return map[string]any{"session_id": id, "agent": "claude", "provider_session_id": "fixture-native", "state": "idle", "status": "idle", "state_source": "claude_hook", "ready": true, "process_alive": true, "cursor": head, "head_cursor": head, "has_more": false, "events": filtered}, nil
	case "read":
		return map[string]any{"screen": fmt.Sprintf("%s / %s\n\nDisposable acceptance session\nReady for your direction.\n\n%s", origin, id, strings.Join(f.input, "\n")), "ended": false}, nil
	case "input":
		f.input = append(f.input, origin+"|"+id+"|"+input)
		if f.dropInput {
			f.dropInput = false
			f.inputReplyLost = true
		}
	case "stop":
		f.stops++
	}
	return map[string]bool{"ok": true}, nil
}
func (*fixture) Close(string) {}

type peer struct {
	manager    *phonepair.Manager
	invitation phonepair.Invitation
	server     *http.Server
	handler    *fixture
	mu         sync.Mutex
	down, drop bool
}

func newPeer(root, wing, name string, fleet bool) (*peer, error) {
	if e := os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	private, _ := ecdh.X25519().GenerateKey(rand.Reader)
	ln, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	handler := &fixture{wing: wing, fleet: fleet}
	manager, e := phonepair.New(root, wing, name, "http://"+ln.Addr().String(), nil, private, handler)
	if e != nil {
		ln.Close()
		return nil, e
	}
	q, e := manager.Create()
	if e != nil {
		ln.Close()
		manager.Close()
		return nil, e
	}
	p := &peer{manager: manager, invitation: q, handler: handler}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /phone", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		down, drop := p.down, p.drop
		p.drop = false
		p.mu.Unlock()
		if down {
			http.Error(w, "fixture route down", 503)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, phonepair.MaxPayload+4096)
		var req phonepair.Request
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		reply := manager.Box(r.Context(), req)
		handler.mu.Lock()
		if handler.inputReplyLost {
			drop = true
			handler.inputReplyLost = false
		}
		handler.mu.Unlock()
		if drop {
			if h, ok := w.(http.Hijacker); ok {
				c, _, err := h.Hijack()
				if err == nil {
					c.Close()
					return
				}
			}
			http.Error(w, "dropped", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reply)
	})
	p.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go p.server.Serve(ln)
	return p, nil
}
func (p *peer) close() { p.server.Close(); p.manager.Close() }
func main() {
	dir, e := os.MkdirTemp("", "wingthing-pair-test-*")
	if e != nil {
		panic(e)
	}
	defer os.RemoveAll(dir)
	fleet := os.Getenv("WT_PAIRING_UI_FIXTURE") == "1"
	main, e := newPeer(dir+"/main", "fixture-main-wing", "EhrlichMac", fleet)
	if e != nil {
		panic(e)
	}
	defer main.close()
	if !fleet {
		json.NewEncoder(os.Stdout).Encode(map[string]string{"link": main.invitation.Link()})
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var err error
			switch scanner.Text() {
			case "allow":
				err = main.manager.Allow(main.invitation.ID)
			case "revoke":
				err = main.manager.Revoke("Bryan's iPhone")
			case "stop":
				return
			default:
				err = fmt.Errorf("unknown fixture control")
			}
			if err != nil {
				json.NewEncoder(os.Stdout).Encode(map[string]string{"error": err.Error()})
			} else {
				json.NewEncoder(os.Stdout).Encode(map[string]bool{"ok": true})
			}
		}
		return
	}
	remote, e := newPeer(dir+"/remote", "fixture-remote-wing", "forge", true)
	if e != nil {
		panic(e)
	}
	defer remote.close()
	ln, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		panic(e)
	}
	tokenBytes := make([]byte, 32)
	rand.Read(tokenBytes)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fixture", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "denied", 403)
			return
		}
		var req struct{ Op, Wing string }
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		p := main
		if req.Wing == "remote" {
			p = remote
		}
		var err error
		var value any = map[string]bool{"ok": true}
		switch req.Op {
		case "allow":
			err = p.manager.Allow(p.invitation.ID)
		case "cancel":
			err = p.manager.Cancel(p.invitation.ID)
		case "revoke":
			err = p.manager.Revoke("Bryan's iPhone")
		case "down":
			p.mu.Lock()
			p.down = true
			p.mu.Unlock()
		case "up":
			p.mu.Lock()
			p.down = false
			p.mu.Unlock()
		case "drop-input":
			p.handler.mu.Lock()
			p.handler.dropInput = true
			p.handler.mu.Unlock()
		case "drop":
			p.mu.Lock()
			p.drop = true
			p.mu.Unlock()
		case "new", "expired", "wrongpin":
			p.invitation, err = p.manager.Create()
			q := p.invitation
			if req.Op == "expired" {
				q.ExpiresAt = time.Now().Add(-time.Second)
			}
			if req.Op == "wrongpin" {
				q.Key = base64.StdEncoding.EncodeToString(make([]byte, 32))
			}
			value = map[string]string{"link": q.Link()}
		case "stats":
			p.handler.mu.Lock()
			value = map[string]any{"inputs": append([]string{}, p.handler.input...), "stops": p.handler.stops, "devices": len(p.manager.Devices())}
			p.handler.mu.Unlock()
		default:
			err = errors.New("unknown fixture control")
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(value)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(ln)
	defer server.Close()
	json.NewEncoder(os.Stdout).Encode(map[string]any{"links": []string{main.invitation.Link(), remote.invitation.Link()}, "control": "http://" + ln.Addr().String() + "/fixture", "token": token})
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if scanner.Text() == "stop" {
			return
		}
	}
}
