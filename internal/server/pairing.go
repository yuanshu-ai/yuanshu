package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/enrollment"
	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
)

const maxPairingBytes = 16 << 10

type PairingOptions struct {
	Random io.Reader
	Clock  func() time.Time
}
type PairingService struct {
	store  *serverstore.Store
	hub    *Hub
	random io.Reader
	clock  func() time.Time
}

type createPairingRequest struct {
	CodeHash  string `json:"codeHash"`
	Challenge string `json:"challenge"`
}
type claimPairingRequest struct {
	ClientID  string `json:"clientId"`
	KeyID     string `json:"keyId"`
	Name      string `json:"name"`
	PublicKey string `json:"publicKey"`
}
type decidePairingRequest struct {
	Decision  string `json:"decision"`
	Signature string `json:"signature"`
}
type revokeClientRequest struct {
	NodeID    string `json:"nodeId"`
	KeyID     string `json:"keyId"`
	IssuedAt  string `json:"issuedAt"`
	Signature string `json:"signature"`
}
type rotateCredentialRequest struct {
	NewCredentialHash string `json:"newCredentialHash"`
	IssuedAt          string `json:"issuedAt"`
	Signature         string `json:"signature"`
}

func NewPairingService(local *serverstore.Store, hub *Hub, options PairingOptions) (*PairingService, error) {
	if local == nil {
		return nil, ErrInvalid
	}
	random := options.Random
	if random == nil {
		random = rand.Reader
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	return &PairingService{store: local, hub: hub, random: random, clock: clock}, nil
}

func (s *PairingService) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/control-client-pairings", s.create)
	mux.HandleFunc("GET /v1/control-client-pairings", s.pending)
	mux.HandleFunc("POST /v1/control-client-pairings/{id}/claim", s.claim)
	mux.HandleFunc("GET /v1/control-client-pairings/{id}/status", s.status)
	mux.HandleFunc("POST /v1/control-client-pairings/{id}/decision", s.decide)
	mux.HandleFunc("DELETE /v1/control-clients/{id}", s.revokeClient)
	mux.HandleFunc("POST /v1/nodes/{id}/credential/rotate", s.rotateCredential)
	return mux
}

func (s *PairingService) create(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticateNode(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.hub == nil || !s.hub.NodeConnected(node.OwnerID, node.NodeID) {
		writeError(w, http.StatusConflict, "node_offline")
		return
	}
	var request createPairingRequest
	if !decodeStrict(w, r, &request) {
		return
	}
	codeHash, ok := canonicalBytes(request.CodeHash, 32)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	challenge, ok := canonicalBytes(request.Challenge, 32)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	id, err := s.randomID("pair", 18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	now := s.clock().UTC()
	expires := now.Add(enrollment.PairingTTL)
	err = s.store.CreatePairing(r.Context(), serverstore.Pairing{ID: id, OwnerID: node.OwnerID, NodeID: node.NodeID, CodeHash: codeHash, Challenge: challenge, CreatedAt: now, ExpiresAt: expires})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"pairingId": id, "expiresAt": expires.Format(time.RFC3339Nano)})
}

func (s *PairingService) pending(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticateNode(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	items, err := s.store.NodePairings(r.Context(), node.NodeID, s.clock().UTC())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	result := make([]map[string]string, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]string{"pairingId": item.ID, "clientId": item.ClientID, "keyId": item.KeyID, "name": item.ClientName, "publicKey": base64.RawURLEncoding.EncodeToString(item.PublicKey), "fingerprint": enrollment.Fingerprint(item.PublicKey), "expiresAt": item.ExpiresAt.Format(time.RFC3339Nano)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"pairings": result})
}

