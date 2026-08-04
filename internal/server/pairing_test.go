package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/enrollment"
	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
	"github.com/yuanshu-ai/yuanshu/internal/transport"
)

func TestControlClientPairingApprovalRevocationAndNodeSessionRotation(t *testing.T) {
	service, local, bootstrapSecret := openServerService(t)
	nodePublic, nodePrivate, _ := ed25519.GenerateKey(nil)
	credential := "synthetic-current-node-credential"
	credentialHash := sha256.Sum256([]byte(credential))
	claim := validClaimRequest()
	claim.PublicKey = base64.RawURLEncoding.EncodeToString(nodePublic)
	claim.CredentialHash = base64.RawURLEncoding.EncodeToString(credentialHash[:])
	bound, _, err := service.Claim(context.Background(), bootstrapSecret, claim)
	if err != nil {
		t.Fatal(err)
	}
	origin := "https://mobile.example.test"
	hub, err := NewHub(local, HubOptions{AllowedControlOrigins: []string{origin}})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	handler, err := NewHandler(service, local, hub)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()

	node := dialPairedTestNode(t, server, hub, bound.NodeID, credential, nodePrivate)
	defer node.Close()

	pairingSecret := []byte("0123456789abcdef0123456789abcdef")
	codeHash := sha256.Sum256(pairingSecret)
	created := doPairingJSON(t, server.Client(), http.MethodPost, server.URL+"/v1/control-client-pairings", map[string]string{
		"codeHash": base64.RawURLEncoding.EncodeToString(codeHash[:]), "challenge": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
	}, nodeHeaders(bound.NodeID, credential))
	var createdValue struct{ PairingID, ExpiresAt string }
	if json.Unmarshal(created, &createdValue) != nil || createdValue.PairingID == "" {
		t.Fatal("pairing was not created")
	}

	clientPublic, clientPrivate, _ := ed25519.GenerateKey(nil)
	clientID, keyID := "cli_mobile", "key_mobile"
	claimHeaders := make(http.Header)
	claimHeaders.Set("Authorization", "Bearer "+string(pairingSecret))
	claimed := doPairingJSON(t, server.Client(), http.MethodPost, server.URL+"/v1/control-client-pairings/"+createdValue.PairingID+"/claim", map[string]string{
		"clientId": clientID, "keyId": keyID, "name": "Mobile browser", "publicKey": base64.RawURLEncoding.EncodeToString(clientPublic),
	}, claimHeaders)
	if !bytes.Contains(claimed, []byte(`"status":"claimed"`)) {
		t.Fatalf("claim=%s", claimed)
	}

	pending := doPairingJSON(t, server.Client(), http.MethodGet, server.URL+"/v1/control-client-pairings", nil, nodeHeaders(bound.NodeID, credential))
	var pendingValue struct {
		Pairings []PairingCandidateWire `json:"pairings"`
	}
	if json.Unmarshal(pending, &pendingValue) != nil || len(pendingValue.Pairings) != 1 {
		t.Fatalf("pending=%s", pending)
	}
	binding := enrollment.PairingDecision{Version: "1", PairingID: createdValue.PairingID, OwnerID: bound.OwnerID, NodeID: bound.NodeID, ClientID: clientID, KeyID: keyID, PublicKey: base64.RawURLEncoding.EncodeToString(clientPublic), Name: "Mobile browser", Decision: "accept", ExpiresAt: createdValue.ExpiresAt}
	input, err := enrollment.PairingDecisionSigningInput(binding)
	if err != nil {
		t.Fatal(err)
	}
	doPairingJSON(t, server.Client(), http.MethodPost, server.URL+"/v1/control-client-pairings/"+createdValue.PairingID+"/decision", map[string]string{"decision": "accept", "signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(nodePrivate, input))}, nodeHeaders(bound.NodeID, credential))

	status := doPairingJSON(t, server.Client(), http.MethodGet, server.URL+"/v1/control-client-pairings/"+createdValue.PairingID+"/status", nil, claimHeaders)
	if !bytes.Contains(status, []byte(`"status":"approved"`)) || !bytes.Contains(status, []byte(`"proof"`)) {
		t.Fatalf("status=%s", status)
	}
	record, err := local.ControlClientSession(context.Background(), clientID)
	if err != nil || record.Status != "active" || record.KeyID != keyID {
		t.Fatalf("client=%+v err=%v", record, err)
	}

	controlHeader := make(http.Header)
	controlHeader.Set("X-Yuanshu-Client-ID", clientID)
	controlHeader.Set("Origin", origin)
	control, _, err := transport.DialRelay(context.Background(), wssURL(server.URL)+"/web/connect", transport.RelayDialOptions{HTTPClient: server.Client(), Header: controlHeader, Role: transport.SessionRoleControl, SubjectID: clientID, Sign: func(_ context.Context, input []byte) ([]byte, error) { return ed25519.Sign(clientPrivate, input), nil }, Relay: transport.RelayOptions{MaxSendBytes: 256 << 10, MaxReceiveBytes: 1 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	waitHubSnapshot(t, hub, 1, 1)
	issued := fixedServerNow.Format(time.RFC3339Nano)
	revoke := enrollment.ClientRevocation{Version: "1", OwnerID: bound.OwnerID, NodeID: bound.NodeID, ClientID: clientID, KeyID: keyID, IssuedAt: issued}
	revokeInput, _ := enrollment.ClientRevocationSigningInput(revoke)
	doPairingJSON(t, server.Client(), http.MethodDelete, server.URL+"/v1/control-clients/"+clientID, map[string]string{"nodeId": bound.NodeID, "keyId": keyID, "issuedAt": issued, "signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(nodePrivate, revokeInput))}, nodeHeaders(bound.NodeID, credential))
	if _, err := control.Receive(context.Background()); err == nil {
		t.Fatal("revoked control client remained connected")
	}

	oldHeaders := nodeHeaders(bound.NodeID, credential)
	rotated := doPairingJSON(t, server.Client(), http.MethodPost, server.URL+"/v1/node-sessions/refresh", map[string]any{}, oldHeaders)
	var session struct{ SessionToken, SessionExpiresAt string }
	if json.Unmarshal(rotated, &session) != nil || session.SessionToken == "" || session.SessionToken == oldHeaders.Get("Authorization") {
		t.Fatalf("session refresh=%s", rotated)
	}
	pairedTestNodeSessions.Store(credential, session.SessionToken)
	oldRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/nodes", nil)
	oldRequest.Header = oldHeaders
	oldResponse, err := server.Client().Do(oldRequest)
	if err != nil {
		t.Fatal(err)
	}
	oldResponse.Body.Close()
	if oldResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old session status=%d", oldResponse.StatusCode)
	}
	_ = doPairingJSON(t, server.Client(), http.MethodGet, server.URL+"/v1/nodes", nil, nodeHeaders(bound.NodeID, credential))
}

func TestNodeInvitationLimiterSeparatesIPAndInvitation(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	limiter := newKeyedAttemptLimiter(func() time.Time { return now })
	for range 5 {
		if !limiter.allowed("ip:192.0.2.10", "invite:a") {
			t.Fatal("limiter blocked before the failure threshold")
		}
		limiter.failure("ip:192.0.2.10", "invite:a")
	}
	if limiter.allowed("ip:192.0.2.10", "invite:b") {
		t.Fatal("IP limit did not protect a second invitation")
	}
	if limiter.allowed("ip:192.0.2.11", "invite:a") {
		t.Fatal("invitation limit did not protect a second IP")
	}
	if !limiter.allowed("ip:192.0.2.11", "invite:b") {
		t.Fatal("unrelated IP and invitation were blocked")
	}
}

func TestNodeInvitationClaimActivatesFirstNodeOnce(t *testing.T) {
	_, local, _ := openServerService(t)
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	secret := "single-use-node-invitation"
	shortCode := "0123456789ABCDEF"
	secretHash, codeHash := sha256.Sum256([]byte(secret)), sha256.Sum256([]byte(shortCode))
	if err := local.CreateNodeInvitation(context.Background(), serverstore.CreateNodeInvitation{NodeInvitation: serverstore.NodeInvitation{ID: "inv_first", DisplayName: "First Node", Status: "pending", CreatedBy: "server_setup", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}, SecretHash: secretHash[:], CodeHash: codeHash[:]}); err != nil {
		t.Fatal(err)
	}
	pairing, err := NewPairingService(local, nil, PairingOptions{Clock: func() time.Time { return now.Add(time.Minute) }})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]string{"invitationId": "inv_first", "secret": secret, "name": "Office Mac", "os": "darwin", "arch": "arm64", "version": "dev", "publicKey": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, ed25519.PublicKeySize))}
	raw, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPost, "/v1/node-invitations/claim", bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "192.0.2.10:50123"
	response := httptest.NewRecorder()
	pairing.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !bytes.Contains(response.Body.Bytes(), []byte(`"status":"active"`)) {
		t.Fatalf("claim status=%d body=%s", response.Code, response.Body.String())
	}
	replay := httptest.NewRequest(http.MethodPost, "/v1/node-invitations/claim", bytes.NewReader(raw))
	replay.Header.Set("Content-Type", "application/json")
	replay.RemoteAddr = "192.0.2.10:50124"
	replayResponse := httptest.NewRecorder()
	pairing.Handler().ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusUnauthorized && replayResponse.Code != http.StatusConflict {
		t.Fatalf("replayed claim status=%d body=%s", replayResponse.Code, replayResponse.Body.String())
	}
}

func TestPairingPageUsesStrictBrowserBoundary(t *testing.T) {
	handler := PairingPageHandler()
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/pair", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "连接这个浏览器") || !strings.Contains(page.Header().Get("Content-Security-Policy"), "default-src 'none'") || page.Header().Get("Referrer-Policy") != "no-referrer" || page.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("page status=%d headers=%v", page.Code, page.Header())
	}
	script := httptest.NewRecorder()
	handler.ServeHTTP(script, httptest.NewRequest(http.MethodGet, "/pair/app.js", nil))
	for _, required := range []string{"crypto.subtle.generateKey", "indexedDB", "textContent", "deleteKey", "client.publicKey", "/pair/storage.js"} {
		if !strings.Contains(script.Body.String(), required) {
			t.Fatalf("pairing script missing %q", required)
		}
	}
	if strings.Contains(script.Body.String(), "innerHTML") {
		t.Fatal("pairing script uses unsafe HTML rendering")
	}
	logo := httptest.NewRecorder()
	handler.ServeHTTP(logo, httptest.NewRequest(http.MethodGet, "/pair/logo.svg", nil))
	if logo.Code != http.StatusOK || logo.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(logo.Body.String(), "Yuanshu remote hub mark") {
		t.Fatalf("pairing logo status=%d content-type=%q", logo.Code, logo.Header().Get("Content-Type"))
	}
	storage := httptest.NewRecorder()
	handler.ServeHTTP(storage, httptest.NewRequest(http.MethodGet, "/pair/storage.js", nil))
	for _, required := range []string{"CONTROL_DATABASE_VERSION = 5", "runtime-settings", "node-bindings", "preferences"} {
		if storage.Code != http.StatusOK || !strings.Contains(storage.Body.String(), required) {
			t.Fatalf("pairing storage module missing %q", required)
		}
	}
}

func TestPersonalNodeEnrollmentRoutesOneControlToTwoIsolatedNodes(t *testing.T) {
	service, local, bootstrapSecret := openServerService(t)
	issuerPublic, issuerPrivate, _ := ed25519.GenerateKey(nil)
	issuerCredential := "issuer-node-credential-material"
	issuerHash := sha256.Sum256([]byte(issuerCredential))
	claim := validClaimRequest()
	claim.PublicKey = base64.RawURLEncoding.EncodeToString(issuerPublic)
	claim.CredentialHash = base64.RawURLEncoding.EncodeToString(issuerHash[:])
	bound, _, err := service.Claim(context.Background(), bootstrapSecret, claim)
	if err != nil {
		t.Fatal(err)
	}
	origin := "https://mobile.example.test"
	hub, _ := NewHub(local, HubOptions{AllowedControlOrigins: []string{origin}})
	defer hub.Close()
	handler, _ := NewHandler(service, local, hub)
	tlsServer := httptest.NewTLSServer(handler)
	defer tlsServer.Close()
	issuer := dialPairedTestNodeCount(t, tlsServer, hub, bound.NodeID, issuerCredential, issuerPrivate, 1)
	defer issuer.Close()

	clientPublic, clientPrivate, _ := ed25519.GenerateKey(nil)
	clientID, keyID := "cli_multi", "key_multi"
	pairSecret := bytes.Repeat([]byte{0x31}, 32)
	pairHash := sha256.Sum256(pairSecret)
	createdPair := doPairingJSON(t, tlsServer.Client(), http.MethodPost, tlsServer.URL+"/v1/control-client-pairings", map[string]string{"codeHash": base64.RawURLEncoding.EncodeToString(pairHash[:]), "challenge": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x32}, 32))}, nodeHeaders(bound.NodeID, issuerCredential))
	var pair struct{ PairingID, ExpiresAt string }
	_ = json.Unmarshal(createdPair, &pair)
	pairHeaders := make(http.Header)
	pairHeaders.Set("Authorization", "Bearer "+string(pairSecret))
	doPairingJSON(t, tlsServer.Client(), http.MethodPost, tlsServer.URL+"/v1/control-client-pairings/"+pair.PairingID+"/claim", map[string]string{"clientId": clientID, "keyId": keyID, "name": "One controller", "publicKey": base64.RawURLEncoding.EncodeToString(clientPublic)}, pairHeaders)
	pairBinding := enrollment.PairingDecision{Version: "1", PairingID: pair.PairingID, OwnerID: bound.OwnerID, NodeID: bound.NodeID, ClientID: clientID, KeyID: keyID, PublicKey: base64.RawURLEncoding.EncodeToString(clientPublic), Name: "One controller", Decision: "accept", ExpiresAt: pair.ExpiresAt}
	pairInput, _ := enrollment.PairingDecisionSigningInput(pairBinding)
	doPairingJSON(t, tlsServer.Client(), http.MethodPost, tlsServer.URL+"/v1/control-client-pairings/"+pair.PairingID+"/decision", map[string]string{"decision": "accept", "signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(issuerPrivate, pairInput))}, nodeHeaders(bound.NodeID, issuerCredential))

	enrollSecret := bytes.Repeat([]byte{0x41}, 32)
	enrollHash := sha256.Sum256(enrollSecret)
	created := doPairingJSON(t, tlsServer.Client(), http.MethodPost, tlsServer.URL+"/v1/node-enrollments", map[string]string{"codeHash": base64.RawURLEncoding.EncodeToString(enrollHash[:])}, nodeHeaders(bound.NodeID, issuerCredential))
	var enrollmentCreated struct{ EnrollmentID, ExpiresAt string }
	if json.Unmarshal(created, &enrollmentCreated) != nil || enrollmentCreated.EnrollmentID == "" {
		t.Fatalf("create=%s", created)
	}
	candidatePublic, candidatePrivate, _ := ed25519.GenerateKey(nil)
	candidateCredential := "candidate-node-credential-material"
	candidateHash := sha256.Sum256([]byte(candidateCredential))
	enrollHeaders := make(http.Header)
	enrollHeaders.Set("Authorization", "Bearer "+string(enrollSecret))
	claimed := doPairingJSON(t, tlsServer.Client(), http.MethodPost, tlsServer.URL+"/v1/node-enrollments/"+enrollmentCreated.EnrollmentID+"/claim", map[string]string{"name": "Home PC", "os": "windows", "version": "dev", "publicKey": base64.RawURLEncoding.EncodeToString(candidatePublic), "credentialHash": base64.RawURLEncoding.EncodeToString(candidateHash[:])}, enrollHeaders)
	var claimValue struct {
		CandidateNodeID string `json:"candidateNodeId"`
	}
	if json.Unmarshal(claimed, &claimValue) != nil || claimValue.CandidateNodeID == "" {
		t.Fatalf("claim=%s", claimed)
	}
	pending := doPairingJSON(t, tlsServer.Client(), http.MethodGet, tlsServer.URL+"/v1/node-enrollments", nil, nodeHeaders(bound.NodeID, issuerCredential))
	var pendingValue struct {
		Enrollments []struct{ EnrollmentID, CandidateNodeID, Name, OS, Version, PublicKey, CredentialHash, ExpiresAt string } `json:"enrollments"`
	}
	if json.Unmarshal(pending, &pendingValue) != nil || len(pendingValue.Enrollments) != 1 {
		t.Fatalf("pending=%s", pending)
	}
	candidate := pendingValue.Enrollments[0]
	decision := enrollment.NodeEnrollmentDecision{Version: "1", EnrollmentID: candidate.EnrollmentID, OwnerID: bound.OwnerID, IssuerNodeID: bound.NodeID, CandidateNodeID: candidate.CandidateNodeID, CandidatePublicKey: candidate.PublicKey, CredentialHash: candidate.CredentialHash, Name: candidate.Name, OS: candidate.OS, NodeVersion: candidate.Version, Decision: "accept", ExpiresAt: candidate.ExpiresAt}
	decisionInput, _ := enrollment.NodeEnrollmentDecisionSigningInput(decision)
	doPairingJSON(t, tlsServer.Client(), http.MethodPost, tlsServer.URL+"/v1/node-enrollments/"+candidate.EnrollmentID+"/decision", map[string]string{"decision": "accept", "signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(issuerPrivate, decisionInput))}, nodeHeaders(bound.NodeID, issuerCredential))
	status := doPairingJSON(t, tlsServer.Client(), http.MethodGet, tlsServer.URL+"/v1/node-enrollments/"+candidate.EnrollmentID+"/status", nil, enrollHeaders)
	if !bytes.Contains(status, []byte(`"status":"approved"`)) || !bytes.Contains(status, []byte(`"proof"`)) {
		t.Fatalf("status=%s", status)
	}
	candidateNode := dialPairedTestNodeCount(t, tlsServer, hub, claimValue.CandidateNodeID, candidateCredential, candidatePrivate, 2)
	defer candidateNode.Close()

	controlHeader := make(http.Header)
	controlHeader.Set("X-Yuanshu-Client-ID", clientID)
	controlHeader.Set("Origin", origin)
	control, _, err := transport.DialRelay(context.Background(), wssURL(tlsServer.URL)+"/web/connect", transport.RelayDialOptions{HTTPClient: tlsServer.Client(), Header: controlHeader, Role: transport.SessionRoleControl, SubjectID: clientID, Sign: func(_ context.Context, input []byte) ([]byte, error) { return ed25519.Sign(clientPrivate, input), nil }, Relay: transport.RelayOptions{MaxSendBytes: 256 << 10, MaxReceiveBytes: 1 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	waitHubSnapshot(t, hub, 2, 1)
	first := signedRoutedControl(t, bound.OwnerID, bound.NodeID, clientID, keyID, clientPrivate, protocolv1.ControlDeviceSync, 1, map[string]any{}, "", "", "", "")
	second := signedRoutedControl(t, bound.OwnerID, claimValue.CandidateNodeID, clientID, keyID, clientPrivate, protocolv1.ControlDeviceSync, 1, map[string]any{}, "", "", "", "")
	if err := control.Send(context.Background(), transport.NewFrame(first)); err != nil {
		t.Fatal(err)
	}
	if got, err := issuer.Receive(context.Background()); err != nil || !bytes.Equal(got.Bytes(), first) {
		t.Fatalf("issuer route err=%v", err)
	}
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := candidateNode.Receive(short); err != context.DeadlineExceeded {
		t.Fatalf("cross-stream err=%v", err)
	}
	if err := control.Send(context.Background(), transport.NewFrame(second)); err != nil {
		t.Fatal(err)
	}
	if got, err := candidateNode.Receive(context.Background()); err != nil || !bytes.Equal(got.Bytes(), second) {
		t.Fatalf("candidate route err=%v", err)
	}
	manifest := doPairingJSON(t, tlsServer.Client(), http.MethodGet, tlsServer.URL+"/v1/control-clients", nil, nodeHeaders(claimValue.CandidateNodeID, candidateCredential))
	if !bytes.Contains(manifest, []byte(clientID)) || !bytes.Contains(manifest, []byte(`"revision":1`)) {
		t.Fatalf("manifest=%s", manifest)
	}

	issued := fixedServerNow.Format(time.RFC3339Nano)
	revoke := enrollment.NodeRevocation{Version: "1", OwnerID: bound.OwnerID, IssuerNodeID: bound.NodeID, TargetNodeID: claimValue.CandidateNodeID, IssuedAt: issued}
	revokeInput, _ := enrollment.NodeRevocationSigningInput(revoke)
	doPairingJSON(t, tlsServer.Client(), http.MethodDelete, tlsServer.URL+"/v1/nodes/"+claimValue.CandidateNodeID, map[string]string{"issuedAt": issued, "signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(issuerPrivate, revokeInput))}, nodeHeaders(bound.NodeID, issuerCredential))
	ctx, cancelRevoke := context.WithTimeout(context.Background(), time.Second)
	defer cancelRevoke()
	if _, err := candidateNode.Receive(ctx); err == nil {
		t.Fatal("revoked node remained connected")
	}
	if !hub.NodeConnected(bound.OwnerID, bound.NodeID) {
		t.Fatal("issuer node was disconnected by target revocation")
	}
}

type PairingCandidateWire struct {
	PairingID string `json:"pairingId"`
}

func nodeHeaders(nodeID, credential string) http.Header {
	header := make(http.Header)
	header.Set("X-Yuanshu-Node-ID", nodeID)
	if value, ok := pairedTestNodeSessions.Load(credential); ok {
		header.Set("Authorization", "YuanshuNodeSession "+value.(string))
	}
	return header
}

var pairedTestNodeSessions sync.Map

func dialPairedTestNode(t *testing.T, server *httptest.Server, hub *Hub, nodeID, credential string, private ed25519.PrivateKey) transport.Transport {
	return dialPairedTestNodeCount(t, server, hub, nodeID, credential, private, 1)
}
func dialPairedTestNodeCount(t *testing.T, server *httptest.Server, hub *Hub, nodeID, credential string, private ed25519.PrivateKey, expected int) transport.Transport {
	t.Helper()
	header := make(http.Header)
	header.Set("X-Yuanshu-Node-ID", nodeID)
	result, _, err := transport.DialRelay(context.Background(), wssURL(server.URL)+"/node/connect", transport.RelayDialOptions{HTTPClient: server.Client(), Header: header, Role: transport.SessionRoleNode, SubjectID: nodeID, Sign: func(_ context.Context, input []byte) ([]byte, error) { return ed25519.Sign(private, input), nil }, OnAuthenticated: func(ready transport.SessionReady) error {
		pairedTestNodeSessions.Store(credential, ready.SessionToken)
		return nil
	}, Relay: transport.RelayOptions{MaxSendBytes: 1 << 20, MaxReceiveBytes: 256 << 10}})
	if err != nil {
		t.Fatal(err)
	}
	waitHubSnapshot(t, hub, expected, hub.Snapshot().ControlConnections)
	return result
}
func doPairingJSON(t *testing.T, client *http.Client, method, url string, body any, headers http.Header) []byte {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	request, _ := http.NewRequest(method, url, reader)
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		t.Fatalf("%s %s status=%d body=%s", method, url, response.StatusCode, raw)
	}
	return raw
}
