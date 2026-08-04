package server

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"time"

	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
)

const nodeSessionAuthorization = "YuanshuNodeSession "

type nodeHTTPSession struct {
	OwnerID      string
	NodeID       string
	ConnectionID string
	ExpiresAt    time.Time
}

func (h *Hub) issueNodeSession(ownerID, nodeID, connectionID string) (string, time.Time, error) {
	if !validOpaque(ownerID, 128) || !validOpaque(nodeID, 128) || !validOpaque(connectionID, 128) {
		return "", time.Time{}, ErrInvalid
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(h.random, raw); err != nil {
		clear(raw)
		return "", time.Time{}, errors.New("node session generation failed")
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	clear(raw)
	digest := sha256.Sum256([]byte(token))
	expiresAt := h.clock().UTC().Add(h.nodeSessionTTL)
	h.nodeSessionMu.Lock()
	h.cleanupNodeSessionsLocked(h.clock().UTC())
	h.nodeSessions[digest] = nodeHTTPSession{OwnerID: ownerID, NodeID: nodeID, ConnectionID: connectionID, ExpiresAt: expiresAt}
	h.nodeSessionMu.Unlock()
	return token, expiresAt, nil
}

func (h *Hub) registerNodeSession(ownerID, nodeID, connectionID string, raw []byte, expiresAt time.Time) error {
	if !validOpaque(ownerID, 128) || !validOpaque(nodeID, 128) || !validOpaque(connectionID, 128) || len(raw) != 32 || !expiresAt.After(h.clock().UTC()) {
		return ErrInvalid
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	h.nodeSessionMu.Lock()
	h.cleanupNodeSessionsLocked(h.clock().UTC())
	h.nodeSessions[digest] = nodeHTTPSession{OwnerID: ownerID, NodeID: nodeID, ConnectionID: connectionID, ExpiresAt: expiresAt}
	h.nodeSessionMu.Unlock()
	return nil
}

func (h *Hub) AuthenticateNodeRequest(request *http.Request) (serverNodeSession, bool) {
	if request == nil {
		return serverNodeSession{}, false
	}
	nodeID := request.Header.Get("X-Yuanshu-Node-ID")
	token, ok := nodeSessionToken(request.Header.Get("Authorization"))
	if !ok || !validOpaque(nodeID, 128) {
		return serverNodeSession{}, false
	}
	digest := sha256.Sum256([]byte(token))
	h.nodeSessionMu.Lock()
	h.cleanupNodeSessionsLocked(h.clock().UTC())
	session, exists := h.nodeSessions[digest]
	h.nodeSessionMu.Unlock()
	if !exists || session.NodeID != nodeID {
		return serverNodeSession{}, false
	}
	connection := h.node(session.OwnerID, session.NodeID)
	if connection == nil || connection.connectionID != session.ConnectionID {
		return serverNodeSession{}, false
	}
	record, err := h.store.NodeSession(request.Context(), nodeID)
	if err != nil || record.Status != "active" || record.OwnerID != session.OwnerID {
		return serverNodeSession{}, false
	}
	return serverNodeSession{NodeSession: record, ConnectionID: session.ConnectionID, Digest: digest}, true
}

type serverNodeSession struct {
	serverstore.NodeSession
	ConnectionID string
	Digest       [sha256.Size]byte
}

func (h *Hub) RefreshNodeSession(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	session, ok := h.AuthenticateNodeRequest(request)
	if !ok {
		writeError(writer, http.StatusUnauthorized, "node_session_invalid")
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 1024)
	defer request.Body.Close()
	var body struct{}
	if !decodeEmptyJSONObject(request, &body) {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	token, expiresAt, err := h.issueNodeSession(session.OwnerID, session.NodeID, session.ConnectionID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal")
		return
	}
	h.nodeSessionMu.Lock()
	delete(h.nodeSessions, session.Digest)
	h.nodeSessionMu.Unlock()
	writeJSON(writer, http.StatusOK, map[string]string{"sessionToken": token, "sessionExpiresAt": expiresAt.Format(time.RFC3339Nano)})
}

func decodeEmptyJSONObject(request *http.Request, target any) bool {
	decoder := jsonNewDecoder(request.Body)
	return decoder.Decode(target) == nil && ensureJSONEnd(decoder) == nil
}

func (h *Hub) invalidateNodeSessions(connection *hubConnection) {
	if connection == nil || connection.role != "node" {
		return
	}
	h.invalidateNodeSessionScope(connection.ownerID, connection.subjectID, connection.connectionID)
}

func (h *Hub) invalidateNodeSessionScope(ownerID, nodeID, connectionID string) {
	h.nodeSessionMu.Lock()
	for digest, session := range h.nodeSessions {
		if session.OwnerID == ownerID && session.NodeID == nodeID && session.ConnectionID == connectionID {
			delete(h.nodeSessions, digest)
		}
	}
	h.nodeSessionMu.Unlock()
}

func (h *Hub) cleanupNodeSessionsLocked(now time.Time) {
	for digest, session := range h.nodeSessions {
		if !now.Before(session.ExpiresAt) {
			delete(h.nodeSessions, digest)
		}
	}
}

func nodeSessionToken(header string) (string, bool) {
	if len(header) <= len(nodeSessionAuthorization) || header[:len(nodeSessionAuthorization)] != nodeSessionAuthorization {
		return "", false
	}
	value := header[len(nodeSessionAuthorization):]
	raw, err := base64.RawURLEncoding.DecodeString(value)
	valid := err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == value
	clear(raw)
	return value, valid
}
