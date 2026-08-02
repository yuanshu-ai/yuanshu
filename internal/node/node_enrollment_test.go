package node

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/enrollment"
	"github.com/yuanshu-ai/yuanshu/internal/node/identity"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	platformfake "github.com/yuanshu-ai/yuanshu/internal/platform/fake"
	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

func TestUnboundNodeJoinsAndImportsOwnerTrust(t *testing.T) {
	local, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "node.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	secrets := platformfake.NewSecureStore()
	manager, err := identity.NewManager(local, secrets, platform.SecretRef("identity/test"), identity.Options{})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := manager.Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	issuerPublic, issuerPrivate, _ := ed25519.GenerateKey(nil)
	clientPublic, _, _ := ed25519.GenerateKey(nil)
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = 0x51
	}
	expires := time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339Nano)
	var claimBody map[string]string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/node-enrollments/enrollment/claim", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+base64.RawURLEncoding.EncodeToString(secret) {
			t.Error("wrong enrollment bearer")
		}
		_ = json.NewDecoder(r.Body).Decode(&claimBody)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "claimed", "candidateNodeId": "node-new", "fingerprint": "synthetic", "expiresAt": expires})
	})
	mux.HandleFunc("GET /v1/node-enrollments/enrollment/status", func(w http.ResponseWriter, r *http.Request) {
		binding := enrollment.NodeEnrollmentDecision{Version: "1", EnrollmentID: "enrollment", OwnerID: "owner", IssuerNodeID: "node-issuer", CandidateNodeID: "node-new", CandidatePublicKey: claimBody["publicKey"], CredentialHash: claimBody["credentialHash"], Name: "New PC", OS: runtime.GOOS, NodeVersion: "dev", Decision: "accept", ExpiresAt: expires}
		input, _ := enrollment.NodeEnrollmentDecisionSigningInput(binding)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "approved", "ownerId": "owner", "nodeId": "node-new", "issuerNodeId": "node-issuer", "issuerPublicKey": base64.RawURLEncoding.EncodeToString(issuerPublic), "proof": base64.RawURLEncoding.EncodeToString(ed25519.Sign(issuerPrivate, input)), "expiresAt": expires})
	})
	mux.HandleFunc("GET /v1/control-clients", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Yuanshu-Node-ID") != "node-new" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("trust request was not candidate authenticated")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"revision": 1, "clients": []map[string]string{{"clientId": "client", "keyId": "key", "publicKey": base64.RawURLEncoding.EncodeToString(clientPublic), "status": "active"}}})
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	completed := make(chan struct{}, 1)
	failures := make(chan error, 1)
	joiner, err := newNodeEnrollmentJoiner(enrollmentJoinerOptions{RelayURL: "wss" + strings.TrimPrefix(server.URL, "https"), HTTPClient: server.Client(), Identity: candidate, Signer: manager, Local: local, Secrets: secrets, CredentialRef: platform.SecretRef("relay/test"), Name: "New PC", Version: "dev", OnComplete: func() { completed <- struct{}{} }, OnError: func(err error) { failures <- err }})
	if err != nil {
		t.Fatal(err)
	}
	defer joiner.Close()
	joinURL := server.URL + "/join#enrollment." + base64.RawURLEncoding.EncodeToString(secret) + "." + base64.RawURLEncoding.EncodeToString(issuerPublic)
	if err := joiner.Join(context.Background(), joinURL); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(4 * time.Second):
		t.Fatal("join did not complete")
	}
	bound, err := manager.Ensure(context.Background())
	if err != nil || bound.OwnerID != "owner" || bound.NodeID != "node-new" {
		t.Fatalf("bound=%+v err=%v", bound, err)
	}
	trusted, err := local.LookupControlKey(context.Background(), protocolv1.KeyRef{OwnerID: "owner", NodeID: "node-new", ClientID: "client", KeyID: "key"})
	if err != nil || trusted.Status != protocolv1.TrustStatusActive {
		t.Fatalf("trust=%+v err=%v", trusted, err)
	}
}
