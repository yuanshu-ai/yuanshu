package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
)

func TestAdminSignedSessionOverviewAndAdmissionProof(t *testing.T) {
	service, local, bootstrapSecret := openServerService(t)
	nodePublic, _, _ := ed25519.GenerateKey(nil)
	claim := validClaimRequest()
	claim.PublicKey = base64.RawURLEncoding.EncodeToString(nodePublic)
	bound, _, err := service.Claim(context.Background(), bootstrapSecret, claim)
	if err != nil {
		t.Fatal(err)
	}

	clientPublic, clientPrivate, _ := ed25519.GenerateKey(nil)
	codeHash := sha256.Sum256([]byte("admin-pairing-code"))
	pairing := serverstore.Pairing{ID: "pair_admin", OwnerID: bound.OwnerID, NodeID: bound.NodeID, CodeHash: codeHash[:], Challenge: bytes.Repeat([]byte{1}, 32), CreatedAt: fixedServerNow, ExpiresAt: fixedServerNow.Add(5 * time.Minute)}
	if err := local.CreatePairing(context.Background(), pairing); err != nil {
		t.Fatal(err)
	}
	if _, err := local.ClaimPairing(context.Background(), serverstore.PairingClaim{PairingID: pairing.ID, ClientID: "cli_admin", KeyID: "key_admin", ClientName: "Admin browser", CodeHash: codeHash[:], PublicKey: clientPublic, Now: fixedServerNow}); err != nil {
		t.Fatal(err)
	}
	if _, err := local.ResolvePairing(context.Background(), serverstore.PairingResolution{PairingID: pairing.ID, NodeID: bound.NodeID, Decision: "accept", Proof: bytes.Repeat([]byte{2}, 64), Now: fixedServerNow}); err != nil {
		t.Fatal(err)
	}

	hub, err := NewHub(local, HubOptions{Clock: func() time.Time { return fixedServerNow }})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	admin, err := newAdminService(local, hub, adminHandlerOptions{Enabled: true, Listen: "127.0.0.1:9527", WebEnabled: true, Clock: func() time.Time { return fixedServerNow }, Random: bytes.NewReader(bytes.Repeat([]byte{0x41}, 4096)), StartedAt: fixedServerNow.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://example.test"

	challengeResponse := adminJSONRequest(t, admin.Handler(), http.MethodPost, origin+"/v1/admin/auth/challenge", map[string]any{"clientId": "cli_admin", "keyId": "key_admin"}, nil, "", origin, "")
	if challengeResponse.Code != http.StatusCreated {
		t.Fatalf("challenge status=%d body=%s", challengeResponse.Code, challengeResponse.Body.String())
	}
	var challenge adminChallenge
	if err := json.Unmarshal(challengeResponse.Body.Bytes(), &challenge); err != nil {
		t.Fatal(err)
	}
	input, err := adminSigningInput(adminSessionDomain, challenge)
	if err != nil {
		t.Fatal(err)
	}
	sessionResponse := adminJSONRequest(t, admin.Handler(), http.MethodPost, origin+"/v1/admin/auth/session", map[string]any{"challengeId": challenge.ChallengeID, "signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(clientPrivate, input))}, nil, "", origin, "")
	if sessionResponse.Code != http.StatusCreated {
		t.Fatalf("session status=%d body=%s", sessionResponse.Code, sessionResponse.Body.String())
	}
	var sessionValue struct {
		CSRFToken string `json:"csrfToken"`
	}
	_ = json.Unmarshal(sessionResponse.Body.Bytes(), &sessionValue)
	cookies := sessionResponse.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || sessionValue.CSRFToken == "" {
		t.Fatalf("session cookie=%v body=%s", cookies, sessionResponse.Body.String())
	}

	overview := adminJSONRequest(t, admin.Handler(), http.MethodGet, origin+"/v1/admin/overview", nil, cookies[0], "", "", "")
	if overview.Code != http.StatusOK || bytes.Contains(overview.Body.Bytes(), clientPrivate) {
		t.Fatalf("overview status=%d body=%s", overview.Code, overview.Body.String())
	}
	if !bytes.Contains(overview.Body.Bytes(), []byte(`"operation":"local_cli_only"`)) {
		t.Fatalf("overview backup guidance missing: %s", overview.Body.String())
	}
	nodeDetail := adminJSONRequest(t, admin.Handler(), http.MethodGet, origin+"/v1/admin/nodes/"+bound.NodeID, nil, cookies[0], "", "", "")
	if nodeDetail.Code != http.StatusOK || !bytes.Contains(nodeDetail.Body.Bytes(), []byte(`"relayStatus":"offline"`)) || bytes.Contains(nodeDetail.Body.Bytes(), clientPrivate) {
		t.Fatalf("node detail status=%d body=%s", nodeDetail.Code, nodeDetail.Body.String())
	}
	unknownNode := adminJSONRequest(t, admin.Handler(), http.MethodGet, origin+"/v1/admin/nodes/other-owner-node", nil, cookies[0], "", "", "")
	if unknownNode.Code != http.StatusNotFound {
		t.Fatalf("unknown node status=%d body=%s", unknownNode.Code, unknownNode.Body.String())
	}

	payload, _ := json.Marshal(map[string]any{"controlPairingEnabled": false, "nodeEnrollmentEnabled": true, "baseRevision": float64(1)})
	digest, err := canonicalJSONDigest(payload)
	if err != nil {
		t.Fatal(err)
	}
	actionResponse := adminJSONRequest(t, admin.Handler(), http.MethodPost, origin+"/v1/admin/auth/action-challenge", map[string]any{"method": "PUT", "path": "/v1/admin/security/admission", "bodyDigest": digest}, cookies[0], sessionValue.CSRFToken, origin, "action-challenge")
	if actionResponse.Code != http.StatusCreated {
		t.Fatalf("action status=%d body=%s", actionResponse.Code, actionResponse.Body.String())
	}
	var action adminActionChallenge
	_ = json.Unmarshal(actionResponse.Body.Bytes(), &action)
	actionInput, _ := adminSigningInput(adminActionDomain, action)
	var payloadValue map[string]any
	_ = json.Unmarshal(payload, &payloadValue)
	envelope := map[string]any{"request": payloadValue, "proof": map[string]any{"challengeId": action.ChallengeID, "signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(clientPrivate, actionInput))}}
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			responses <- adminJSONRequest(t, admin.Handler(), http.MethodPut, origin+"/v1/admin/security/admission", envelope, cookies[0], sessionValue.CSRFToken, origin, "admission-update")
		}()
	}
	updated, duplicate := <-responses, <-responses
	if updated.Code != http.StatusOK || !bytes.Contains(updated.Body.Bytes(), []byte(`"controlPairingEnabled":false`)) {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	if duplicate.Code != http.StatusOK || duplicate.Body.String() != updated.Body.String() {
		t.Fatalf("concurrent idempotent status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	replayed := adminJSONRequest(t, admin.Handler(), http.MethodPut, origin+"/v1/admin/security/admission", envelope, cookies[0], sessionValue.CSRFToken, origin, "admission-replay")
	if replayed.Code != http.StatusUnauthorized {
		t.Fatalf("replay status=%d body=%s", replayed.Code, replayed.Body.String())
	}
	auditResponse := adminJSONRequest(t, admin.Handler(), http.MethodGet, origin+"/v1/admin/audit", nil, cookies[0], "", "", "")
	if auditResponse.Code != http.StatusOK || bytes.Contains(auditResponse.Body.Bytes(), []byte(bound.NodeID)) || bytes.Contains(auditResponse.Body.Bytes(), clientPrivate) {
		t.Fatalf("audit status=%d body=%s", auditResponse.Code, auditResponse.Body.String())
	}
	if !bytes.Contains(auditResponse.Body.Bytes(), []byte(`"action":"security.admission.update"`)) {
		t.Fatalf("audit entry missing: %s", auditResponse.Body.String())
	}
}

func adminJSONRequest(t *testing.T, handler http.Handler, method, url string, body any, cookie *http.Cookie, csrf, origin, idempotency string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	} else {
		raw = []byte{}
	}
	request := httptest.NewRequest(method, url, bytes.NewReader(raw))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if csrf != "" {
		request.Header.Set("X-Yuanshu-CSRF", csrf)
		if idempotency == "" {
			idempotency = "idem-test"
		}
		request.Header.Set("Idempotency-Key", idempotency)
		request.Header.Set("X-Correlation-ID", "cor_test")
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
