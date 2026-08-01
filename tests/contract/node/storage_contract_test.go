package node_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/identity"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/node/workspace"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	platformfake "github.com/yuanshu-ai/yuanshu/internal/platform/fake"
	v1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

func TestIdentitySecurityAndOutboxSurviveRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "node.db")
	now := time.Date(2026, 8, 1, 14, 0, 0, 0, time.UTC)
	secrets := platformfake.NewSecureStore()
	local, err := store.Open(ctx, path, store.Options{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	seed := bytes.Repeat([]byte{0x29}, ed25519.SeedSize)
	manager, err := identity.NewManager(local, secrets, "node/identity", identity.Options{Random: bytes.NewReader(seed), Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Bind(ctx, "owner", "node"); err != nil {
		t.Fatal(err)
	}
	keyRef := v1.KeyRef{OwnerID: "owner", NodeID: "node", ClientID: "client", KeyID: "key"}
	controlPrivate := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x37}, ed25519.SeedSize))
	if err := local.PutTrustedKey(ctx, keyRef, v1.TrustedKey{PublicKey: controlPrivate.Public().(ed25519.PublicKey), Status: v1.TrustStatusActive}); err != nil {
		t.Fatal(err)
	}
	replay := v1.ReplayRecord{OwnerID: "owner", NodeID: "node", ClientID: "client", KeyID: "key", MessageID: "control-1", Nonce: "nonce-1", Sequence: 9, NonceRetainTo: now.Add(time.Minute)}
	if err := local.CheckAndRecord(ctx, replay); err != nil {
		t.Fatal(err)
	}
	frame := []byte(`{"protocolVersion":"1.0","type":"runtime.status"}`)
	if err := local.Enqueue(ctx, store.OutboxRecord{MessageID: "event-1", StreamID: "node-events", Sequence: 1, Frame: frame}); err != nil {
		t.Fatal(err)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(ctx, path, store.Options{Clock: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedManager, err := identity.NewManager(reopened, secrets, "node/identity", identity.Options{})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopenedManager.Load(ctx)
	if err != nil || !bytes.Equal(loaded.PublicKey, created.PublicKey) || loaded.OwnerID != "owner" || loaded.NodeID != "node" {
		t.Fatalf("reloaded identity = %#v, %v", loaded, err)
	}
	trusted, err := reopened.LookupControlKey(ctx, keyRef)
	if err != nil || trusted.Status != v1.TrustStatusActive {
		t.Fatalf("reloaded trust = %#v, %v", trusted, err)
	}
	next := replay
	next.MessageID, next.Nonce = "control-2", "nonce-2"
	if err := reopened.CheckAndRecord(ctx, next); !errors.Is(err, v1.ErrReplayDetected) {
		t.Fatalf("persisted replay boundary error = %v", err)
	}
	pending, err := reopened.Pending(ctx, 10)
	if err != nil || len(pending) != 1 || !bytes.Equal(pending[0].Frame, frame) {
		t.Fatalf("reloaded outbox = %#v, %v", pending, err)
	}
}

func TestWorkspacePolicyMapsOpaqueIDAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "node.db")
	workspacePath := filepath.Join(t.TempDir(), "local-sensitive-workspace")
	inspector := platformfake.NewWorkspaceInspector()
	facts := platform.WorkspaceFacts{
		CanonicalPath:  workspacePath,
		FilesystemRoot: filepath.VolumeName(workspacePath) + string(filepath.Separator),
		FileIdentity:   "synthetic-workspace-identity",
		IsDirectory:    true,
	}
	if err := inspector.Register(workspacePath, facts); err != nil {
		t.Fatal(err)
	}
	local, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := workspace.NewManager(inspector, local)
	if err != nil {
		t.Fatal(err)
	}
	configured := config.WorkspaceConfig{
		ID: "workspace-1", DisplayName: "Synthetic Workspace", Path: workspacePath,
		AllowedAdapters: []string{"codex"}, DefaultAdapter: "codex",
		PermissionProfile: config.PermissionWorkspaceWrite,
	}
	if err := manager.Reconcile(ctx, []config.WorkspaceConfig{configured}); err != nil {
		t.Fatal(err)
	}
	listed, err := manager.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != "workspace-1" {
		t.Fatalf("List() = %+v, %v", listed, err)
	}
	if strings.Contains(fmt.Sprintf("%+v", listed), workspacePath) {
		t.Fatal("workspace listing exposed the local path")
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted, err := workspace.NewManager(inspector, reopened)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := restarted.Resolve(ctx, "workspace-1")
	if err != nil || resolved.CanonicalPath != workspacePath || resolved.PermissionProfile != config.PermissionWorkspaceWrite {
		t.Fatalf("Resolve() = %+v, %v", resolved, err)
	}
	replacement := facts
	replacement.FileIdentity = "replacement-identity"
	if err := inspector.Register(workspacePath, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Resolve(ctx, "workspace-1"); !errors.Is(err, workspace.ErrStale) {
		t.Fatalf("replacement Resolve error = %v", err)
	}
}
