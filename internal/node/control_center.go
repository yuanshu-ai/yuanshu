package node

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/node/webassets"
)

const (
	controlCenterBootstrapTTL = time.Minute
	controlCenterSessionTTL   = 15 * time.Minute
	controlCenterMaxBody      = 64 << 10
)

type controlCenter struct {
	mu       sync.Mutex
	status   func() Status
	manage   func(context.Context, localRequest) localResponse
	assets   fs.FS
	listener net.Listener
	server   *http.Server
	host     string
	boots    map[string]time.Time
	sessions map[string]time.Time
	closed   bool
}

func newControlCenter(status func() Status, manage func(context.Context, localRequest) localResponse) (*controlCenter, error) {
	if status == nil || manage == nil {
		return nil, errors.New("node control center is unavailable")
	}
	assets, err := webassets.FS()
	if err != nil {
		return nil, errors.New("node control center assets are unavailable")
	}
	if _, err := fs.Stat(assets, "index.html"); err != nil {
		return nil, errors.New("node control center entry is unavailable")
	}
	return &controlCenter{status: status, manage: manage, assets: assets, boots: map[string]time.Time{}, sessions: map[string]time.Time{}}, nil
}

func (c *controlCenter) Open(ctx context.Context) (string, error) {
	if ctx == nil || ctx.Err() != nil {
		return "", context.Canceled
	}
	token, err := randomControlCenterToken()
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", errors.New("node control center is closed")
	}
	if c.listener == nil {
		if err := c.startLocked(); err != nil {
			return "", err
		}
	}
	c.boots[token] = time.Now().UTC().Add(controlCenterBootstrapTTL)
	return "http://" + c.host + "/#" + token, nil
}

func (c *controlCenter) startLocked() error {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("node control center loopback is unavailable")
	}
	c.listener = listener
	c.host = listener.Addr().String()
	c.server = &http.Server{Handler: c, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	server := c.server
	go func() { _ = server.Serve(listener) }()
	go c.expire(server)
	return nil
}

func (c *controlCenter) expire(server *http.Server) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now().UTC()
		c.mu.Lock()
		if c.closed || c.server != server {
			c.mu.Unlock()
			return
		}
		for token, expires := range c.boots {
			if !expires.After(now) {
				delete(c.boots, token)
			}
		}
		for token, lastSeen := range c.sessions {
			if now.Sub(lastSeen) >= controlCenterSessionTTL {
				delete(c.sessions, token)
			}
		}
		idle := len(c.boots) == 0 && len(c.sessions) == 0
		if idle {
			c.listener, c.server, c.host = nil, nil, ""
		}
		c.mu.Unlock()
		if idle {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = server.Shutdown(ctx)
			cancel()
			return
		}
	}
}

func (c *controlCenter) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	c.mu.Lock()
	host := c.host
	c.mu.Unlock()
	setControlCenterHeaders(writer)
	if request.Host != host || strings.Contains(request.Host, "@") {
		http.Error(writer, `{"error":"invalid_host"}`, http.StatusForbidden)
		return
	}
	if request.URL.Path == "/api/v1/session" {
		c.exchangeSession(writer, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/api/") {
		session, authorized := c.authorize(request)
		if !authorized {
			writeControlCenterJSON(writer, http.StatusUnauthorized, map[string]string{"error": "session_required"})
			return
		}
		switch request.URL.Path {
		case "/api/v1/overview":
			c.serveOverview(writer, request)
		case "/api/v1/action":
			c.serveAction(writer, request, session)
		default:
			writeControlCenterJSON(writer, http.StatusNotFound, map[string]string{"error": "not_found"})
		}
		return
	}
	c.serveAsset(writer, request)
}

func (c *controlCenter) exchangeSession(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.Header.Get("Origin") != "http://"+request.Host || request.Header.Get("Content-Type") != "application/json" {
		writeControlCenterJSON(writer, http.StatusForbidden, map[string]string{"error": "session_rejected"})
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if !decodeControlCenterJSON(writer, request, &input) {
		return
	}
	now := time.Now().UTC()
	c.mu.Lock()
	expires, exists := c.boots[input.Token]
	if exists {
		delete(c.boots, input.Token)
	}
	c.mu.Unlock()
	if !exists || !expires.After(now) {
		writeControlCenterJSON(writer, http.StatusUnauthorized, map[string]string{"error": "session_expired"})
		return
	}
	session, err := randomControlCenterToken()
	if err != nil {
		writeControlCenterJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "session_unavailable"})
		return
	}
	c.mu.Lock()
	c.sessions[session] = now
	c.mu.Unlock()
	writeControlCenterJSON(writer, http.StatusOK, map[string]string{"session": session})
}

