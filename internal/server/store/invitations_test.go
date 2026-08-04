package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestNodeInvitationCreatesFirstOwnerAndIsSingleUse(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	local, err := Open(context.Background(), filepath.Join(t.TempDir(), "server.db"), Options{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	secret, code := sha256.Sum256([]byte("single-use-secret")), sha256.Sum256([]byte("0123456789ABCDEF"))
	if err := local.CreateNodeInvitation(context.Background(), CreateNodeInvitation{NodeInvitation: NodeInvitation{ID: "inv_first", DisplayName: "First Mac", Status: "pending", CreatedBy: "server_setup", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}, SecretHash: secret[:], CodeHash: code[:]}); err != nil {
		t.Fatal(err)
	}
	claim := ClaimNodeInvitation{InvitationID: "inv_first", ProofHash: secret[:], NodeID: "nod_first", OwnerID: "own_first", PublicKey: bytes.Repeat([]byte{7}, 32), Name: "Office Mac", OS: "darwin", Arch: "arm64", Version: "dev", Now: now.Add(time.Minute)}
	item, err := local.ClaimNodeInvitation(context.Background(), claim)
	if err != nil || item.OwnerID != "own_first" || item.NodeID != "nod_first" || item.Status != "used" {
		t.Fatalf("claim=%+v err=%v", item, err)
	}
	session, err := local.NodeSession(context.Background(), "nod_first")
	if err != nil || session.OwnerID != "own_first" || len(session.CredentialHash) != 0 {
		t.Fatalf("session=%+v err=%v", session, err)
	}
	if _, err := local.ClaimNodeInvitation(context.Background(), claim); err == nil {
		t.Fatal("used invitation was accepted twice")
	}
}

func TestNodeInvitationShortCodeCancelAndExpiry(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	local, err := Open(context.Background(), filepath.Join(t.TempDir(), "server.db"), Options{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	secret, code := sha256.Sum256([]byte("secret")), sha256.Sum256([]byte("0123456789ABCDEF"))
	if err := local.CreateNodeInvitation(context.Background(), CreateNodeInvitation{NodeInvitation: NodeInvitation{ID: "inv_cancel", DisplayName: "Windows PC", Status: "pending", CreatedBy: "server_setup", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, SecretHash: secret[:], CodeHash: code[:]}); err != nil {
		t.Fatal(err)
	}
	if err := local.CancelNodeInvitation(context.Background(), "", "inv_cancel", now); err == nil {
		t.Fatal("ownerless invitation was remotely cancelled")
	}
	items, err := local.ListNodeInvitations(context.Background(), "own_missing", now.Add(2*time.Minute))
	if err != nil || len(items) != 0 {
		t.Fatalf("owner isolation items=%v err=%v", items, err)
	}
}

func TestNodeInvitationHonorsEnrollmentAdmission(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	local, err := Open(context.Background(), filepath.Join(t.TempDir(), "server.db"), Options{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	firstSecret, firstCode := sha256.Sum256([]byte("first-secret")), sha256.Sum256([]byte("FIRSTNODE0000001"))
	if err := local.CreateNodeInvitation(context.Background(), CreateNodeInvitation{NodeInvitation: NodeInvitation{ID: "inv_first", DisplayName: "First Node", Status: "pending", CreatedBy: "server_setup", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}, SecretHash: firstSecret[:], CodeHash: firstCode[:]}); err != nil {
		t.Fatal(err)
	}
	first := ClaimNodeInvitation{InvitationID: "inv_first", ProofHash: firstSecret[:], NodeID: "nod_first", OwnerID: "own_first", PublicKey: bytes.Repeat([]byte{1}, 32), Name: "First Node", OS: "darwin", Arch: "arm64", Version: "dev", Now: now.Add(time.Minute)}
	if _, err := local.ClaimNodeInvitation(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	settings, err := local.SecuritySettings(context.Background(), "own_first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.UpdateSecuritySettings(context.Background(), "own_first", "cli_admin", settings.ControlPairingEnabled, false, settings.Revision, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	secret, code := sha256.Sum256([]byte("blocked-secret")), sha256.Sum256([]byte("BLOCKEDNODE00001"))
	if err := local.CreateNodeInvitation(context.Background(), CreateNodeInvitation{NodeInvitation: NodeInvitation{ID: "inv_blocked", OwnerID: "own_first", DisplayName: "Blocked Node", Status: "pending", CreatedBy: "cli_admin", CreatedAt: now.Add(3 * time.Minute), ExpiresAt: now.Add(10 * time.Minute)}, SecretHash: secret[:], CodeHash: code[:]}); err != nil {
		t.Fatal(err)
	}
	_, err = local.ClaimNodeInvitation(context.Background(), ClaimNodeInvitation{InvitationID: "inv_blocked", ProofHash: secret[:], NodeID: "nod_blocked", PublicKey: bytes.Repeat([]byte{2}, 32), Name: "Blocked Node", OS: "windows", Arch: "amd64", Version: "dev", Now: now.Add(4 * time.Minute)})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("disabled admission claim error=%v", err)
	}
}
