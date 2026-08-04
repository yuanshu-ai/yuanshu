package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gowebpki/jcs"
	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
)

const (
	adminSessionDomain = "yuanshu-admin-session-v1\x00"
	adminActionDomain  = "yuanshu-admin-action-v1\x00"
	adminCookieName    = "yuanshu_admin"
	maxAdminBody       = 32 << 10
)

type adminHandlerOptions struct {
	Enabled        bool
	PublicURL      string
	Listen         string
	WebEnabled     bool
	TLSConfigured  bool
	AllowedOrigins []string
	SessionIdle    time.Duration
	SessionMax     time.Duration
	AuditRetention time.Duration
	Random         io.Reader
	Clock          func() time.Time
	StartedAt      time.Time
	DatabasePath   string
	ConfigRevision string
	TLSSAN         []string
	TLSNotAfter    time.Time
	TLSFingerprint string
}

type adminChallenge struct {
	Version     string `json:"version"`
	Type        string `json:"type"`
	ChallengeID string `json:"challengeId"`
	ClientID    string `json:"clientId"`
	KeyID       string `json:"keyId"`
	Origin      string `json:"origin"`
	Nonce       string `json:"nonce"`
	ExpiresAt   string `json:"expiresAt"`
}

type adminActionChallenge struct {
	Version     string `json:"version"`
	Type        string `json:"type"`
	ChallengeID string `json:"challengeId"`
	ClientID    string `json:"clientId"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	BodyDigest  string `json:"bodyDigest"`
	Nonce       string `json:"nonce"`
	ExpiresAt   string `json:"expiresAt"`
}

type pendingAdminChallenge struct {
	value     adminChallenge
	publicKey ed25519.PublicKey
}
type pendingActionChallenge struct {
	value            adminActionChallenge
	sessionTokenHash string
}
type adminSession struct {
	tokenHash string
	ownerID   string
	clientID  string
	keyID     string
	csrf      string
	createdAt time.Time
	lastSeen  time.Time
	expiresAt time.Time
}

type cachedAdminResponse struct {
	bodyDigest [sha256.Size]byte
	status     int
	header     http.Header
	body       []byte
	expiresAt  time.Time
	ready      chan struct{}
}

type adminService struct {
	store            *serverstore.Store
	hub              *Hub
	options          adminHandlerOptions
	random           io.Reader
	clock            func() time.Time
	mu               sync.Mutex
	challenges       map[string]pendingAdminChallenge
	actionChallenges map[string]pendingActionChallenge
	sessions         map[string]*adminSession
	idempotency      map[string]*cachedAdminResponse
	loginLimiter     *attemptLimiter
}

func newAdminService(store *serverstore.Store, hub *Hub, options adminHandlerOptions) (*adminService, error) {
	if store == nil || hub == nil {
		return nil, ErrInvalid
	}
	if options.SessionIdle <= 0 {
		options.SessionIdle = 30 * time.Minute
	}
	if options.SessionMax <= 0 {
		options.SessionMax = 8 * time.Hour
	}
	if options.AuditRetention <= 0 {
		options.AuditRetention = 90 * 24 * time.Hour
	}
	if options.StartedAt.IsZero() {
		options.StartedAt = time.Now().UTC()
	}
	randomSource := options.Random
	if randomSource == nil {
		randomSource = rand.Reader
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	return &adminService{store: store, hub: hub, options: options, random: randomSource, clock: clock, challenges: map[string]pendingAdminChallenge{}, actionChallenges: map[string]pendingActionChallenge{}, sessions: map[string]*adminSession{}, idempotency: map[string]*cachedAdminResponse{}, loginLimiter: newAttemptLimiter(clock)}, nil
}

func (s *adminService) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/admin/auth/challenge", s.issueChallenge)
	mux.HandleFunc("POST /v1/admin/auth/session", s.createSession)
	mux.HandleFunc("GET /v1/admin/auth/session", s.readSession)
	mux.HandleFunc("DELETE /v1/admin/auth/session", s.deleteSession)
	mux.HandleFunc("POST /v1/admin/auth/action-challenge", s.issueActionChallenge)
	mux.HandleFunc("GET /v1/admin/overview", s.overview)
	mux.HandleFunc("GET /v1/admin/nodes", s.nodes)
	mux.HandleFunc("GET /v1/admin/nodes/{id}", s.nodeDetail)
	mux.HandleFunc("GET /v1/admin/control-clients", s.controlClients)
	mux.HandleFunc("GET /v1/admin/access-requests", s.accessRequests)
	mux.HandleFunc("GET /v1/admin/leases", s.leases)
	mux.HandleFunc("GET /v1/admin/audit", s.auditList)
	mux.HandleFunc("GET /v1/admin/config", s.configView)
	mux.HandleFunc("GET /v1/admin/diagnostics", s.diagnostics)
	mux.HandleFunc("POST /v1/admin/nodes/{id}/revoke", s.revokeNode)
	mux.HandleFunc("POST /v1/admin/control-clients/{id}/revoke", s.revokeControlClient)
	mux.HandleFunc("POST /v1/admin/pairings/{id}/cancel", s.cancelPairing)
	mux.HandleFunc("POST /v1/admin/node-enrollments/{id}/cancel", s.cancelEnrollment)
	mux.HandleFunc("POST /v1/admin/leases/release", s.releaseLease)
	mux.HandleFunc("PUT /v1/admin/security/admission", s.updateAdmission)
	return s.idempotencyMiddleware(noStore(mux))
}

func (s *adminService) idempotencyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !adminMutationPath(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		session, ok := s.requireMutation(w, r)
		if !ok {
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxAdminBody+1))
		if err != nil || len(raw) > maxAdminBody {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		digest := sha256.Sum256(raw)
		cacheKey := session.ownerID + "\x00" + session.clientID + "\x00" + r.Header.Get("Idempotency-Key")
		now := s.clock().UTC()
		s.mu.Lock()
		s.cleanupLocked(now)
		cached, found := s.idempotency[cacheKey]
		if found {
			if cached.bodyDigest != digest {
				s.mu.Unlock()
				writeError(w, http.StatusConflict, "idempotency_conflict")
				return
			}
			ready := cached.ready
			s.mu.Unlock()
			if ready != nil {
				select {
				case <-ready:
				case <-r.Context().Done():
					writeError(w, http.StatusRequestTimeout, "request_canceled")
					return
				}
			}
			s.mu.Lock()
			cached, found = s.idempotency[cacheKey]
			if !found {
				s.mu.Unlock()
				writeError(w, http.StatusServiceUnavailable, "operation_unknown")
				return
			}
			status, header, body := cached.status, cached.header.Clone(), append([]byte(nil), cached.body...)
			s.mu.Unlock()
			copyHeaders(w.Header(), header)
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return
		}
		cached = &cachedAdminResponse{bodyDigest: digest, expiresAt: now.Add(10 * time.Minute), ready: make(chan struct{})}
		s.idempotency[cacheKey] = cached
		s.mu.Unlock()
		capture := &adminResponseCapture{header: make(http.Header), status: http.StatusOK}
		next.ServeHTTP(capture, r)
		s.mu.Lock()
		if capture.status < http.StatusInternalServerError {
			cached.status = capture.status
			cached.header = capture.header.Clone()
			cached.body = append([]byte(nil), capture.body.Bytes()...)
			close(cached.ready)
			cached.ready = nil
		} else {
			delete(s.idempotency, cacheKey)
			close(cached.ready)
		}
		s.mu.Unlock()
		copyHeaders(w.Header(), capture.header)
		w.WriteHeader(capture.status)
		_, _ = w.Write(capture.body.Bytes())
	})
}

type adminResponseCapture struct {
	header      http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func (w *adminResponseCapture) Header() http.Header { return w.header }
func (w *adminResponseCapture) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status, w.wroteHeader = status, true
	}
}
func (w *adminResponseCapture) Write(value []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(value)
}
func copyHeaders(target, source http.Header) {
	for key, values := range source {
		target[key] = append([]string(nil), values...)
	}
}
func adminMutationPath(method, path string) bool {
	if method == http.MethodPut && path == "/v1/admin/security/admission" {
		return true
	}
	if method != http.MethodPost {
		return false
	}
	return strings.HasPrefix(path, "/v1/admin/nodes/") || strings.HasPrefix(path, "/v1/admin/control-clients/") || strings.HasPrefix(path, "/v1/admin/pairings/") || strings.HasPrefix(path, "/v1/admin/node-enrollments/") || path == "/v1/admin/leases/release"
}

func (s *adminService) issueChallenge(w http.ResponseWriter, r *http.Request) {
	if !s.validOrigin(r) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if !s.loginLimiter.allowed() {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	var request struct {
		ClientID string `json:"clientId"`
		KeyID    string `json:"keyId"`
	}
	if !decodeAdminJSON(w, r, &request) {
		s.loginLimiter.failure()
		return
	}
	if !validOpaque(request.ClientID, 128) || !validOpaque(request.KeyID, 128) {
		s.loginLimiter.failure()
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	record, err := s.store.ControlClientSession(r.Context(), request.ClientID)
	if err != nil || record.Status != "active" || record.KeyID != request.KeyID || len(record.PublicKey) != ed25519.PublicKeySize {
		s.loginLimiter.failure()
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := s.randomValue(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	nonce, err := s.randomValue(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	now := s.clock().UTC()
	challenge := adminChallenge{Version: "1", Type: "admin.challenge", ChallengeID: "adm_" + id, ClientID: record.ClientID, KeyID: record.KeyID, Origin: r.Header.Get("Origin"), Nonce: nonce, ExpiresAt: now.Add(2 * time.Minute).Format(time.RFC3339Nano)}
	s.mu.Lock()
	s.cleanupLocked(now)
	s.challenges[challenge.ChallengeID] = pendingAdminChallenge{value: challenge, publicKey: append(ed25519.PublicKey(nil), record.PublicKey...)}
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, challenge)
}

func (s *adminService) createSession(w http.ResponseWriter, r *http.Request) {
	if !s.validOrigin(r) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	var request struct {
		ChallengeID string `json:"challengeId"`
		Signature   string `json:"signature"`
	}
	if !decodeAdminJSON(w, r, &request) {
		return
	}
	s.mu.Lock()
	pending, ok := s.challenges[request.ChallengeID]
	delete(s.challenges, request.ChallengeID)
	s.mu.Unlock()
	now := s.clock().UTC()
	signature, sigOK := canonicalBytes(request.Signature, ed25519.SignatureSize)
	input, inputErr := adminSigningInput(adminSessionDomain, pending.value)
	if !ok || inputErr != nil || !sigOK || !now.Before(mustParseTime(pending.value.ExpiresAt)) || pending.value.Origin != r.Header.Get("Origin") || !ed25519.Verify(pending.publicKey, input, signature) {
		s.loginLimiter.failure()
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	record, err := s.store.ControlClientSession(r.Context(), pending.value.ClientID)
	if err != nil || record.Status != "active" || record.KeyID != pending.value.KeyID {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	token, err := s.randomValue(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	csrf, err := s.randomValue(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	hash := tokenDigest(token)
	session := &adminSession{tokenHash: hash, ownerID: record.OwnerID, clientID: record.ClientID, keyID: record.KeyID, csrf: csrf, createdAt: now, lastSeen: now, expiresAt: now.Add(s.options.SessionMax)}
	s.mu.Lock()
	s.sessions[hash] = session
	s.mu.Unlock()
	_ = s.store.TouchControlClient(r.Context(), record.OwnerID, record.ClientID, now)
	http.SetCookie(w, &http.Cookie{Name: adminCookieName, Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: int(s.options.SessionMax.Seconds())})
	writeJSON(w, http.StatusCreated, map[string]any{"clientId": record.ClientID, "csrfToken": csrf, "expiresAt": session.expiresAt.Format(time.RFC3339Nano)})
}

func (s *adminService) readSession(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clientId": session.clientID, "csrfToken": session.csrf, "expiresAt": session.expiresAt.Format(time.RFC3339Nano)})
}
func (s *adminService) deleteSession(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireMutation(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	delete(s.sessions, session.tokenHash)
	s.mu.Unlock()
	s.clearCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *adminService) issueActionChallenge(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireMutation(w, r)
	if !ok {
		return
	}
	var request struct {
		Method     string `json:"method"`
		Path       string `json:"path"`
		BodyDigest string `json:"bodyDigest"`
	}
	if !decodeAdminJSON(w, r, &request) {
		return
	}
	if !highRiskAdminAction(request.Method, request.Path) || !canonicalDigest(request.BodyDigest) {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	id, err := s.randomValue(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	nonce, err := s.randomValue(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	now := s.clock().UTC()
	challenge := adminActionChallenge{Version: "1", Type: "admin.action", ChallengeID: "act_" + id, ClientID: session.clientID, Method: request.Method, Path: request.Path, BodyDigest: request.BodyDigest, Nonce: nonce, ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano)}
	s.mu.Lock()
	s.actionChallenges[challenge.ChallengeID] = pendingActionChallenge{value: challenge, sessionTokenHash: session.tokenHash}
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, challenge)
}

func (s *adminService) overview(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	counts, err := s.store.AdminCounts(r.Context(), session.ownerID, s.clock().UTC())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	connections := s.hub.OwnerConnections(session.ownerID)
	nodes, controls := 0, 0
	for _, item := range connections {
		if item.Role == "node" {
			nodes++
		} else {
			controls++
		}
	}
	size := int64(0)
	if info, err := os.Stat(s.options.DatabasePath); err == nil {
		size = info.Size()
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "uptimeSeconds": int64(s.clock().UTC().Sub(s.options.StartedAt).Seconds()), "build": serverBuildMetadata(), "database": map[string]any{"schemaVersion": serverstore.CurrentSchemaVersion, "quickCheck": "ok", "sizeBytes": size}, "connections": map[string]int{"nodes": nodes, "controlClients": controls}, "counts": counts, "tls": s.tlsView(), "backup": s.backupView(r.Context())})
}

func (s *adminService) nodes(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	items, err := s.store.OwnerNodes(r.Context(), session.ownerID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	online := connectionSet(s.hub.OwnerConnections(session.ownerID), "node")
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{"id": item.ID, "name": item.Name, "os": item.OS, "version": item.Version, "status": item.Status, "online": online[item.ID], "createdAt": item.CreatedAt.Format(time.RFC3339Nano), "lastSeenAt": timeValue(item.LastSeenAt)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": result})
}

func (s *adminService) nodeDetail(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	nodeID := r.PathValue("id")
	items, err := s.store.OwnerNodes(r.Context(), session.ownerID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var selected *serverstore.Node
	for index := range items {
		if items[index].ID == nodeID {
			selected = &items[index]
			break
		}
	}
	if selected == nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	detail, found := s.hub.OwnerNodeDetail(session.ownerID, nodeID)
	if !found {
		detail = HubNodeDetail{NodeID: nodeID, Online: false, RelayStatus: "offline"}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node": map[string]any{
			"id": selected.ID, "name": selected.Name, "os": selected.OS, "version": selected.Version,
			"status": selected.Status, "lastSeenAt": timeValue(selected.LastSeenAt), "runtime": detail,
		},
	})
}

func (s *adminService) controlClients(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	items, err := s.store.ControlClients(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	online := connectionSet(s.hub.OwnerConnections(session.ownerID), "control")
	result := make([]map[string]any, 0)
	for _, item := range items {
		if item.OwnerID != session.ownerID {
			continue
		}
		result = append(result, map[string]any{"id": item.ID, "name": item.Name, "status": item.Status, "online": online[item.ID], "current": item.ID == session.clientID, "createdAt": item.CreatedAt.Format(time.RFC3339Nano), "lastSeenAt": timeValue(item.LastSeenAt), "revokedAt": timeValue(item.RevokedAt)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"controlClients": result})
}

func (s *adminService) accessRequests(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	items, err := s.store.AdminAccessRequests(r.Context(), session.ownerID, s.clock().UTC())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": items})
}
func (s *adminService) leases(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	items, err := s.store.AdminLeases(r.Context(), session.ownerID, s.clock().UTC())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"leases": items})
}
func (s *adminService) auditList(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	limit := 50
	items, err := s.store.ListAdminAudit(r.Context(), session.ownerID, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": items})
}

func (s *adminService) configView(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	settings, err := s.store.SecuritySettings(r.Context(), session.ownerID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.redactedConfig(settings))
}
func (s *adminService) diagnostics(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	counts, err := s.store.AdminCounts(r.Context(), session.ownerID, s.clock().UTC())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": "ready", "build": serverBuildMetadata(), "schemaVersion": serverstore.CurrentSchemaVersion, "quickCheck": "ok", "uptimeSeconds": int64(s.clock().UTC().Sub(s.options.StartedAt).Seconds()), "webEnabled": s.options.WebEnabled, "adminEnabled": s.options.Enabled, "tls": s.tlsView(), "counts": counts, "configRevision": s.options.ConfigRevision})
}

func serverBuildMetadata() map[string]string {
	result := map[string]string{"goVersion": runtime.Version()}
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			result["version"] = info.Main.Version
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if setting.Value != "" {
					result["revision"] = setting.Value
				}
			case "vcs.modified":
				result["modified"] = setting.Value
			}
		}
	}
	return result
}

type actionProof struct {
	ChallengeID string `json:"challengeId"`
	Signature   string `json:"signature"`
}
type actionEnvelope struct {
	Request json.RawMessage `json:"request"`
	Proof   actionProof     `json:"proof"`
}

func (s *adminService) revokeNode(w http.ResponseWriter, r *http.Request) {
	session, payload, ok := s.requireHighRisk(w, r)
	if !ok {
		return
	}
	var request struct{}
	if !decodeRaw(payload, &request) {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	id := r.PathValue("id")
	err := s.store.AdminRevokeNode(r.Context(), session.ownerID, id, s.clock().UTC())
	if err == nil {
		s.hub.DisconnectNode(session.ownerID, id)
	}
	s.finishMutation(w, r, session, "node.revoke", "node", id, err)
}
func (s *adminService) revokeControlClient(w http.ResponseWriter, r *http.Request) {
	session, payload, ok := s.requireHighRisk(w, r)
	if !ok {
		return
	}
	var request struct{}
	if !decodeRaw(payload, &request) {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	id := r.PathValue("id")
	err := s.store.AdminRevokeControlClient(r.Context(), session.ownerID, id, s.clock().UTC())
	if err == nil {
		s.hub.DisconnectControl(session.ownerID, id)
		s.closeClientSessions(session.ownerID, id)
	}
	s.finishMutation(w, r, session, "control_client.revoke", "control_client", id, err)
}
func (s *adminService) cancelPairing(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireMutation(w, r)
	if !ok {
		return
	}
	if !decodeEmpty(w, r) {
		return
	}
	id := r.PathValue("id")
	err := s.store.CancelAdminAccessRequest(r.Context(), session.ownerID, "control_client", id, s.clock().UTC())
	s.finishMutation(w, r, session, "pairing.cancel", "pairing", id, err)
}
func (s *adminService) cancelEnrollment(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireMutation(w, r)
	if !ok {
		return
	}
	if !decodeEmpty(w, r) {
		return
	}
	id := r.PathValue("id")
	err := s.store.CancelAdminAccessRequest(r.Context(), session.ownerID, "node", id, s.clock().UTC())
	s.finishMutation(w, r, session, "node_enrollment.cancel", "node_enrollment", id, err)
}

func (s *adminService) releaseLease(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireMutation(w, r)
	if !ok {
		return
	}
	var request struct {
		NodeID        string `json:"nodeId"`
		WorkspaceID   string `json:"workspaceId"`
		ThreadID      string `json:"threadId"`
		ExpectedEpoch int64  `json:"expectedEpoch"`
	}
	if !decodeAdminJSON(w, r, &request) {
		return
	}
	scope := serverstore.LeaseScope{OwnerID: session.ownerID, NodeID: request.NodeID, WorkspaceID: request.WorkspaceID, ThreadID: request.ThreadID}
	record, err := s.store.AdminReleaseLease(r.Context(), scope, request.ExpectedEpoch, s.clock().UTC())
	if err == nil {
		_ = s.hub.broadcastLeaseChange(r.Context(), record, correlationID(r))
	}
	resource := digestResource(request.NodeID + "\x00" + request.WorkspaceID + "\x00" + request.ThreadID)
	s.finishMutation(w, r, session, "lease.release", "lease", resource, err)
}

func (s *adminService) updateAdmission(w http.ResponseWriter, r *http.Request) {
	session, payload, ok := s.requireHighRisk(w, r)
	if !ok {
		return
	}
	var request struct {
		ControlPairingEnabled bool  `json:"controlPairingEnabled"`
		NodeEnrollmentEnabled bool  `json:"nodeEnrollmentEnabled"`
		BaseRevision          int64 `json:"baseRevision"`
	}
	if !decodeRaw(payload, &request) {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	settings, err := s.store.UpdateSecuritySettings(r.Context(), session.ownerID, session.clientID, request.ControlPairingEnabled, request.NodeEnrollmentEnabled, request.BaseRevision, s.clock().UTC())
	if err != nil {
		s.finishMutation(w, r, session, "security.admission.update", "security", "admission", err)
		return
	}
	if err = s.writeAudit(r.Context(), session, "security.admission.update", "security", "admission", "succeeded", ""); err != nil {
		writeError(w, http.StatusInternalServerError, "audit_failure")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *adminService) requireHighRisk(w http.ResponseWriter, r *http.Request) (*adminSession, json.RawMessage, bool) {
	session, ok := s.requireMutation(w, r)
	if !ok {
		return nil, nil, false
	}
	var envelope actionEnvelope
	if !decodeAdminJSON(w, r, &envelope) || len(envelope.Request) == 0 {
		return nil, nil, false
	}
	digest, err := canonicalJSONDigest(envelope.Request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return nil, nil, false
	}
	s.mu.Lock()
	pending, found := s.actionChallenges[envelope.Proof.ChallengeID]
	delete(s.actionChallenges, envelope.Proof.ChallengeID)
	s.mu.Unlock()
	signature, sigOK := canonicalBytes(envelope.Proof.Signature, ed25519.SignatureSize)
	input, inputErr := adminSigningInput(adminActionDomain, pending.value)
	record, storeErr := s.store.ControlClientSession(r.Context(), session.clientID)
	if !found || pending.sessionTokenHash != session.tokenHash || pending.value.Method != r.Method || pending.value.Path != r.URL.Path || pending.value.BodyDigest != digest || !s.clock().UTC().Before(mustParseTime(pending.value.ExpiresAt)) || !sigOK || inputErr != nil || storeErr != nil || record.Status != "active" || !ed25519.Verify(record.PublicKey, input, signature) {
		writeError(w, http.StatusUnauthorized, "action_proof_invalid")
		return nil, nil, false
	}
	return session, envelope.Request, true
}

func (s *adminService) requireSession(w http.ResponseWriter, r *http.Request) (*adminSession, bool) {
	cookie, err := r.Cookie(adminCookieName)
	if err != nil || cookie.Value == "" {
		writeError(w, http.StatusUnauthorized, "admin_auth_required")
		return nil, false
	}
	hash := tokenDigest(cookie.Value)
	now := s.clock().UTC()
	s.mu.Lock()
	stored := s.sessions[hash]
	if stored != nil && (now.After(stored.expiresAt) || now.Sub(stored.lastSeen) > s.options.SessionIdle) {
		delete(s.sessions, hash)
		stored = nil
	}
	var session adminSession
	found := stored != nil
	if found {
		stored.lastSeen = now
		session = *stored
	}
	s.mu.Unlock()
	if !found {
		s.clearCookie(w, r)
		writeError(w, http.StatusUnauthorized, "admin_auth_required")
		return nil, false
	}
	record, err := s.store.ControlClientSession(r.Context(), session.clientID)
	if err != nil || record.Status != "active" || record.OwnerID != session.ownerID || record.KeyID != session.keyID {
		s.closeClientSessions(session.ownerID, session.clientID)
		s.clearCookie(w, r)
		writeError(w, http.StatusUnauthorized, "admin_auth_required")
		return nil, false
	}
	return &session, true
}
func (s *adminService) requireMutation(w http.ResponseWriter, r *http.Request) (*adminSession, bool) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return nil, false
	}
	if !s.validOrigin(r) || r.Header.Get("X-Yuanshu-CSRF") != session.csrf || !validOpaque(r.Header.Get("Idempotency-Key"), 128) {
		writeError(w, http.StatusForbidden, "mutation_protection_failed")
		return nil, false
	}
	return session, true
}

func (s *adminService) finishMutation(w http.ResponseWriter, r *http.Request, session *adminSession, action, resourceType, resource string, err error) {
	if err != nil {
		code, status := adminStoreError(err)
		_ = s.writeAudit(r.Context(), session, action, resourceType, resource, "rejected", code)
		writeError(w, status, code)
		return
	}
	if err := s.writeAudit(r.Context(), session, action, resourceType, resource, "succeeded", ""); err != nil {
		writeError(w, http.StatusInternalServerError, "audit_failure")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *adminService) writeAudit(ctx context.Context, session *adminSession, action, resourceType, resource, result, code string) error {
	id, err := s.randomValue(18)
	if err != nil {
		return err
	}
	if resource != "admission" && !strings.HasPrefix(resource, "sha256:") {
		resource = digestResource(resource)
	}
	item := serverstore.AdminAudit{ID: "aud_" + id, OwnerID: session.ownerID, ActorClientID: session.clientID, Action: action, ResourceType: resourceType, ResourceRef: resource, Result: result, ErrorCode: code, CorrelationID: correlationIDFromContext(), CreatedAt: s.clock().UTC()}
	if err := s.store.SaveAdminAudit(ctx, item); err != nil {
		return err
	}
	return s.store.PurgeAdminAudit(ctx, s.clock().UTC().Add(-s.options.AuditRetention))
}

func (s *adminService) redactedConfig(settings serverstore.SecuritySettings) map[string]any {
	return map[string]any{"listen": s.options.Listen, "publicUrl": s.options.PublicURL, "allowedControlOrigins": append([]string(nil), s.options.AllowedOrigins...), "webEnabled": s.options.WebEnabled, "adminEnabled": s.options.Enabled, "dataDirConfigured": s.options.DatabasePath != "", "tls": s.tlsView(), "configRevision": s.options.ConfigRevision, "admission": map[string]any{"controlPairingEnabled": settings.ControlPairingEnabled, "nodeEnrollmentEnabled": settings.NodeEnrollmentEnabled, "revision": settings.Revision, "updatedAt": settings.UpdatedAt.Format(time.RFC3339Nano)}}
}
func (s *adminService) tlsView() map[string]any {
	result := map[string]any{"configured": s.options.TLSConfigured}
	if len(s.options.TLSSAN) > 0 {
		result["san"] = append([]string(nil), s.options.TLSSAN...)
	}
	if !s.options.TLSNotAfter.IsZero() {
		result["notAfter"] = s.options.TLSNotAfter.Format(time.RFC3339)
		if warning := certificateExpiryWarning(s.clock().UTC(), s.options.TLSNotAfter); warning != "" {
			result["expiryWarning"] = warning
		}
	}
	if s.options.TLSFingerprint != "" {
		result["fingerprint"] = s.options.TLSFingerprint
	}
	return result
}

func (s *adminService) backupView(ctx context.Context) map[string]any {
	directory := filepath.Join(filepath.Dir(s.options.DatabasePath), "backups")
	items, err := listBackupArchives(directory)
	if err != nil || len(items) == 0 {
		return map[string]any{"available": false, "integrity": "unavailable", "operation": "local_cli_only"}
	}
	latest := items[0]
	integrity := "valid"
	if err := inspectBackupArchive(ctx, filepath.Join(directory, latest.Name()), filepath.Dir(s.options.DatabasePath)); err != nil {
		integrity = "invalid"
	}
	return map[string]any{"available": true, "lastBackupAt": latest.ModTime().UTC().Format(time.RFC3339Nano), "sizeBytes": latest.Size(), "integrity": integrity, "operation": "local_cli_only"}
}

func adminSigningInput(domain string, value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalid
	}
	canonical, err := jcs.Transform(encoded)
	if err != nil {
		return nil, ErrInvalid
	}
	return append([]byte(domain), canonical...), nil
}
func canonicalJSONDigest(raw json.RawMessage) (string, error) {
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return base64.RawURLEncoding.EncodeToString(digest[:]), nil
}
func canonicalDigest(value string) bool {
	raw, ok := canonicalBytes(value, sha256.Size)
	return ok && len(raw) == sha256.Size
}
func digestResource(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:16])
}
func tokenDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
func (s *adminService) randomValue(size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(s.random, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
func (s *adminService) validOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	expected := ""
	if s.options.PublicURL != "" {
		expected = controlOrigin(s.options.PublicURL)
	}
	if expected == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		expected = scheme + "://" + r.Host
	}
	return origin == expected
}
func (s *adminService) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: adminCookieName, Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}
func (s *adminService) closeClientSessions(ownerID, clientID string) {
	s.mu.Lock()
	for key, item := range s.sessions {
		if item.ownerID == ownerID && item.clientID == clientID {
			delete(s.sessions, key)
		}
	}
	s.mu.Unlock()
}
func (s *adminService) cleanupLocked(now time.Time) {
	for id, item := range s.challenges {
		if !now.Before(mustParseTime(item.value.ExpiresAt)) {
			delete(s.challenges, id)
		}
	}
	for id, item := range s.actionChallenges {
		if !now.Before(mustParseTime(item.value.ExpiresAt)) {
			delete(s.actionChallenges, id)
		}
	}
	for key, item := range s.idempotency {
		if item.ready == nil && !now.Before(item.expiresAt) {
			delete(s.idempotency, key)
		}
	}
}
func decodeAdminJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "invalid_request")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || ensureJSONEnd(decoder) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
}
func decodeRaw(raw json.RawMessage, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && ensureJSONEnd(decoder) == nil
}
func decodeEmpty(w http.ResponseWriter, r *http.Request) bool {
	var request struct{}
	return decodeAdminJSON(w, r, &request)
}
func highRiskAdminAction(method, path string) bool {
	return (method == http.MethodPost && (strings.HasPrefix(path, "/v1/admin/nodes/") || strings.HasPrefix(path, "/v1/admin/control-clients/"))) || (method == http.MethodPut && path == "/v1/admin/security/admission")
}
func connectionSet(items []HubConnectionSnapshot, role string) map[string]bool {
	result := map[string]bool{}
	for _, item := range items {
		if item.Role == role {
			result[item.SubjectID] = true
		}
	}
	return result
}
func timeValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.Format(time.RFC3339Nano)
}
func correlationID(r *http.Request) string {
	value := r.Header.Get("X-Correlation-ID")
	if validOpaque(value, 128) {
		return value
	}
	return correlationIDFromContext()
}
func correlationIDFromContext() string {
	sum := sha256.Sum256([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	return "cor_" + base64.RawURLEncoding.EncodeToString(sum[:12])
}
func adminStoreError(err error) (string, int) {
	switch {
	case errors.Is(err, serverstore.ErrInvalid):
		return "invalid_request", http.StatusBadRequest
	case errors.Is(err, serverstore.ErrNotFound):
		return "not_found", http.StatusNotFound
	case errors.Is(err, serverstore.ErrConflict):
		return "conflict", http.StatusConflict
	case errors.Is(err, serverstore.ErrUnauthorized):
		return "unauthorized", http.StatusUnauthorized
	default:
		return "internal", http.StatusInternalServerError
	}
}

func sortedConnections(items []HubConnectionSnapshot) []HubConnectionSnapshot {
	sort.Slice(items, func(i, j int) bool { return items[i].ConnectedAt.Before(items[j].ConnectedAt) })
	return items
}
