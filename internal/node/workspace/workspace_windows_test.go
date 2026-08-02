//go:build windows

package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/yuanshu-ai/yuanshu/internal/config"
	nodestore "github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

func openWindowsManager(t *testing.T) (*Manager, *nodestore.Store) {
	t.Helper()
	local, err := nodestore.Open(context.Background(), filepath.Join(t.TempDir(), "node.db"), nodestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	manager, err := NewManager(platform.Current().Workspaces(), local)
	if err != nil {
		t.Fatal(err)
	}
	return manager, local
}

func TestWindowsManagerRealPathLifecycle(t *testing.T) {
	manager, _ := openWindowsManager(t)
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	configured := workspaceConfig("windows", root, config.PermissionWorkspaceWrite)
	if err := manager.Reconcile(context.Background(), []config.WorkspaceConfig{configured}); err != nil {
		t.Fatal(err)
	}
	registered, err := manager.Resolve(context.Background(), "windows")
	if err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(existing, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := manager.ResolvePath(context.Background(), "windows", "existing.txt", PathRead)
	if err != nil || !resolved.Exists || resolved.Directory {
		t.Fatalf("existing ResolvePath = %+v, %v", resolved, err)
	}
	created, err := manager.ResolvePath(context.Background(), "windows", "new/deep.txt", PathWrite)
	if err != nil || created.Exists || created.Path != filepath.Join(registered.CanonicalPath, "new", "deep.txt") {
		t.Fatalf("create ResolvePath = %+v, %v", created, err)
	}

	old := filepath.Join(parent, "workspace-old")
	if err := os.Rename(root, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resolve(context.Background(), "windows"); !errors.Is(err, ErrStale) {
		t.Fatalf("replacement Resolve error = %v", err)
	}
}

func TestWindowsManagerRejectsJunctionWorkspace(t *testing.T) {
	manager, _ := openWindowsManager(t)
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	junction := filepath.Join(parent, "junction")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, target).Run(); err != nil {
		t.Fatal("synthetic junction setup failed")
	}
	configured := workspaceConfig("junction", junction, config.PermissionWorkspaceWrite)
	if err := manager.Reconcile(context.Background(), []config.WorkspaceConfig{configured}); !errors.Is(err, ErrDenied) {
		t.Fatalf("junction Reconcile error = %v", err)
	}
}