func (c *controlCenter) authorize(request *http.Request) (string, bool) {
	if request.Header.Get("Origin") != "" && request.Header.Get("Origin") != "http://"+request.Host {
		return "", false
	}
	const prefix = "YuanshuLocal "
	authorization := request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(authorization, prefix)
	now := time.Now().UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	lastSeen, ok := c.sessions[token]
	if !ok || now.Sub(lastSeen) >= controlCenterSessionTTL {
		delete(c.sessions, token)
		return "", false
	}
	c.sessions[token] = now
	return token, true
}

func (c *controlCenter) serveOverview(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeControlCenterJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	result := struct {
		Status        Status                    `json:"status"`
		Config        map[string]any            `json:"config,omitempty"`
		ConfigChanges []ConfigChangeSummary     `json:"configChanges,omitempty"`
		Pairings      []PairingCandidate        `json:"pairings,omitempty"`
		Clients       []TrustedClientSummary    `json:"clients,omitempty"`
		Enrollments   []NodeEnrollmentCandidate `json:"enrollments,omitempty"`
		Devices       []DeviceSummary           `json:"devices,omitempty"`
		Setup         *SetupView                `json:"setup,omitempty"`
	}{Status: c.status()}
	if response := c.manage(request.Context(), localRequest{Protocol: localProtocol, Command: "config_show"}); response.OK {
		result.Config = response.Config
	}
	if response := c.manage(request.Context(), localRequest{Protocol: localProtocol, Command: "config_pending"}); response.OK {
		result.ConfigChanges = response.ConfigChanges
	}
	if response := c.manage(request.Context(), localRequest{Protocol: localProtocol, Command: "pairing_list"}); response.OK {
		result.Pairings = response.Pairings
	}
	if response := c.manage(request.Context(), localRequest{Protocol: localProtocol, Command: "client_list"}); response.OK {
		result.Clients = response.Clients
	}
	if response := c.manage(request.Context(), localRequest{Protocol: localProtocol, Command: "enrollment_list"}); response.OK {
		result.Enrollments = response.Enrollments
	}
	if response := c.manage(request.Context(), localRequest{Protocol: localProtocol, Command: "device_list"}); response.OK {
		result.Devices = response.Devices
	}
	if response := c.manage(request.Context(), localRequest{Protocol: localProtocol, Command: "setup_status"}); response.OK {
		result.Setup = response.Setup
	}
	writeControlCenterJSON(writer, http.StatusOK, result)
}

func (c *controlCenter) serveAction(writer http.ResponseWriter, request *http.Request, session string) {
	if request.Method != http.MethodPost || request.Header.Get("Origin") != "http://"+request.Host || request.Header.Get("Content-Type") != "application/json" {
		writeControlCenterJSON(writer, http.StatusForbidden, map[string]string{"error": "action_rejected"})
		return
	}
	var input localRequest
	if !decodeControlCenterJSON(writer, request, &input) {
		return
	}
	if !controlCenterCommand(input.Command) {
		writeControlCenterJSON(writer, http.StatusBadRequest, map[string]string{"error": "unsupported_command"})
		return
	}
	input.Protocol = localProtocol
	input.localSession = session
	response := c.manage(request.Context(), input)
	if !response.OK {
		status := http.StatusConflict
		if response.Error == "unsupported_command" {
			status = http.StatusBadRequest
		}
		writeControlCenterJSON(writer, status, response)
		return
	}
	writeControlCenterJSON(writer, http.StatusOK, response)
}

func (c *controlCenter) serveAsset(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		writeControlCenterJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	cleaned := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
	if cleaned == "." || cleaned == "" {
		cleaned = "index.html"
	}
	if strings.Contains(cleaned, "..") {
		writeControlCenterJSON(writer, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if _, err := fs.Stat(c.assets, cleaned); err != nil {
		cleaned = "index.html"
	}
	if cleaned == "index.html" {
		writer.Header().Set("Cache-Control", "no-store")
	} else {
		writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	content, err := fs.ReadFile(c.assets, cleaned)
	if err != nil {
		writeControlCenterJSON(writer, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if contentType := mime.TypeByExtension(path.Ext(cleaned)); contentType != "" {
		writer.Header().Set("Content-Type", contentType)
	}
	writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodGet {
		_, _ = writer.Write(content)
	}
}

func (c *controlCenter) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	server := c.server
	c.server, c.listener, c.host = nil, nil, ""
	clear(c.boots)
	clear(c.sessions)
	c.mu.Unlock()
	if server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}

func controlCenterCommand(value string) bool {
	switch value {
	case "reload", "autostart_set", "pairing_create", "pairing_list", "client_list", "enrollment_list", "device_list",
		"config_show", "config_update", "config_pending", "setup_pick", "setup_test", "setup_complete":
		return true
	default:
		return false
	}
}

func randomControlCenterToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("node control center token is unavailable")
	}
	result := base64.RawURLEncoding.EncodeToString(value)
	clear(value)
	return result, nil
}

func decodeControlCenterJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, controlCenterMaxBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeControlCenterJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeControlCenterJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return false
	}
	return true
}

func writeControlCenterJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func setControlCenterHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	writer.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), usb=(), serial=()")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
}
