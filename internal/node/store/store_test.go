package store

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

var fixedNow = time.Date(2026, 8, 1, 12, 0, 0, 123456789, time.UTC)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "node.db")
	store, err := Open(context.Background(), path, Options{Clock: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func TestTrustManifestIsOwnerWideButAppliedPerNodeAndNeverRollsBack(t *testing.T) {
	local, _ := openTestStore(t)
	key := bytes.Repeat([]byte{0x61}, ed25519.PublicKeySize)
	first := TrustManifest{OwnerID: "owner", NodeID: "node-a", Revision: 1, Clients: []TrustedClientRecord{{OwnerID: "owner", NodeID: "node-a", ClientID: "client", KeyID: "key", PublicKey: key, Status: v1.TrustStatusActive}}}
	if err := local.ReconcileTrustManifest(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.NodeID = "node-b"
	second.Clients = []TrustedClientRecord{{OwnerID: "owner", NodeID: "node-b", ClientID: "client", KeyID: "key", PublicKey: key, Status: v1.TrustStatusActive}}
	if err := local.ReconcileTrustManifest(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range []string{"node-a", "node-b"} {
		trusted, err := local.LookupControlKey(context.Background(), v1.KeyRef{OwnerID: "owner", NodeID: nodeID, ClientID: "client", KeyID: "key"})
		if err != nil || trusted.Status != v1.TrustStatusActive {
			t.Fatalf("node %s trust=%+v err=%v", nodeID, trusted, err)
		}
	}
	revoked := TrustManifest{OwnerID: "owner", NodeID: "node-a", Revision: 2, Clients: []TrustedClientRecord{{OwnerID: "owner", NodeID: "node-a", ClientID: "client", KeyID: "key", PublicKey: key, Status: v1.TrustStatusRevoked}}}
	if err := local.ReconcileTrustManifest(context.Background(), revoked); err != nil {
		t.Fatal(err)
	}
	if trusted, _ := local.LookupControlKey(context.Background(), v1.KeyRef{OwnerID: "owner", NodeID: "node-a", ClientID: "client", KeyID: "key"}); trusted.Status != v1.TrustStatusRevoked {
		t.Fatalf("revoked trust=%+v", trusted)
	}
	if err := local.ReconcileTrustManifest(context.Background(), first); !errors.Is(err, ErrConflict) {
		t.Fatalf("old revision error=%v", err)
	}
	if trusted, _ := local.LookupControlKey(context.Background(), v1.KeyRef{OwnerID: "owner", NodeID: "node-b", ClientID: "client", KeyID: "key"}); trusted.Status != v1.TrustStatusActive {
		t.Fatal("one Node manifest changed another Node")
	}
}

func TestOpenMigratesAndReopensSQLite(t *testing.T) {
	store, path := openTestStore(t)
	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != CurrentSchemaVersion {
		t.Fatalf("user_version = %d, %v", version, err)
	}
	var mode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
	wantTables := []string{"identity", "outbox", "replay_messages", "replay_nonces", "runtime_threads", "schema_migrations", "signer_sequences", "trust_manifests", "trusted_clients", "workspaces"}
	for _, table := range wantTables {
		var count int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("table %q count = %d, %v", table, count, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("reopened Close() error = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestOpenRejectsInvalidFutureAndCorruptStores(t *testing.T) {
	if _, err := Open(context.Background(), "relative.db", Options{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative path error = %v", err)
	}
	dir := t.TempDir()
	directoryPath := filepath.Join(dir, "directory")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), directoryPath, Options{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("directory path error = %v", err)
	}

	futurePath := filepath.Join(dir, "future.db")
	future, err := Open(context.Background(), futurePath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := future.db.Exec("INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, 'future', ?)", CurrentSchemaVersion+1, timestamp(fixedNow)); err != nil {
		t.Fatal(err)
	}
	_ = future.Close()
	if _, err := Open(context.Background(), futurePath, Options{}); !errors.Is(err, ErrFutureSchema) {
		t.Fatalf("future schema error = %v", err)
	}
	futureUserPath := filepath.Join(dir, "future-user-version.db")
	futureUser, err := Open(context.Background(), futureUserPath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := futureUser.db.Exec("PRAGMA user_version = " + strconv.Itoa(CurrentSchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	_ = futureUser.Close()
	if _, err := Open(context.Background(), futureUserPath, Options{}); !errors.Is(err, ErrFutureSchema) {
		t.Fatalf("future user_version error = %v", err)
	}

	corruptPath := filepath.Join(dir, "corrupt.db")
	if err := os.WriteFile(corruptPath, []byte("sqlite-corruption-canary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), corruptPath, Options{}); !errors.Is(err, ErrCorrupt) || strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), corruptPath) {
		t.Fatalf("corrupt store returned unsafe error = %v", err)
	}
}

func TestMigrationFailureRollsBack(t *testing.T) {
	db, err := sql.Open("sqlite3", sqliteDSN(filepath.Join(t.TempDir(), "rollback.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	original := nodeMigrations
	nodeMigrations = []migration{{version: 1, name: "broken", statements: []string{
		"CREATE TABLE should_rollback(id INTEGER)",
		"THIS IS NOT SQL",
	}}}
	t.Cleanup(func() { nodeMigrations = original })
	if err := runMigrations(context.Background(), db, fixedNow); err == nil {
		t.Fatal("runMigrations() unexpectedly succeeded")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name IN ('should_rollback', 'schema_migrations')").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed migration left %d tables", count)
	}
}

func TestTrustedKeysPersistAndRevoke(t *testing.T) {
	store, path := openTestStore(t)
	ref := v1.KeyRef{OwnerID: "owner", NodeID: "node", ClientID: "client", KeyID: "key"}
	private := ed25519.NewKeyFromSeed(bytesOf(7, ed25519.SeedSize))
	public := append([]byte(nil), private.Public().(ed25519.PublicKey)...)
	if err := store.PutTrustedKey(context.Background(), ref, v1.TrustedKey{PublicKey: public, Status: v1.TrustStatusActive}); err != nil {
		t.Fatalf("PutTrustedKey() error = %v", err)
	}
	public[0] ^= 0xff
	got, err := store.LookupControlKey(context.Background(), ref)
	if err != nil || got.Status != v1.TrustStatusActive || got.PublicKey[0] == public[0] {
		t.Fatalf("LookupControlKey() = %#v, %v", got, err)
	}
	got.PublicKey[0] ^= 0xff
	again, _ := store.LookupControlKey(context.Background(), ref)
	if again.PublicKey[0] == got.PublicKey[0] {
		t.Fatal("LookupControlKey returned shared storage")
	}
	if err := store.RevokeTrustedKey(context.Background(), ref); err != nil {
		t.Fatalf("RevokeTrustedKey() error = %v", err)
	}
	revoked, err := store.LookupControlKey(context.Background(), ref)
	if err != nil || revoked.Status != v1.TrustStatusRevoked {
		t.Fatalf("revoked key = %#v, %v", revoked, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	revoked, err = reopened.LookupControlKey(context.Background(), ref)
	if err != nil || revoked.Status != v1.TrustStatusRevoked {
		t.Fatal("trusted key did not persist")
	}
}

func TestReplayStoreIsAtomicAndPersistent(t *testing.T) {
	store, path := openTestStore(t)
	base := v1.ReplayRecord{OwnerID: "owner", NodeID: "node", ClientID: "client", KeyID: "key", MessageID: "message", Nonce: "nonce", Sequence: 1, NonceRetainTo: fixedNow.Add(time.Minute)}
	var success atomic.Int32
	var replay atomic.Int32
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			err := store.CheckAndRecord(context.Background(), base)
			if err == nil {
				success.Add(1)
			} else if errors.Is(err, v1.ErrReplayDetected) {
				replay.Add(1)
			} else {
				t.Errorf("CheckAndRecord() error = %v", err)
			}
		}()
	}
	group.Wait()
	if success.Load() != 1 || replay.Load() != 15 {
		t.Fatalf("success = %d, replay = %d", success.Load(), replay.Load())
	}
	for _, record := range []v1.ReplayRecord{
		{OwnerID: "owner", NodeID: "node", ClientID: "client", KeyID: "key", MessageID: "other", Nonce: "nonce", Sequence: 2, NonceRetainTo: fixedNow.Add(time.Minute)},
		{OwnerID: "owner", NodeID: "node", ClientID: "client", KeyID: "key", MessageID: "third", Nonce: "fresh", Sequence: 1, NonceRetainTo: fixedNow.Add(time.Minute)},
	} {
		if err := store.CheckAndRecord(context.Background(), record); !errors.Is(err, v1.ErrReplayDetected) {
			t.Fatalf("replay error = %v", err)
		}
	}
	next := base
	next.MessageID, next.Nonce, next.Sequence = "next", "next-nonce", 2
	if err := store.CheckAndRecord(context.Background(), next); err != nil {
		t.Fatalf("higher sequence error = %v", err)
	}
	rotated := base
	rotated.KeyID, rotated.MessageID, rotated.Nonce = "rotated", "rotated-message", "rotated-nonce"
	if err := store.CheckAndRecord(context.Background(), rotated); err != nil {
		t.Fatalf("rotated key error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	nextAgain := next
	nextAgain.MessageID, nextAgain.Nonce, nextAgain.Sequence = "after-restart", "after-restart-nonce", 2
	if err := reopened.CheckAndRecord(context.Background(), nextAgain); !errors.Is(err, v1.ErrReplayDetected) {
		t.Fatalf("sequence did not persist: %v", err)
	}
	count, err := reopened.PruneExpiredNonces(context.Background(), fixedNow.Add(2*time.Minute))
	if err != nil || count != 3 {
		t.Fatalf("PruneExpiredNonces() = %d, %v", count, err)
	}
}

func TestReplaySequenceIsIndependentPerNode(t *testing.T) {
	local, _ := openTestStore(t)
	for _, nodeID := range []string{"node-a", "node-b"} {
		record := v1.ReplayRecord{OwnerID: "owner", NodeID: nodeID, ClientID: "client", KeyID: "key", MessageID: "message-" + nodeID, Nonce: "nonce-" + nodeID, Sequence: 1, NonceRetainTo: fixedNow.Add(time.Minute)}
		if err := local.CheckAndRecord(context.Background(), record); err != nil {
			t.Fatalf("node %s sequence 1: %v", nodeID, err)
		}
	}
}

func TestReplayStoreIsAtomicAcrossStoreInstances(t *testing.T) {
	first, path := openTestStore(t)
	second, err := Open(context.Background(), path, Options{Clock: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	record := v1.ReplayRecord{OwnerID: "owner", NodeID: "node", ClientID: "client", KeyID: "key", MessageID: "cross-process", Nonce: "cross-process-nonce", Sequence: 1, NonceRetainTo: fixedNow.Add(time.Minute)}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, candidate := range []*Store{first, second} {
		go func(local *Store) {
			<-start
			results <- local.CheckAndRecord(context.Background(), record)
		}(candidate)
	}
	close(start)
	var accepted, replayed int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, v1.ErrReplayDetected):
			replayed++
		default:
			t.Fatalf("CheckAndRecord() error = %v", err)
		}
	}
	if accepted != 1 || replayed != 1 {
		t.Fatalf("accepted = %d, replayed = %d", accepted, replayed)
	}
}

func TestOutboxIsBoundedOrderedAndIdempotent(t *testing.T) {
	store, _ := openTestStore(t)
	firstFrame := []byte(` {"signed":"bytes"} `)
	first := OutboxRecord{MessageID: "m1", StreamID: "stream", Sequence: 1, Frame: firstFrame}
	second := OutboxRecord{MessageID: "m2", StreamID: "stream", Sequence: 2, Frame: []byte("second")}
	if err := store.Enqueue(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	firstFrame[1] = 'X'
	if err := store.Enqueue(context.Background(), first); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed duplicate error = %v", err)
	}
	first.Frame = []byte(` {"signed":"bytes"} `)
	if err := store.Enqueue(context.Background(), first); err != nil {
		t.Fatalf("exact duplicate error = %v", err)
	}
	if err := store.Enqueue(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	pending, err := store.Pending(context.Background(), 10)
	if err != nil || len(pending) != 2 || pending[0].MessageID != "m1" || pending[1].MessageID != "m2" {
		t.Fatalf("Pending() = %#v, %v", pending, err)
	}
	pending[0].Frame[0] = 'X'
	again, _ := store.OutboxRecord(context.Background(), "m1")
	if again.Frame[0] == 'X' {
		t.Fatal("outbox returned shared frame bytes")
	}
	if err := store.Acknowledge(context.Background(), "m1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Acknowledge(context.Background(), "m1"); err != nil {
		t.Fatalf("idempotent acknowledge error = %v", err)
	}
	pending, _ = store.Pending(context.Background(), 10)
	if len(pending) != 1 || pending[0].MessageID != "m2" {
		t.Fatalf("pending after acknowledge = %#v", pending)
	}
	if err := store.Enqueue(context.Background(), OutboxRecord{MessageID: "large", StreamID: "stream", Sequence: 3, Frame: make([]byte, MaxOutboxFrameBytes+1)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize error = %v", err)
	}
	if err := store.Enqueue(context.Background(), OutboxRecord{MessageID: "collision", StreamID: "stream", Sequence: 2, Frame: []byte("other")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("sequence collision error = %v", err)
	}
}

func TestStoreErrorsDoNotExposeCanaries(t *testing.T) {
	store, _ := openTestStore(t)
	canary := strings.Repeat("sensitive-canary", 10)
	err := store.Enqueue(context.Background(), OutboxRecord{MessageID: canary, StreamID: canary, Frame: []byte(canary)})
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("unsafe error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pending(context.Background(), 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed store error = %v", err)
	}
}

func TestStoreHonorsCanceledContext(t *testing.T) {
	store, _ := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Pending(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Pending() error = %v", err)
	}
	if err := store.CheckAndRecord(ctx, v1.ReplayRecord{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("CheckAndRecord() error = %v", err)
	}
	if _, err := Open(ctx, filepath.Join(t.TempDir(), "canceled.db"), Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open() error = %v", err)
	}
}

func TestWorkspaceRecordsReplacePersistAndRemainAtomic(t *testing.T) {
	store, path := openTestStore(t)
	records := []WorkspaceRecord{
		{ID: "alpha", DisplayName: "Alpha", CanonicalPath: `D:\\code\\alpha`, FilesystemRoot: `D:\\`, FileIdentity: "volume:file-alpha", Adapter: "codex", PermissionProfile: workspaceWorkspaceWrite, AllowNetwork: true},
		{ID: "beta", DisplayName: "Beta", CanonicalPath: `D:\\code\\beta`, FilesystemRoot: `D:\\`, FileIdentity: "volume:file-beta", Adapter: "codex", PermissionProfile: workspaceReadOnly},
	}
	if err := store.ReplaceWorkspaces(context.Background(), records); err != nil {
		t.Fatal(err)
	}
	records[0].DisplayName = "mutated"
	got, err := store.Workspace(context.Background(), "alpha")
	if err != nil || got.DisplayName != "Alpha" || !got.AllowNetwork {
		t.Fatalf("Workspace() = %+v, %v", got, err)
	}
	listed, err := store.Workspaces(context.Background())
	if err != nil || len(listed) != 2 || listed[0].ID != "alpha" || listed[1].ID != "beta" {
		t.Fatalf("Workspaces() = %+v, %v", listed, err)
	}
	if err := store.ReplaceWorkspaces(context.Background(), []WorkspaceRecord{{ID: "invalid"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid replace error = %v", err)
	}
	listed, err = store.Workspaces(context.Background())
	if err != nil || len(listed) != 2 {
		t.Fatal("invalid replacement changed existing workspaces")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err = reopened.Workspace(context.Background(), "beta")
	if err != nil || got.PermissionProfile != workspaceReadOnly {
		t.Fatalf("reopened Workspace() = %+v, %v", got, err)
	}
	if _, err := reopened.Workspace(context.Background(), "unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown workspace error = %v", err)
	}
}

func TestWorkspaceMigrationUpgradesSchemaV1(t *testing.T) {
	original := nodeMigrations
	nodeMigrations = append([]migration(nil), original[:1]...)
	path := filepath.Join(t.TempDir(), "node-v1.db")
	versionOne, err := Open(context.Background(), path, Options{})
	if err != nil {
		nodeMigrations = original
		t.Fatal(err)
	}
	if err := versionOne.Close(); err != nil {
		nodeMigrations = original
		t.Fatal(err)
	}
	nodeMigrations = original
	upgraded, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var version int
	if err := upgraded.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != CurrentSchemaVersion {
		t.Fatalf("upgraded user_version = %d, %v", version, err)
	}
	if records, err := upgraded.Workspaces(context.Background()); err != nil || len(records) != 0 {
		t.Fatalf("upgraded Workspaces() = %+v, %v", records, err)
	}
}

func TestWorkspaceStoreCancellationAndSanitizedErrors(t *testing.T) {
	store, _ := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.ReplaceWorkspaces(ctx, []WorkspaceRecord{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ReplaceWorkspaces error = %v", err)
	}
	if _, err := store.Workspaces(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Workspaces error = %v", err)
	}
	const canary = "workspace-storage-sensitive-canary"
	err := store.ReplaceWorkspaces(context.Background(), []WorkspaceRecord{{ID: canary}})
	if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), canary) {
		t.Fatalf("unsafe workspace error = %v", err)
	}
}

func TestIdentityFactoryRollbackAndBinding(t *testing.T) {
	store, _ := openTestStore(t)
	rollbackCalled := false
	_, _, err := store.LoadOrCreateIdentity(context.Background(), func(context.Context) (IdentityRecord, func(), error) {
		return IdentityRecord{Algorithm: "ed25519", PublicKey: make([]byte, 31), PrivateKeyRef: "ref", CreatedAt: fixedNow, UpdatedAt: fixedNow}, func() { rollbackCalled = true }, nil
	})
	if !errors.Is(err, ErrInvalid) || !rollbackCalled {
		t.Fatalf("invalid factory = %v, rollback = %v", err, rollbackCalled)
	}
	public := bytesOf(3, ed25519.PublicKeySize)
	record, created, err := store.LoadOrCreateIdentity(context.Background(), func(context.Context) (IdentityRecord, func(), error) {
		return IdentityRecord{Algorithm: "ed25519", PublicKey: public, PrivateKeyRef: "identity/ref", CreatedAt: fixedNow, UpdatedAt: fixedNow}, nil, nil
	})
	if err != nil || !created || !bytes.Equal(record.PublicKey, public) {
		t.Fatalf("LoadOrCreateIdentity() = %#v, %v, %v", record, created, err)
	}
	public[0] ^= 0xff
	loaded, err := store.Identity(context.Background())
	if err != nil || loaded.PublicKey[0] == public[0] {
		t.Fatal("identity storage did not isolate public key bytes")
	}
	if err := store.BindIdentity(context.Background(), "owner", "node"); err != nil {
		t.Fatal(err)
	}
	if err := store.BindIdentity(context.Background(), "other", "other"); !errors.Is(err, ErrConflict) {
		t.Fatalf("rebind error = %v", err)
	}
}

func TestReadOnlyInspectionReportsCurrentSchema(t *testing.T) {
	database, path := openTestStore(t)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(context.Background(), path)
	if err != nil || inspection.SchemaVersion != CurrentSchemaVersion || inspection.QuickCheck != "ok" {
		t.Fatalf("Inspect = %+v, %v", inspection, err)
	}
}

func bytesOf(value byte, size int) []byte {
	result := make([]byte, size)
	for index := range result {
		result[index] = value
	}
	return result
}
