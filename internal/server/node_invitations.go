package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/yuanshu-ai/yuanshu/internal/enrollment"
	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
)

const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

type nodeInvitationPayload struct {
	Version       int    `json:"version"`
	ServerURL     string `json:"serverUrl"`
	InvitationID  string `json:"invitationId"`
	Secret        string `json:"secret"`
	ExpiresAt     string `json:"expiresAt"`
	CACertificate string `json:"caCertificate,omitempty"`
	CAFingerprint string `json:"caFingerprint,omitempty"`
}

type issuedNodeInvitation struct {
	InvitationID string `json:"invitationId"`
	DisplayName  string `json:"displayName"`
	Secret       string `json:"secret"`
	ShortCode    string `json:"shortCode"`
	ServerURL    string `json:"serverUrl"`
	ExpiresAt    string `json:"expiresAt"`
	Invite       string `json:"invite"`
	InviteURL    string `json:"inviteUrl"`
	QRCode       string `json:"qrCode"`
}

func (s *adminService) createNodeInvitation(w http.ResponseWriter, r *http.Request) {
	session, payload, ok := s.requireHighRisk(w, r)
	if !ok {
		return
	}
	var request struct {
		DisplayName      string `json:"displayName"`
		ExpiresInMinutes int    `json:"expiresInMinutes"`
	}
	if !decodeRaw(payload, &request) {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if request.ExpiresInMinutes == 0 {
		request.ExpiresInMinutes = 10
	}
	result, err := s.issueNodeInvitation(r, session, strings.TrimSpace(request.DisplayName), time.Duration(request.ExpiresInMinutes)*time.Minute)
	if err != nil {
		s.finishMutation(w, r, session, "node_invitation.create", "node_invitation", "new", err)
		return
	}
	if err := s.writeAudit(r.Context(), session, "node_invitation.create", "node_invitation", result.InvitationID, "succeeded", ""); err != nil {
		writeError(w, http.StatusInternalServerError, "audit_failure")
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *adminService) listNodeInvitations(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	items, err := s.store.ListNodeInvitations(r.Context(), session.ownerID, s.clock().UTC())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitations": items})
}

func (s *adminService) cancelNodeInvitation(w http.ResponseWriter, r *http.Request) {
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
	err := s.store.CancelNodeInvitation(r.Context(), session.ownerID, id, s.clock().UTC())
	s.finishMutation(w, r, session, "node_invitation.cancel", "node_invitation", id, err)
}

func (s *adminService) reissueNodeInvitation(w http.ResponseWriter, r *http.Request) {
	session, payload, ok := s.requireHighRisk(w, r)
	if !ok {
		return
	}
	var request struct {
		ExpiresInMinutes int `json:"expiresInMinutes"`
	}
	if !decodeRaw(payload, &request) {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	items, err := s.store.ListNodeInvitations(r.Context(), session.ownerID, s.clock().UTC())
	if err != nil {
		s.finishMutation(w, r, session, "node_invitation.reissue", "node_invitation", r.PathValue("id"), err)
		return
	}
	var displayName string
	for _, item := range items {
		if item.ID == r.PathValue("id") {
			displayName = item.DisplayName
			break
		}
	}
	if displayName == "" {
		s.finishMutation(w, r, session, "node_invitation.reissue", "node_invitation", r.PathValue("id"), serverstore.ErrNotFound)
		return
	}
	_ = s.store.CancelNodeInvitation(r.Context(), session.ownerID, r.PathValue("id"), s.clock().UTC())
	if request.ExpiresInMinutes == 0 {
		request.ExpiresInMinutes = 10
	}
	result, err := s.issueNodeInvitation(r, session, displayName, time.Duration(request.ExpiresInMinutes)*time.Minute)
	if err != nil {
		s.finishMutation(w, r, session, "node_invitation.reissue", "node_invitation", r.PathValue("id"), err)
		return
	}
	if err := s.writeAudit(r.Context(), session, "node_invitation.reissue", "node_invitation", result.InvitationID, "succeeded", ""); err != nil {
		writeError(w, http.StatusInternalServerError, "audit_failure")
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *adminService) issueNodeInvitation(r *http.Request, session *adminSession, displayName string, ttl time.Duration) (issuedNodeInvitation, error) {
	if displayName == "" || len(displayName) > 128 || ttl < time.Minute || ttl > 30*time.Minute {
		return issuedNodeInvitation{}, serverstore.ErrInvalid
	}
	settings, err := s.store.SecuritySettings(r.Context(), session.ownerID)
	if err != nil || !settings.NodeEnrollmentEnabled {
		return issuedNodeInvitation{}, serverstore.ErrConflict
	}
	idValue := make([]byte, 18)
	secretRaw := make([]byte, 32)
	codeRaw := make([]byte, 10)
	if _, err = io.ReadFull(s.random, idValue); err != nil {
		return issuedNodeInvitation{}, err
	}
	if _, err = io.ReadFull(s.random, secretRaw); err != nil {
		return issuedNodeInvitation{}, err
	}
	defer clear(secretRaw)
	if _, err = io.ReadFull(s.random, codeRaw); err != nil {
		return issuedNodeInvitation{}, err
	}
	defer clear(codeRaw)
	id := "inv_" + base64.RawURLEncoding.EncodeToString(idValue)
	secret := base64.RawURLEncoding.EncodeToString(secretRaw)
	code := crockfordCode(codeRaw)
	secretHash, codeHash := sha256.Sum256([]byte(secret)), sha256.Sum256([]byte(code))
	now, expires := s.clock().UTC(), s.clock().UTC().Add(ttl)
	if err = s.store.CreateNodeInvitation(r.Context(), serverstore.CreateNodeInvitation{NodeInvitation: serverstore.NodeInvitation{ID: id, OwnerID: session.ownerID, DisplayName: displayName, Status: "pending", CreatedBy: session.clientID, CreatedAt: now, ExpiresAt: expires}, SecretHash: secretHash[:], CodeHash: codeHash[:]}); err != nil {
		return issuedNodeInvitation{}, err
	}
	serverURL := strings.TrimRight(s.options.PublicURL, "/")
	if serverURL == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		serverURL = scheme + "://" + r.Host
	}
	payload := nodeInvitationPayload{Version: 1, ServerURL: serverURL, InvitationID: id, Secret: secret, ExpiresAt: expires.Format(time.RFC3339Nano)}
	if s.options.Certificate != nil {
		if ca, ok := s.options.Certificate.PublicCACertificate(); ok {
			payload.CACertificate = string(ca)
			payload.CAFingerprint = s.options.Certificate.Status().CAFingerprint
		}
	}
	raw, _ := json.Marshal(payload)
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	inviteURL := serverURL + "/join#" + encoded
	qr, _ := qrcode.Encode(string(raw), qrcode.Medium, 256)
	return issuedNodeInvitation{InvitationID: id, DisplayName: displayName, Secret: secret, ShortCode: code, ServerURL: serverURL, ExpiresAt: payload.ExpiresAt, Invite: string(raw), InviteURL: inviteURL, QRCode: "data:image/png;base64," + base64.StdEncoding.EncodeToString(qr)}, nil
}

func crockfordCode(raw []byte) string {
	value := make([]byte, 16)
	for index := range value {
		value[index] = crockfordAlphabet[int(raw[index%len(raw)]+byte(index*17))%len(crockfordAlphabet)]
	}
	return string(value)
}

type invitationClaimRequest struct {
	InvitationID string `json:"invitationId,omitempty"`
	Secret       string `json:"secret,omitempty"`
	ShortCode    string `json:"shortCode,omitempty"`
	Name         string `json:"name"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Version      string `json:"version"`
	PublicKey    string `json:"publicKey"`
}

func (s *PairingService) claimNodeInvitation(w http.ResponseWriter, r *http.Request) {
	var request invitationClaimRequest
	if !decodeStrict(w, r, &request) {
		return
	}
	publicKey, valid := canonicalBytes(request.PublicKey, 32)
	useCode := request.ShortCode != ""
	proof := request.Secret
	if useCode {
		proof = normalizeCrockford(request.ShortCode)
	}
	proofKey := sha256.Sum256([]byte(request.InvitationID + "\x00" + proof))
	limitKeys := []string{"ip:" + remoteHost(r.RemoteAddr), "invite:" + base64.RawURLEncoding.EncodeToString(proofKey[:12])}
	if !s.invitationLimiter.allowed(limitKeys...) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	if !valid || proof == "" || (useCode && request.InvitationID != "") || (!useCode && request.InvitationID == "") || !validDisplay(request.Name, 128) || !validOS(request.OS) || !validDisplay(request.Arch, 32) || !validVersion(request.Version) {
		s.invitationLimiter.failure(limitKeys...)
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	proofHash := sha256.Sum256([]byte(proof))
	nodeID, err := s.randomID("nod_", 16)
	if err != nil {
		s.invitationLimiter.failure(limitKeys...)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	ownerID, err := s.randomID("own_", 16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	item, err := s.store.ClaimNodeInvitation(r.Context(), serverstore.ClaimNodeInvitation{InvitationID: request.InvitationID, ProofHash: proofHash[:], UseShortCode: useCode, NodeID: nodeID, OwnerID: ownerID, PublicKey: publicKey, Name: request.Name, OS: request.OS, Arch: request.Arch, Version: request.Version, Now: s.clock().UTC()})
	if err != nil {
		if errors.Is(err, serverstore.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "invitation_invalid")
		} else if errors.Is(err, serverstore.ErrConflict) {
			writeError(w, http.StatusConflict, "invitation_unavailable")
		} else {
			writeStoreError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"ownerId": item.OwnerID, "nodeId": nodeID, "status": "active", "fingerprint": enrollment.Fingerprint(publicKey), "platform": request.OS, "arch": request.Arch})
}

func normalizeCrockford(value string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(value))
}
