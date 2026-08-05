package workspace

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yuanshu-ai/yuanshu/internal/config"
	nodestore "github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	"github.com/yuanshu-ai/yuanshu/internal/platform/fake"
)

func openTestManager(t *testing.T) (*Manager, *fake.WorkspaceInspector, *nodestore.Store) {
	t.Helper()
	workspaceStore, err := nodestore.Open(context.Background(), filepath.Join(t.TempDir(), "node.db"), nodestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	inspector := fake.NewWorkspaceInspector()
	manager, err := NewManager(inspector, workspaceStore)
	if err != nil {
		t.Fatal(err)
	}
	return manager, inspector, workspaceStore
}

func workspaceConfig(id, path string, permission config.PermissionProfile) config.WorkspaceConfig {
	return config.WorkspaceConfig{
		ID: id, DisplayName: "Synthetic " + id, Path: path,
		AllowedAgentInstances: []string{config.DefaultCodexInstanceID}, DefaultAgentInstance: config.DefaultCodexInstanceID,
		PermissionProfile: permission,
	}
}

func workspaceFacts(path, identity string) platform.WorkspaceFacts {
	return platform.WorkspaceFacts{
		CanonicalPath: path, FilesystemRoot: filepath.VolumeName(path) + string(filepath.Separator),
		FileIdentity: identity, IsDirectory: true,
	}
}

func TestManagerReconcileListResolveAndRestart(t *testing.T) {
	manager, inspector, workspaceStore := openTestManager(t)
	root := filepath.Join(t.TempDir(), "workspace-sensitive-path")
	if err := inspector.Register(root, workspaceFacts(root, "identity-alpha")); err != nil {
		t.Fatal(err)
	}
	configured := workspaceConfig("alpha", root, config.PermissionWorkspaceWrite)
	configured.AllowNetwork = true
	if err := manager.Reconcile(context.Background(), []config.WorkspaceConfig{configured}); err != nil {
		t.Fatal(err)
	}
	descriptors, err := manager.List(context.Background())
	if err != nil || len(descriptors) != 1 || descriptors[0].ID != "alpha" || !descriptors[0].AllowNetwork {
		t.Fatalf("List() = %+v, %v", descriptors, err)
	}
	if strings.Contains(fmt.Sprintf("%+v", descriptors), root) {
		t.Fatal("public descriptor exposed the local path")
	}
	resolved, err := manager.Resolve(context.Background(), "alpha")
	if err != nil || resolved.CanonicalPath != root || resolved.FileIdentity != "identity-alpha" {
		t.Fatalf("Resolve() = %+v, %v", resolved, err)
	}
	restarted, err := NewManager(inspector, workspaceStore)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Resolve(context.Background(), "alpha"); err != nil {
		t.Fatalf("Resolve after restart = %v", err)
	}
}

func TestManagerReconcileRejectsUnsafeAndRemainsAtomic(t *testing.T) {
	manager, inspector, _ := openTestManager(t)
	root := filepath.Join(t.TempDir(), "safe")
	if err := inspector.Register(root, workspaceFacts(root, "safe-identity")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(context.Background(), []config.WorkspaceConfig{workspaceConfig("safe", root, config.PermissionReadOnly)}); err != nil {
		t.Fatal(err)
	}

	unsafeCases := []platform.WorkspaceFacts{
		{CanonicalPath: root, FilesystemRoot: root, FileIdentity: "root", IsDirectory: true, IsFilesystemRoot: true},
		{CanonicalPath: root, FilesystemRoot: root, FileIdentity: "home", IsDirectory: true, IsHome: true},
		{CanonicalPath: root, FilesystemRoot: root, FileIdentity: "system", IsDirectory: true, IsSystem: true},
		{CanonicalPath: root, FilesystemRoot: root, FileIdentity: "reparse", IsDirectory: true, CrossesReparseBoundary: true},
		{CanonicalPath: root, FilesystemRoot: root, FileIdentity: "link", IsDirectory: true, CrossesLinkBoundary: true},
		{CanonicalPath: root, FilesystemRoot: root, FileIdentity: "file"},
	}
	for index, facts := range unsafeCases {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("unsafe-%d", index))
		facts.CanonicalPath = path
		if err := inspector.Register(path, facts); err != nil {
			t.Fatal(err)
		}
		if err := manager.Reconcile(context.Background(), []config.WorkspaceConfig{workspaceConfig("unsafe", path, config.PermissionReadOnly)}); !errors.Is(err, ErrDenied) {
			t.Fatalf("unsafe case %d error = %v", index, err)
		}
	}
	descriptors, err := manager.List(context.Background())
	if err != nil || len(descriptors) != 1 || descriptors[0].ID != "safe" {
		t.Fatal("failed reconciliation changed the registered workspace set")
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if err := manager.Reconcile(context.Background(), []config.WorkspaceConfig{workspaceConfig("missing", missing, config.PermissionReadOnly)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing path error = %v", err)
	}
}

func TestManagerRejectsDuplicateCanonicalPathsAndIdentity(t *testing.T) {
	manager, inspector, _ := openTestManager(t)
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	if err := inspector.Register(first, workspaceFacts(first, "same-identity")); err != nil {
		t.Fatal(err)
	}
	if err := inspector.Register(second, workspaceFacts(second, "same-identity")); err != nil {
		t.Fatal(err)
	}
	configured := []config.WorkspaceConfig{
		workspaceConfig("one", first, config.PermissionReadOnly),
		workspaceConfig("two", second, config.PermissionReadOnly),
	}
	if err := manager.Reconcile(context.Background(), configured); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate identity error = %v", err)
	}
	secondFacts := workspaceFacts(strings.ToUpper(first), "different-identity")
	if err := inspector.Register(second, secondFacts); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(context.Background(), configured); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate canonical path error = %v", err)
	}
}

func TestManagerDetectsDeletedMovedOrReplacedWorkspace(t *testing.T) {
	manager, inspector, _ := openTestManager(t)
	root := filepath.Join(t.TempDir(), "workspace")
	if err := inspector.Register(root, workspaceFacts(root, "original-identity")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(context.Background(), []config.WorkspaceConfig{workspaceConfig("workspace", root, config.PermissionWorkspaceWrite)}); err != nil {
		t.Fatal(err)
	}
	if err := inspector.Register(root, workspaceFacts(root, "replacement-identity")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resolve(context.Background(), "workspace"); !errors.Is(err, ErrStale) {
		t.Fatalf("replacement Resolve error = %v", err)
	}
	inspector = fake.NewWorkspaceInspector()
	restarted, err := NewManager(inspector, manager.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Resolve(context.Background(), "workspace"); !errors.Is(err, ErrStale) {
		t.Fatalf("deleted Resolve error = %v", err)
	}
}

func TestManagerResolvePathEnforcesContainmentAndPermission(t *testing.T) {
	manager, inspector, _ := openTestManager(t)
	root := filepath.Join(t.TempDir(), "workspace")
	file := filepath.Join(root, "src", "existing.txt")
	parent := filepath.Join(root, "new")
	if err := inspector.Register(root, workspaceFacts(root, "workspace-identity")); err != nil {
		t.Fatal(err)
	}
	fileFacts := workspaceFacts(file, "file-identity")
	fileFacts.IsDirectory = false
	if err := inspector.Register(file, fileFacts); err != nil {
		t.Fatal(err)
	}
	if err := inspector.Register(parent, workspaceFacts(parent, "parent-identity")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(context.Background(), []config.WorkspaceConfig{workspaceConfig("workspace", root, config.PermissionWorkspaceWrite)}); err != nil {
		t.Fatal(err)
	}
	got, err := manager.ResolvePath(context.Background(), "workspace", "src/existing.txt", PathRead)
	if err != nil || !got.Exists || got.Directory || got.Path != file {
		t.Fatalf("existing ResolvePath = %+v, %v", got, err)
	}
	created, err := manager.ResolvePath(context.Background(), "workspace", "new/deep/file.txt", PathWrite)
	if err != nil || created.Exists || created.Path != filepath.Join(parent, "deep", "file.txt") {
		t.Fatalf("create ResolvePath = %+v, %v", created, err)
	}
	if _, err := manager.ResolvePath(context.Background(), "workspace", "missing.txt", PathRead); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing read error = %v", err)
	}

	invalid := []string{"../escape", "/absolute", `C:/drive`, `back\\slash`, "double//slash", "./dot", "trailing/"}
	for _, value := range invalid {
		if _, err := manager.ResolvePath(context.Background(), "workspace", value, PathRead); !errors.Is(err, ErrInvalid) {
			t.Fatalf("path %q error = %v", value, err)
		}
	}
	junctionInput := filepath.Join(root, "junction")
	junctionFacts := workspaceFacts(filepath.Join(t.TempDir(), "outside"), "junction-identity")
	junctionFacts.CrossesReparseBoundary = true
	if err := inspector.Register(junctionInput, junctionFacts); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ResolvePath(context.Background(), "workspace", "junction", PathRead); !errors.Is(err, ErrDenied) {
		t.Fatalf("junction path error = %v", err)
	}

	readOnly := workspaceConfig("workspace", root, config.PermissionReadOnly)
	if err := manager.Reconcile(context.Background(), []config.WorkspaceConfig{readOnly}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ResolvePath(context.Background(), "workspace", "new/file.txt", PathWrite); !errors.Is(err, ErrDenied) {
		t.Fatalf("read-only write error = %v", err)
	}
}

func TestManagerErrorsAreSanitizedContextAwareAndConcurrent(t *testing.T) {
	manager, inspector, _ := openTestManager(t)
	const canary = "workspace-sensitive-canary"
	if _, err := manager.Resolve(context.Background(), canary); !errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), canary) {
		t.Fatalf("unsafe Resolve error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.Reconcile(ctx, []config.WorkspaceConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Reconcile error = %v", err)
	}
	root := filepath.Join(t.TempDir(), canary)
	if err := inspector.Register(root, workspaceFacts(root, "concurrent-identity")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(context.Background(), []config.WorkspaceConfig{workspaceConfig("concurrent", root, config.PermissionReadOnly)}); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := manager.Resolve(context.Background(), "concurrent"); err != nil {
				t.Errorf("concurrent Resolve error = %v", err)
			}
		}()
	}
	wait.Wait()
}
