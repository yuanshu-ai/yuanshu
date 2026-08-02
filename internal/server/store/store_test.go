package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var testNow = time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "server.db")
	local, err := Open(context.Background(), path, Options{Clock: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	return local, path
}

func TestOpenCreatesServerSchemaAndReopens(t *testing.T) {
	local, path := openTestStore(t)
	for _, table := range []string{"bootstrap", "control_clients", "node_credentials", "nodes", "owners", "pairings", "schema_migrations"} {
		var count int
		if err := local.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("table %s count=%d err=%v", table, count, err)
		}
	}
	inspection, err := Inspect(context.Background(), path)
	if err != nil || inspection.SchemaVersion != CurrentSchemaVersion || inspection.QuickCheck != "ok" {
		t.Fatalf("inspection=%+v err=%v", inspection, err)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsInvalidFutureAndCorruptFiles(t *testing.T) {
	if _, err := Open(context.Background(), "relative.db", Options{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative error=%v", err)
	}
	directory := t.TempDir()
	if _, err := Open(context.Background(), directory, Options{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("directory error=%v", err)
	}
	futurePath := filepath.Join(t.TempDir(), "future.db")
	future, err := Open(context.Background(), futurePath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := future.db.Exec("INSERT INTO schema_migrations(version,name,applied_at) VALUES (3,'future',?)", timestamp(testNow)); err != nil {
		t.Fatal(err)
	}
	if _, err := future.db.Exec("PRAGMA user_version = 3"); err != nil {
		t.Fatal(err)
	}
	_ = future.Close()
	if _, err := Open(context.Background(), futurePath, Options{}); !errors.Is(err, ErrFutureSchema) {
		t.Fatalf("future error=%v", err)
	}
	corruptPath := filepath.Join(t.TempDir(), "corrupt.db")
	canary := "server-corruption-canary"
	if err := os.WriteFile(corruptPath, []byte(canary), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), corruptPath, Options{}); !errors.Is(err, ErrCorrupt) || strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), corruptPath) {
		t.Fatalf("unsafe corrupt error=%v", err)
	}
}

func TestOpenRejectsSymlinkAndHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "canceled.db")
	if _, err := Open(ctx, path, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled open error=%v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled open created a database: %v", err)
	}

	directory := t.TempDir()
	target := filepath.Join(directory, "target.db")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "server.db")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Open(context.Background(), link, Options{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink open error=%v", err)
	}
}

