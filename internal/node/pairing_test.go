package node

import (
	"context"
	"crypto/ed25519"
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
	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

func TestPairingManagerCreatesApprovesRevokesAndRotates(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	local, err := store.Open(context.Background(), filepath.Join(root, "node.db"), store.Options{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	sessionToken := []byte("01234567890123456789012345678901")
	identityStore, err := identity.NewFileKeyStore(filepath.Join(root, "identity.key"))
	if err != nil {
		t.Fatal(err)
	}
	identityManager, err := identity.NewManager(local, identityStore, identity.Options{Clock: func() time.Time { return now }})
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
		if r.Header.Get("X-Yuanshu-Node-ID") != "nod_test" || r.Header.Get("Authorization") != "YuanshuNodeSession "+base64.RawURLEncoding.EncodeToString(sessionToken) {
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
		case "POST /v1/node-sessions/refresh":
			sessionToken = []byte("abcdefghijklmnopqrstuvwxyzABCDEF")
			json.NewEncoder(w).Encode(map[string]string{"sessionToken": base64.RawURLEncoding.EncodeToString(sessionToken), "sessionExpiresAt": now.Add(15 * time.Minute).Format(time.RFC3339Nano)})
		default:
			http.NotFound(w, r)
		}
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	relayURL := strings.Replace(server.URL, "https://", "wss://", 1) + "/node/connect"
	manager, err := newPairingManager(pairingManagerOptions{RelayURL: relayURL, HTTPClient: server.Client(), Identity: nodeIdentity, Signer: identityManager, Local: local, SessionToken: sessionToken, SessionExpiresAt: now.Add(15 * time.Minute), Clock: func() time.Time { return now }})
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
	rotated, _ := manager.sessionCopy()
	if string(rotated) != "abcdefghijklmnopqrstuvwxyzABCDEF" {
		t.Fatal("node session did not rotate")
	}
	clear(rotated)
	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{"POST /v1/control-client-pairings", "POST /v1/control-client-pairings/pair_test/decision", "DELETE /v1/control-clients/cli_test", "POST /v1/node-sessions/refresh"} {
		if seen[path] != 1 {
			t.Fatalf("%s count=%d", path, seen[path])
		}
	}
}
