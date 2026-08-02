package server_contract_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
)

func TestServerMetadataStoreContract(t *testing.T) {
	now := time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC)
	local, err := serverstore.Open(context.Background(), filepath.Join(t.TempDir(), "server.db"), serverstore.Options{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	secretHash := bytes.Repeat([]byte{1}, 32)
	if status, err := local.RotateBootstrap(context.Background(), secretHash, now); err != nil || status.State != serverstore.BootstrapPending {
		t.Fatalf("RotateBootstrap()=%+v err=%v", status, err)
	}
	claim := serverstore.BootstrapClaim{
		SecretHash: secretHash, ClaimDigest: bytes.Repeat([]byte{2}, 32), OwnerID: "own_contract", NodeID: "nod_contract",
		RequestID: "request", Name: "Contract Node", OS: "linux", Version: "dev",
		PublicKey: bytes.Repeat([]byte{3}, 32), CredentialHash: bytes.Repeat([]byte{4}, 32),
		Now: now, RetryUntil: now.Add(5 * time.Minute),
	}
	if result, err := local.ClaimBootstrap(context.Background(), claim); err != nil || result.OwnerID != claim.OwnerID || result.NodeID != claim.NodeID {
		t.Fatalf("ClaimBootstrap()=%+v err=%v", result, err)
	}
	owner, err := local.Owner(context.Background())
	if err != nil || owner.ID != claim.OwnerID || owner.Status != "active" {
		t.Fatalf("Owner()=%+v err=%v", owner, err)
	}
	nodes, err := local.Nodes(context.Background())
	if err != nil || len(nodes) != 1 || nodes[0].OwnerID != owner.ID || nodes[0].OS != "linux" {
		t.Fatalf("Nodes()=%+v err=%v", nodes, err)
	}
	clients, err := local.ControlClients(context.Background())
	if err != nil || len(clients) != 0 {
		t.Fatalf("ControlClients()=%+v err=%v", clients, err)
	}
}
