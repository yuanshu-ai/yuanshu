package node

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/enrollment"
	"github.com/yuanshu-ai/yuanshu/internal/node/identity"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	platformfake "github.com/yuanshu-ai/yuanshu/internal/platform/fake"
	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

func TestPairingManagerCreatesApprovesRevokesAndRotates(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	local, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "node.db"), store.Options{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	fakePlatform, _ := platformfake.New(platform.FamilyWindows)
	secrets := fakePlatform.SecureStore()
	identityRef := platform.SecretRef("identity-test")
	credentialRef := platform.SecretRef("credential-test")
	credential := []byte("synthetic-node-credential-32-byte-value")
	if err := secrets.Put(context.Background(), credentialRef, credential); err != nil {
		t.Fatal(err)
	}
	identityManager, err := identity.NewManager(local, secrets, identityRef, identity.Options{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	nodeIdentity, err := identityManager.Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	nodeIdentity, err = identityManager.Bind(context.Background(), "own_test", "nod_test")
	if err != nil {
		t.Fatal(err)
	}
	clientPublic, _, _ := ed25519.GenerateKey(nil)
	expires := now.Add(5 * time.Minute).Format(time.RFC3339Nano)
	var mu sync.Mutex
	seen := map[string]int{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Yuanshu-Node-ID") != "nod_test" || r.Header.Get("Authorization") != "Bearer "+string(credential) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		seen[r.Method+" "+r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/control-client-pairings":
			json.NewEncoder(w).Encode(map[string]string{"pairingId": "pair_test", "expiresAt": expires})
		case "GET /v1/control-client-pairings":
			json.NewEncoder(w).Encode(map[string]any{"pairings": []map[string]string{{"pairingId": "pair_test", "clientId": "cli_test", "keyId": "key_test", "name": "Tablet", "publicKey": base64.RawURLEncoding.EncodeToString(clientPublic), "fingerprint": enrollment.Fingerprint(clientPublic), "expiresAt": expires}}})
		case "POST /v1/control-client-pairings/pair_test/decision":
			json.NewEncoder(w).Encode(map[string]string{"status": "approved"})
		case "DELETE /v1/control-clients/cli_test":
			w.WriteHeader(http.StatusNoContent)
		case "POST /v1/nodes/nod_test/credential/rotate":
			var body struct {
				NewCredentialHash string `json:"newCredentialHash"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			decoded, _ := base64.RawURLEncoding.DecodeString(body.NewCredentialHash)
			if len(decoded) != sha256.Size {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	relayURL := strings.Replace(server.URL, "https://", "wss://", 1) + "/node/connect"
	manager, err := newPairingManager(pairingManagerOptions{RelayURL: relayURL, HTTPClient: server.Client(), Identity: nodeIdentity, Signer: identityManager, Local: local, Secrets: secrets, CredentialRef: credentialRef, Credential: credential, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	link, err := manager.Create(context.Background())
	if err != nil || !strings.HasPrefix(link, server.URL+"/pair#pair_test.") {
		t.Fatalf("Create()=%q err=%v", link, err)
	}
	if err := manager.Decide(context.Background(), "pair_test", "accept"); err != nil {
		t.Fatal(err)
	}
	key, err := local.LookupControlKey(context.Background(), protocolv1.KeyRef{OwnerID: "own_test", NodeID: "nod_test", ClientID: "cli_test", KeyID: "key_test"})
	if err != nil || key.Status != protocolv1.TrustStatusActive {
		t.Fatalf("trusted key=%+v err=%v", key, err)
	}
	clients, err := manager.Clients(context.Background())
	if err != nil || len(clients) != 1 || clients[0].Fingerprint != enrollment.Fingerprint(clientPublic) {
		t.Fatalf("clients=%+v err=%v", clients, err)
	}
	if err := manager.Revoke(context.Background(), "cli_test", "key_test"); err != nil {
		t.Fatal(err)
	}
	key, _ = local.LookupControlKey(context.Background(), protocolv1.KeyRef{OwnerID: "own_test", NodeID: "nod_test", ClientID: "cli_test", KeyID: "key_test"})
	if key.Status != protocolv1.TrustStatusRevoked {
		t.Fatal("client was not revoked locally")
	}
	if err := manager.RotateCredential(context.Background()); err != nil {
		t.Fatal(err)
	}
	rotated, err := secrets.Get(context.Background(), credentialRef)
	if err != nil || string(rotated) == string(credential) {
		t.Fatal("connection credential did not rotate")
	}
	clear(rotated)
	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{"POST /v1/control-client-pairings", "POST /v1/control-client-pairings/pair_test/decision", "DELETE /v1/control-clients/cli_test", "POST /v1/nodes/nod_test/credential/rotate"} {
		if seen[path] != 1 {
			t.Fatalf("%s count=%d", path, seen[path])
		}
	}
}