func (s *PairingService) claim(w http.ResponseWriter, r *http.Request) {
	secret, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var request claimPairingRequest
	if !decodeStrict(w, r, &request) {
		return
	}
	publicKey, ok := canonicalBytes(request.PublicKey, ed25519.PublicKeySize)
	if !ok || !validOpaque(request.ClientID, 128) || !validOpaque(request.KeyID, 128) || !validDisplayName(request.Name) {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	digest := sha256.Sum256([]byte(secret))
	item, err := s.store.ClaimPairing(r.Context(), serverstore.PairingClaim{PairingID: r.PathValue("id"), ClientID: request.ClientID, KeyID: request.KeyID, ClientName: request.Name, CodeHash: digest[:], PublicKey: publicKey, Now: s.clock().UTC()})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": item.Status, "fingerprint": enrollment.Fingerprint(publicKey)})
}

func (s *PairingService) status(w http.ResponseWriter, r *http.Request) {
	secret, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	digest := sha256.Sum256([]byte(secret))
	item, err := s.store.Pairing(r.Context(), r.PathValue("id"), digest[:], s.clock().UTC())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	response := map[string]string{"status": item.Status, "expiresAt": item.ExpiresAt.Format(time.RFC3339Nano)}
	if item.Status == "approved" {
		response["ownerId"] = item.OwnerID
		response["nodeId"] = item.NodeID
		response["nodePublicKey"] = base64.RawURLEncoding.EncodeToString(item.NodePublicKey)
		response["proof"] = base64.RawURLEncoding.EncodeToString(item.Proof)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *PairingService) decide(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticateNode(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var request decidePairingRequest
	if !decodeStrict(w, r, &request) {
		return
	}
	items, err := s.store.NodePairings(r.Context(), node.NodeID, s.clock().UTC())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var item serverstore.Pairing
	for _, candidate := range items {
		if candidate.ID == r.PathValue("id") {
			item = candidate
			break
		}
	}
	if item.ID == "" {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	binding := enrollment.PairingDecision{Version: "1", PairingID: item.ID, OwnerID: item.OwnerID, NodeID: item.NodeID, ClientID: item.ClientID, KeyID: item.KeyID, PublicKey: base64.RawURLEncoding.EncodeToString(item.PublicKey), Name: item.ClientName, Decision: request.Decision, ExpiresAt: item.ExpiresAt.Format(time.RFC3339Nano)}
	input, err := enrollment.PairingDecisionSigningInput(binding)
	signature, valid := canonicalBytes(request.Signature, ed25519.SignatureSize)
	if err != nil || !valid || !ed25519.Verify(node.PublicKey, input, signature) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	item, err = s.store.ResolvePairing(r.Context(), serverstore.PairingResolution{PairingID: item.ID, NodeID: node.NodeID, Decision: request.Decision, Proof: signature, Now: s.clock().UTC()})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": item.Status})
}

func (s *PairingService) revokeClient(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticateNode(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var request revokeClientRequest
	if !decodeStrict(w, r, &request) {
		return
	}
	binding := enrollment.ClientRevocation{Version: "1", OwnerID: node.OwnerID, NodeID: request.NodeID, ClientID: r.PathValue("id"), KeyID: request.KeyID, IssuedAt: request.IssuedAt}
	input, err := enrollment.ClientRevocationSigningInput(binding)
	signature, valid := canonicalBytes(request.Signature, ed25519.SignatureSize)
	if err != nil || request.NodeID != node.NodeID || !valid || !fresh(request.IssuedAt, s.clock().UTC()) || !ed25519.Verify(node.PublicKey, input, signature) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := s.store.RevokeControlClient(r.Context(), node.OwnerID, binding.ClientID, s.clock().UTC()); err != nil {
		writeStoreError(w, err)
		return
	}
	if s.hub != nil {
		s.hub.DisconnectControl(node.OwnerID, binding.ClientID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *PairingService) rotateCredential(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticateNode(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var request rotateCredentialRequest
	if !decodeStrict(w, r, &request) {
		return
	}
	hash, valid := canonicalBytes(request.NewCredentialHash, sha256.Size)
	binding := enrollment.CredentialRotation{Version: "1", OwnerID: node.OwnerID, NodeID: node.NodeID, NewCredentialHash: request.NewCredentialHash, IssuedAt: request.IssuedAt}
	input, err := enrollment.CredentialRotationSigningInput(binding)
	signature, sigOK := canonicalBytes(request.Signature, ed25519.SignatureSize)
	if err != nil || r.PathValue("id") != node.NodeID || !valid || !sigOK || !fresh(request.IssuedAt, s.clock().UTC()) || !ed25519.Verify(node.PublicKey, input, signature) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := s.store.RotateNodeCredential(r.Context(), node.OwnerID, node.NodeID, hash, s.clock().UTC()); err != nil {
		writeStoreError(w, err)
		return
	}
	if s.hub != nil {
		s.hub.DisconnectNode(node.OwnerID, node.NodeID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *PairingService) authenticateNode(r *http.Request) (serverstore.NodeSession, bool) {
	nodeID := r.Header.Get("X-Yuanshu-Node-ID")
	credential, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok || !validOpaque(nodeID, 128) {
		return serverstore.NodeSession{}, false
	}
	record, err := s.store.NodeSession(r.Context(), nodeID)
	if err != nil || record.Status != "active" {
		return serverstore.NodeSession{}, false
	}
	digest := sha256.Sum256([]byte(credential))
	return record, len(record.CredentialHash) == sha256.Size && subtle.ConstantTimeCompare(digest[:], record.CredentialHash) == 1
}

func (s *PairingService) randomID(prefix string, size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(s.random, value); err != nil {
		return "", errors.New("pairing random generation failed")
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(value), nil
}
func canonicalBytes(value string, size int) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	return raw, err == nil && len(raw) == size && base64.RawURLEncoding.EncodeToString(raw) == value
}
func fresh(value string, now time.Time) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !parsed.Before(now.Add(-30*time.Second)) && !parsed.After(now.Add(30*time.Second))
}
func validDisplayName(value string) bool {
	return strings.TrimSpace(value) != "" && validOpaque(value, 128)
}

func decodeStrict(w http.ResponseWriter, r *http.Request, target any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "invalid_request")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPairingBytes)
	decoder := jsonNewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil || ensureJSONEnd(decoder) != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_request")
		}
		return false
	}
	return true
}

func jsonNewDecoder(reader io.Reader) *json.Decoder {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	return decoder
}
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, serverstore.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, serverstore.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, serverstore.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, serverstore.ErrConflict):
		writeError(w, http.StatusConflict, "conflict")
	default:
		writeError(w, http.StatusInternalServerError, "internal")
	}
}
