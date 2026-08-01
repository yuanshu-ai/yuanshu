// Package relay implements the loopback-only HTTPS/WSS relay used by AC-005.
// It routes opaque PoC frames and has no dependency on an Agent Runtime.
package relay

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/yuanshu-ai/yuanshu/internal/poc/protocol"
	"github.com/yuanshu-ai/yuanshu/internal/poc/transport"
	"github.com/yuanshu-ai/yuanshu/internal/poc/ui"
)

const sessionCookie = "yuanshu_poc_session"

type Server struct {
	nodeToken string
	mu        sync.Mutex
	node      transport.Endpoint
	webs      map[transport.Endpoint]struct{}
	sessions  map[string]time.Time
}

func New(nodeToken string) (*Server, error) {
	if len(nodeToken) < 32 {
		return nil, errors.New("YUANSHU_POC_NODE_TOKEN must contain at least 32 bytes")
	}
	return &Server{nodeToken: nodeToken, webs: make(map[transport.Endpoint]struct{}), sessions: make(map[string]time.Time)}, nil
}

func ValidateLoopbackListen(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return errors.New("PoC Server must bind an explicit loopback IP")
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.index)
	mux.HandleFunc("GET /poc/app.js", s.app)
	mux.HandleFunc("POST /poc/session", s.createSession)
	mux.HandleFunc("GET /poc/web", s.web)
	mux.HandleFunc("GET /poc/node", s.acceptNode)
	return securityHeaders(mux)
}

// AttachNode installs the Server side of a StandaloneTransport and routes it
// through the same opaque-frame hub used by /poc/node.
func (s *Server) AttachNode(ctx context.Context, ep transport.Endpoint) error {
	s.mu.Lock()
	if s.node != nil {
		s.mu.Unlock()
		return errors.New("a PoC Node is already attached")
	}
	s.node = ep
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			if s.node == ep {
				s.node = nil
			}
			s.mu.Unlock()
			_ = ep.Close()
		}()
		for {
			f, err := ep.Receive(ctx)
			if err != nil {
				return
			}
			s.broadcast(ctx, f)
		}
	}()
	return nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if !requestLoopback(r) {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(ui.Index)
}
func (s *Server) app(w http.ResponseWriter, r *http.Request) {
	if !requestLoopback(r) {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write(ui.AppJS)
}
func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	if !requestLoopback(r) || r.Header.Get("Origin") != origin(r) || r.Header.Get("X-Yuanshu-Session") != "create" {
		http.Error(w, "session rejected", http.StatusForbidden)
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	token := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(10 * time.Minute)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/poc/web", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 600})
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) web(w http.ResponseWriter, r *http.Request) {
	if !requestLoopback(r) || r.Header.Get("Origin") != origin(r) || !s.consumeSession(r) {
		http.Error(w, "web session rejected", http.StatusForbidden)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ep := transport.WebSocketEndpoint(conn, protocol.MaxControlBytes, protocol.MaxEventBytes)
	s.mu.Lock()
	s.webs[ep] = struct{}{}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.webs, ep); s.mu.Unlock(); _ = ep.Close() }()
	for {
		f, err := ep.Receive(r.Context())
		if err != nil {
			return
		}
		s.mu.Lock()
		node := s.node
		s.mu.Unlock()
		if node == nil {
			e, _ := protocol.New(protocol.ErrorEvent, f.RequestID, "poc-node", map[string]string{"code": "node_offline"})
			_ = ep.Send(r.Context(), e)
			continue
		}
		if err := node.Send(r.Context(), f); err != nil {
			return
		}
	}
}
func (s *Server) acceptNode(w http.ResponseWriter, r *http.Request) {
	want := "Bearer " + s.nodeToken
	got := r.Header.Get("Authorization")
	if len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		http.Error(w, "node rejected", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("Origin") != "" {
		http.Error(w, "node origin rejected", http.StatusForbidden)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ep := transport.WebSocketEndpoint(conn, protocol.MaxEventBytes, protocol.MaxControlBytes)
	s.mu.Lock()
	if s.node != nil {
		s.mu.Unlock()
		_ = ep.Close()
		return
	}
	s.node = ep
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.node == ep {
			s.node = nil
		}
		s.mu.Unlock()
		_ = ep.Close()
	}()
	for {
		f, err := ep.Receive(r.Context())
		if err != nil {
			return
		}
		s.broadcast(r.Context(), f)
	}
}
func (s *Server) broadcast(ctx context.Context, f protocol.Frame) {
	s.mu.Lock()
	webs := make([]transport.Endpoint, 0, len(s.webs))
	for w := range s.webs {
		webs = append(webs, w)
	}
	s.mu.Unlock()
	for _, w := range webs {
		_ = w.Send(ctx, f)
	}
}
func (s *Server) consumeSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.sessions[c.Value]
	delete(s.sessions, c.Value)
	return ok && time.Now().Before(until)
}
func requestLoopback(r *http.Request) bool {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && net.ParseIP(h) != nil && net.ParseIP(h).IsLoopback()
}
func origin(r *http.Request) string { return "https://" + r.Host }
