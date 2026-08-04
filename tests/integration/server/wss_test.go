package server_integration_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
	"github.com/yuanshu-ai/yuanshu/internal/server"
	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
	"github.com/yuanshu-ai/yuanshu/internal/transport"
)

func TestTLSHubAuthenticatesPersistedIdentitiesAndRoutes(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "server.db")
	local, err := serverstore.Open(context.Background(), databasePath, serverstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	nodePublic, nodePrivate, _ := ed25519.GenerateKey(nil)
	controlPublic, controlPrivate, _ := ed25519.GenerateKey(nil)
	bootstrap, err := server.NewBootstrapService(local, server.BootstrapOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret, issued, err := bootstrap.Rotate(context.Background())
	if err != nil || !issued {
		t.Fatalf("rotate issued=%v err=%v", issued, err)
	}
	claim, _, err := bootstrap.Claim(context.Background(), secret, server.ClaimRequest{
		RequestID: "wss-integration", Name: "Integration Node", OS: "windows", Version: "dev",
		PublicKey: base64.RawURLEncoding.EncodeToString(nodePublic),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.ToSlash(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO control_clients(id, owner_id, public_key, name, status, created_at)
		VALUES ('cli_integration', ?, ?, 'Integration Client', 'active', ?)`, claim.OwnerID, controlPublic, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	local, err = serverstore.Open(context.Background(), databasePath, serverstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	bootstrap, _ = server.NewBootstrapService(local, server.BootstrapOptions{})
	origin := "https://mobile.example.test"
	hub, err := server.NewHub(local, server.HubOptions{AllowedControlOrigins: []string{origin}})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	handler, err := server.NewHandler(bootstrap, local, hub)
	if err != nil {
		t.Fatal(err)
	}
	tlsServer := httptest.NewTLSServer(handler)
	defer tlsServer.Close()

	nodeHeader := make(http.Header)
	nodeHeader.Set("X-Yuanshu-Node-ID", claim.NodeID)
	node, _, err := transport.DialRelay(context.Background(), toWSS(tlsServer.URL)+"/node/connect", transport.RelayDialOptions{
		HTTPClient: tlsServer.Client(), Header: nodeHeader, Role: transport.SessionRoleNode, SubjectID: claim.NodeID,
		Sign:  func(_ context.Context, input []byte) ([]byte, error) { return ed25519.Sign(nodePrivate, input), nil },
		Relay: transport.RelayOptions{MaxSendBytes: 1 << 20, MaxReceiveBytes: 256 << 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	controlHeader := make(http.Header)
	controlHeader.Set("X-Yuanshu-Client-ID", "cli_integration")
	controlHeader.Set("Origin", origin)
	control, _, err := transport.DialRelay(context.Background(), toWSS(tlsServer.URL)+"/web/connect", transport.RelayDialOptions{
		HTTPClient: tlsServer.Client(), Header: controlHeader, Role: transport.SessionRoleControl, SubjectID: "cli_integration",
		Sign:  func(_ context.Context, input []byte) ([]byte, error) { return ed25519.Sign(controlPrivate, input), nil },
		Relay: transport.RelayOptions{MaxSendBytes: 256 << 10, MaxReceiveBytes: 1 << 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	controlRaw := signedIntegrationControl(t, claim.OwnerID, claim.NodeID, "cli_integration", "primary", controlPrivate, 1)
	if err := control.Send(context.Background(), transport.NewFrame(controlRaw)); err != nil {
		t.Fatal(err)
	}
	received, err := node.Receive(context.Background())
	if err != nil || !bytes.Equal(received.Bytes(), controlRaw) {
		t.Fatalf("node received=%q err=%v", received.Bytes(), err)
	}
	eventRaw := []byte(`{"protocolVersion":"1.0","type":"device.status","ownerId":"` + claim.OwnerID + `","nodeId":"` + claim.NodeID + `"}`)
	if err := node.Send(context.Background(), transport.NewFrame(eventRaw)); err != nil {
		t.Fatal(err)
	}
	received, err = control.Receive(context.Background())
	if err != nil || !bytes.Equal(received.Bytes(), eventRaw) {
		t.Fatalf("control received=%q err=%v", received.Bytes(), err)
	}
}

func toWSS(value string) string { return "wss" + strings.TrimPrefix(value, "https") }

func signedIntegrationControl(t *testing.T, ownerID, nodeID, clientID, keyID string, private ed25519.PrivateKey, sequence int64) []byte {
	t.Helper()
	now := time.Now().UTC()
	expires := now.Add(time.Minute).Format(time.RFC3339Nano)
	nonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(sequence)}, 16))
	messageID := "control-" + fmt.Sprint(sequence)
	message := protocolv1.YuanshuMessage{
		ProtocolVersion: protocolv1.CurrentVersion, MessageID: messageID, Type: string(protocolv1.ControlDeviceSync),
		SentAt: now.Format(time.RFC3339Nano), ExpiresAt: &expires, OwnerID: ownerID, NodeID: nodeID,
		StreamID: "control-stream", Sequence: sequence, CorrelationID: messageID, Nonce: &nonce,
		Signer: &protocolv1.Signer{ClientID: clientID, KeyID: keyID}, Payload: map[string]any{},
	}
	input, err := protocolv1.ControlSigningInput(message)
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, input))
	message.Signature = &signature
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
