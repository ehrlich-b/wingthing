package phonepair

import (
	"context"
	"crypto/ecdh"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/ehrlich-b/wingthing/internal/pake"
	"github.com/grandcat/zeroconf"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type Control struct {
	Port   int    `json:"port"`
	Token  string `json:"token"`
	WingID string `json:"wing_id"`
	Key    string `json:"key"`
	Name   string `json:"name"`
}
type Server struct {
	Manager     *Manager
	http        *http.Server
	listener    net.Listener
	controlPath string
	discovery   *zeroconf.Server
	closeOnce   sync.Once
}

func Listen(ctx context.Context, dir, wingID, name string, relay func() string, private *ecdh.PrivateKey, handler Handler) (*Server, error) {
	if !pake.Available {
		return nil, nil
	}
	ln, e := net.Listen("tcp4", "0.0.0.0:0")
	if e != nil {
		return nil, e
	}
	port := ln.Addr().(*net.TCPAddr).Port
	local := "http://" + net.JoinHostPort(lanAddress(), strconv.Itoa(port))
	m, e := New(dir, wingID, name, local, relay, private, handler)
	if e != nil {
		ln.Close()
		return nil, e
	}
	direct := directEndpoints(port)
	m.endpoints = func() []string { return append([]string(nil), direct...) }
	control := Control{port, randomID(), wingID, m.key, name}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /phone", func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if decode(w, r, &req) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		writeJSON(w, m.Box(r.Context(), req))
	})
	mux.HandleFunc("POST /admin", func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+control.Token)) != 1 {
			http.Error(w, "denied", 403)
			return
		}
		var req struct{ Op, ID, Name string }
		if decode(w, r, &req) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		var result any
		var e error
		switch req.Op {
		case "create":
			result, e = m.Create()
		case "status":
			result, e = m.Status(req.ID)
		case "allow":
			e = m.Allow(req.ID)
		case "cancel":
			e = m.Cancel(req.ID)
		case "devices":
			result = m.Devices()
		case "revoke":
			e = m.Revoke(req.Name)
		default:
			e = failure("denied")
		}
		if e != nil {
			writeJSON(w, map[string]string{"error": e.Error()})
			return
		}
		if result == nil {
			result = map[string]bool{"ok": true}
		}
		writeJSON(w, result)
	})
	s := &Server{Manager: m, http: &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 4096}, listener: ln, controlPath: filepath.Join(dir, "phone-control.json")}
	if e = saveControl(s.controlPath, encode(control)); e != nil {
		ln.Close()
		return nil, e
	}
	s.discovery, e = zeroconf.Register(DiscoveryName(wingID), "_wingthing-phone._tcp", "local.", port, []string{"v=1", "wing_id=" + wingID, "key=" + m.key}, nil)
	if e != nil {
		log.Printf("wing: Bonjour phone discovery unavailable; scanned IP endpoints still work: %v", e)
	}
	go func() { _ = s.http.Serve(ln) }()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.Close()
				return
			case <-ticker.C:
				m.mu.Lock()
				m.clean()
				m.mu.Unlock()
			}
		}
	}()
	return s, nil
}
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		if s.discovery != nil {
			s.discovery.Shutdown()
		}
		_ = s.http.Close()
		s.Manager.Close()
		_ = os.Remove(s.controlPath)
	})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxPayload+4096)
	defer r.Body.Close()
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
func lanAddress() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ip, n, e := net.ParseCIDR(a.String())
		if e == nil && n != nil && ip.To4() != nil && ip.IsPrivate() {
			return ip.String()
		}
	}
	return "127.0.0.1"
}

func saveControl(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".phone-control-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	return err
}
