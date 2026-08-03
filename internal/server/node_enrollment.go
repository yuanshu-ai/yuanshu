package server

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/enrollment"
	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
)

type createNodeEnrollmentRequest struct {
	CodeHash string `json:"codeHash"`
}
type claimNodeEnrollmentRequest struct {
	Name           string `json:"name"`
	OS             string `json:"os"`
	Version        string `json:"version"`
	PublicKey      string `json:"publicKey"`
	CredentialHash string `json:"credentialHash"`
}
type decideNodeEnrollmentRequest struct {
	Decision  string `json:"decision"`
	Signature string `json:"signature"`
}
type revokeNodeRequest struct {
	IssuedAt  string `json:"issuedAt"`
	Signature string `json:"signature"`
}

func (s *PairingService) createNodeEnrollment(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticateNode(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.hub == nil || !s.hub.NodeConnected(node.OwnerID, node.NodeID) {
		writeError(w, http.StatusConflict, "node_offline")
		return
	}
	settings, err := s.store.SecuritySettings(r.Context(), node.OwnerID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !settings.NodeEnrollmentEnabled {
		writeError(w, http.StatusForbidden, "node_enrollment_disabled")
		return
	}
	var request createNodeEnrollmentRequest
	if !decodeStrict(w, r, &request) {
		return
	}
	hash, ok := canonicalBytes(request.CodeHash, sha256.Size)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	id, err := s.randomID("enr", 18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	now := s.clock().UTC()
	expires := now.Add(enrollment.PairingTTL)
	if err := s.store.CreateNodeEnrollment(r.Context(), serverstore.NodeEnrollment{ID: id, OwnerID: node.OwnerID, IssuerNodeID: node.NodeID, CodeHash: hash, CreatedAt: now, ExpiresAt: expires}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"enrollmentId": id, "expiresAt": expires.Format(time.RFC3339Nano)})
}

func (s *PairingService) pendingNodeEnrollments(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticateNode(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	items, err := s.store.PendingNodeEnrollments(r.Context(), node.NodeID, s.clock().UTC())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	result := make([]map[string]string, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]string{"enrollmentId": item.ID, "candidateNodeId": item.CandidateNodeID, "name": item.Name, "os": item.OS, "version": item.Version, "publicKey": base64.RawURLEncoding.EncodeToString(item.PublicKey), "credentialHash": base64.RawURLEncoding.EncodeToString(item.CredentialHash), "fingerprint": enrollment.Fingerprint(item.PublicKey), "expiresAt": item.ExpiresAt.Format(time.RFC3339Nano)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"enrollments": result})
}

func (s *PairingService) claimNodeEnrollment(w http.ResponseWriter, r *http.Request) {
	secret, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var request claimNodeEnrollmentRequest
	if !decodeStrict(w, r, &request) {
		return
	}
	publicKey, keyOK := canonicalBytes(request.PublicKey, ed25519.PublicKeySize)
	credentialHash, credentialOK := canonicalBytes(request.CredentialHash, sha256.Size)
	if !keyOK || !credentialOK || !validDisplayName(request.Name) || !validOpaque(request.Version, 64) || (request.OS != "windows" && request.OS != "linux" && request.OS != "darwin") {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	digest := sha256.Sum256([]byte(secret))
	now := s.clock().UTC()
	existing, existingErr := s.store.NodeEnrollment(r.Context(), r.PathValue("id"), digest[:], now)
	candidateID := ""
	if existingErr == nil && existing.CandidateNodeID != "" {
		candidateID = existing.CandidateNodeID
	} else if existingErr != nil && existingErr != serverstore.ErrConflict {
		writeStoreError(w, existingErr)
		return
	}
	if candidateID == "" {
		var err error
		candidateID, err = s.randomID("nod", 18)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
	}
	settings, settingsErr := s.store.SecuritySettings(r.Context(), existing.OwnerID)
	if settingsErr != nil {
		writeStoreError(w, settingsErr)
		return
	}
	if !settings.NodeEnrollmentEnabled {
		writeError(w, http.StatusForbidden, "node_enrollment_disabled")
		return
	}
	item, err := s.store.ClaimNodeEnrollment(r.Context(), serverstore.NodeEnrollmentClaim{EnrollmentID: r.PathValue("id"), CandidateNodeID: candidateID, Name: request.Name, OS: request.OS, Version: request.Version, CodeHash: digest[:], PublicKey: publicKey, CredentialHash: credentialHash, Now: now})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": item.Status, "candidateNodeId": item.CandidateNodeID, "fingerprint": enrollment.Fingerprint(publicKey), "expiresAt": item.ExpiresAt.Format(time.RFC3339Nano)})
}

func (s *PairingService) nodeEnrollmentStatus(w http.ResponseWriter, r *http.Request) {
	secret, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	digest := sha256.Sum256([]byte(secret))
	item, err := s.store.NodeEnrollment(r.Context(), r.PathValue("id"), digest[:], s.clock().UTC())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	response := map[string]string{"status": item.Status, "expiresAt": item.ExpiresAt.Format(time.RFC3339Nano)}
	if item.Status == "approved" {
		response["ownerId"] = item.OwnerID
		response["nodeId"] = item.CandidateNodeID
		response["issuerNodeId"] = item.IssuerNodeID
		response["issuerPublicKey"] = base64.RawURLEncoding.EncodeToString(item.IssuerKey)
		response["proof"] = base64.RawURLEncoding.EncodeToString(item.Proof)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *PairingService) decideNodeEnrollment(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticateNode(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var request decideNodeEnrollmentRequest
	if !decodeStrict(w, r, &request) {
		return
	}
	if request.Decision != "accept" && request.Decision != "decline" {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	items, err := s.store.PendingNodeEnrollments(r.Context(), node.NodeID, s.clock().UTC())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var item serverstore.NodeEnrollment
	for _, candidate := range items {
		if candidate.ID == r.PathValue("id") {
			item = candidate
			break
		}
	}
	if item.ID == "" {
		item, err = s.store.IssuerNodeEnrollment(r.Context(), r.PathValue("id"), node.NodeID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
	}
	binding := nodeEnrollmentBinding(item, request.Decision)
	input, err := enrollment.NodeEnrollmentDecisionSigningInput(binding)
	signature, valid := canonicalBytes(request.Signature, ed25519.SignatureSize)
	if err != nil || !valid || !ed25519.Verify(node.PublicKey, input, signature) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	item, err = s.store.ResolveNodeEnrollment(r.Context(), serverstore.NodeEnrollmentResolution{EnrollmentID: item.ID, IssuerNodeID: node.NodeID, Decision: request.Decision, Proof: signature, Now: s.clock().UTC()})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": item.Status})
}

func nodeEnrollmentBinding(item serverstore.NodeEnrollment, decision string) enrollment.NodeEnrollmentDecision {
	return enrollment.NodeEnrollmentDecision{Version: "1", EnrollmentID: item.ID, OwnerID: item.OwnerID, IssuerNodeID: item.IssuerNodeID, CandidateNodeID: item.CandidateNodeID, CandidatePublicKey: base64.RawURLEncoding.EncodeToString(item.PublicKey), CredentialHash: base64.RawURLEncoding.EncodeToString(item.CredentialHash), Name: item.Name, OS: item.OS, NodeVersion: item.Version, Decision: decision, ExpiresAt: item.ExpiresAt.Format(time.RFC3339Nano)}
}

func (s *PairingService) listNodes(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticateNode(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	items, err := s.store.OwnerNodes(r.Context(), node.OwnerID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{"nodeId": item.ID, "name": item.Name, "os": item.OS, "version": item.Version, "status": item.Status, "online": s.hub != nil && s.hub.NodeConnected(item.OwnerID, item.ID), "fingerprint": enrollment.Fingerprint(item.PublicKey)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": result})
}

func (s *PairingService) revokeNode(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticateNode(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var request revokeNodeRequest
	if !decodeStrict(w, r, &request) {
		return
	}
	binding := enrollment.NodeRevocation{Version: "1", OwnerID: node.OwnerID, IssuerNodeID: node.NodeID, TargetNodeID: r.PathValue("id"), IssuedAt: request.IssuedAt}
	input, err := enrollment.NodeRevocationSigningInput(binding)
	signature, valid := canonicalBytes(request.Signature, ed25519.SignatureSize)
	if err != nil || !valid || !fresh(request.IssuedAt, s.clock().UTC()) || !ed25519.Verify(node.PublicKey, input, signature) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := s.store.RevokeNode(r.Context(), node.OwnerID, node.NodeID, binding.TargetNodeID, s.clock().UTC()); err != nil {
		writeStoreError(w, err)
		return
	}
	if s.hub != nil {
		s.hub.DisconnectNode(node.OwnerID, binding.TargetNodeID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *PairingService) controlTrustManifest(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticateNode(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	manifest, err := s.store.ControlTrustManifest(r.Context(), node.OwnerID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	clients := make([]map[string]string, 0, len(manifest.Clients))
	for _, item := range manifest.Clients {
		clients = append(clients, map[string]string{"clientId": item.ID, "keyId": item.KeyID, "publicKey": base64.RawURLEncoding.EncodeToString(item.PublicKey), "status": item.Status})
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": manifest.Revision, "clients": clients})
}