func TestMigrationFailureRollsBack(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.ToSlash(filepath.Join(t.TempDir(), "rollback.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	original := serverMigrations
	serverMigrations = []migration{{version: 1, name: "broken", statements: []string{"CREATE TABLE should_rollback(id INTEGER)", "NOT SQL"}}}
	t.Cleanup(func() { serverMigrations = original })
	if err := runMigrations(context.Background(), db, testNow); err == nil {
		t.Fatal("migration unexpectedly succeeded")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name IN ('should_rollback','schema_migrations')").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback count=%d err=%v", count, err)
	}
}

func TestBootstrapClaimPersistsMetadataAndReplaysExactly(t *testing.T) {
	local, _ := openTestStore(t)
	secret := bytes.Repeat([]byte{1}, 32)
	if status, err := local.RotateBootstrap(context.Background(), secret, testNow); err != nil || status.State != BootstrapPending {
		t.Fatalf("rotate=%+v err=%v", status, err)
	}
	claim := syntheticClaim(secret, bytes.Repeat([]byte{2}, 32), testNow)
	result, err := local.ClaimBootstrap(context.Background(), claim)
	if err != nil || result.Replayed {
		t.Fatalf("claim=%+v err=%v", result, err)
	}
	replayed, err := local.ClaimBootstrap(context.Background(), claim)
	if err != nil || !replayed.Replayed || replayed.OwnerID != result.OwnerID || replayed.NodeID != result.NodeID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	changed := claim
	changed.ClaimDigest = bytes.Repeat([]byte{3}, 32)
	if _, err := local.ClaimBootstrap(context.Background(), changed); !errors.Is(err, ErrBootstrapCompleted) {
		t.Fatalf("changed replay error=%v", err)
	}
	owner, err := local.Owner(context.Background())
	if err != nil || owner.ID != claim.OwnerID {
		t.Fatalf("owner=%+v err=%v", owner, err)
	}
	nodes, err := local.Nodes(context.Background())
	if err != nil || len(nodes) != 1 || nodes[0].ID != claim.NodeID || !bytes.Equal(nodes[0].CredentialHash, claim.CredentialHash) {
		t.Fatalf("nodes=%+v err=%v", nodes, err)
	}
	clients, err := local.ControlClients(context.Background())
	if err != nil || len(clients) != 0 {
		t.Fatalf("clients=%+v err=%v", clients, err)
	}
	nodes[0].PublicKey[0] ^= 0xff
	again, _ := local.Nodes(context.Background())
	if nodes[0].PublicKey[0] == again[0].PublicKey[0] {
		t.Fatal("Nodes returned shared bytes")
	}
}

func TestSessionLookupsReturnDetachedActiveAndRevokedRecords(t *testing.T) {
	local, _ := openTestStore(t)
	secret := bytes.Repeat([]byte{0x51}, 32)
	_, _ = local.RotateBootstrap(context.Background(), secret, testNow)
	claim := syntheticClaim(secret, bytes.Repeat([]byte{0x52}, 32), testNow)
	if _, err := local.ClaimBootstrap(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	node, err := local.NodeSession(context.Background(), claim.NodeID)
	if err != nil || node.OwnerID != claim.OwnerID || node.Status != "active" || !bytes.Equal(node.CredentialHash, claim.CredentialHash) {
		t.Fatalf("node session=%+v err=%v", node, err)
	}
	node.PublicKey[0] ^= 0xff
	again, _ := local.NodeSession(context.Background(), claim.NodeID)
	if node.PublicKey[0] == again.PublicKey[0] {
		t.Fatal("node session returned shared key bytes")
	}
	if _, err := local.db.Exec(`INSERT INTO control_clients(id, owner_id, public_key, name, status, created_at)
		VALUES ('cli_test', ?, ?, 'Synthetic Client', 'active', ?)`, claim.OwnerID, bytes.Repeat([]byte{0x53}, 32), timestamp(testNow)); err != nil {
		t.Fatal(err)
	}
	client, err := local.ControlClientSession(context.Background(), "cli_test")
	if err != nil || client.OwnerID != claim.OwnerID || client.Status != "active" {
		t.Fatalf("client session=%+v err=%v", client, err)
	}
	if _, err := local.db.Exec("UPDATE node_credentials SET status='revoked' WHERE node_id=?", claim.NodeID); err != nil {
		t.Fatal(err)
	}
	revoked, err := local.NodeSession(context.Background(), claim.NodeID)
	if err != nil || revoked.Status != "revoked" {
		t.Fatalf("revoked session=%+v err=%v", revoked, err)
	}
	if _, err := local.NodeSession(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing node error=%v", err)
	}
}

func TestBootstrapRetryWindowScrubsAuthorization(t *testing.T) {
	local, _ := openTestStore(t)
	secret := bytes.Repeat([]byte{4}, 32)
	_, _ = local.RotateBootstrap(context.Background(), secret, testNow)
	claim := syntheticClaim(secret, bytes.Repeat([]byte{5}, 32), testNow)
	if _, err := local.ClaimBootstrap(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	claim.Now = claim.RetryUntil.Add(time.Nanosecond)
	claim.RetryUntil = claim.Now.Add(5 * time.Minute)
	if _, err := local.ClaimBootstrap(context.Background(), claim); !errors.Is(err, ErrBootstrapCompleted) {
		t.Fatalf("expired replay error=%v", err)
	}
	var secretHash, digest []byte
	if err := local.db.QueryRow("SELECT secret_hash, claim_digest FROM bootstrap WHERE singleton=1").Scan(&secretHash, &digest); err != nil || secretHash != nil || digest != nil {
		t.Fatalf("bootstrap hashes not scrubbed: %x %x err=%v", secretHash, digest, err)
	}
}

func TestBootstrapStatusAndRestartScrubExpiredRetryHashes(t *testing.T) {
	local, _ := openTestStore(t)
	secret := bytes.Repeat([]byte{0x31}, 32)
	_, _ = local.RotateBootstrap(context.Background(), secret, testNow)
	claim := syntheticClaim(secret, bytes.Repeat([]byte{0x32}, 32), testNow)
	if _, err := local.ClaimBootstrap(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	local.clock = func() time.Time { return claim.RetryUntil.Add(time.Nanosecond) }
	if status, err := local.BootstrapStatus(context.Background()); err != nil || status.State != BootstrapCompleted {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	assertBootstrapHashesCleared(t, local)

	secret = bytes.Repeat([]byte{0x41}, 32)
	local.db.Exec("UPDATE bootstrap SET secret_hash=?, claim_digest=?, retry_until=? WHERE singleton=1", secret, secret, timestamp(claim.RetryUntil))
	if status, err := local.RotateBootstrap(context.Background(), bytes.Repeat([]byte{0x42}, 32), claim.RetryUntil.Add(time.Second)); err != nil || status.State != BootstrapCompleted {
		t.Fatalf("restart rotation=%+v err=%v", status, err)
	}
	assertBootstrapHashesCleared(t, local)
}

func assertBootstrapHashesCleared(t *testing.T, local *Store) {
	t.Helper()
	var secretHash, digest []byte
	if err := local.db.QueryRow("SELECT secret_hash, claim_digest FROM bootstrap WHERE singleton=1").Scan(&secretHash, &digest); err != nil || secretHash != nil || digest != nil {
		t.Fatalf("bootstrap hashes not scrubbed: %x %x err=%v", secretHash, digest, err)
	}
}

func TestConcurrentBootstrapClaimHasOneInitialCommit(t *testing.T) {
	local, _ := openTestStore(t)
	secret := bytes.Repeat([]byte{6}, 32)
	_, _ = local.RotateBootstrap(context.Background(), secret, testNow)
	claim := syntheticClaim(secret, bytes.Repeat([]byte{7}, 32), testNow)
	var initial, replayed atomic.Int32
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := local.ClaimBootstrap(context.Background(), claim)
			if err != nil {
				t.Errorf("claim error=%v", err)
				return
			}
			if result.Replayed {
				replayed.Add(1)
			} else {
				initial.Add(1)
			}
		}()
	}
	group.Wait()
	if initial.Load() != 1 || replayed.Load() != 15 {
		t.Fatalf("initial=%d replayed=%d", initial.Load(), replayed.Load())
	}
}

func syntheticClaim(secret, digest []byte, now time.Time) BootstrapClaim {
	return BootstrapClaim{
		SecretHash: secret, ClaimDigest: digest, OwnerID: "own_test", NodeID: "nod_test", RequestID: "request",
		Name: "Synthetic", OS: "windows", Version: "dev", PublicKey: bytes.Repeat([]byte{8}, 32),
		CredentialHash: bytes.Repeat([]byte{9}, 32), Now: now, RetryUntil: now.Add(5 * time.Minute),
	}
}
