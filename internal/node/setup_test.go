package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	platformfake "github.com/yuanshu-ai/yuanshu/internal/platform/fake"
)

func TestNodeSetupUsesSessionBoundNativeWorkspaceToken(t *testing.T) {
	directory := t.TempDir()
	workspacePath := filepath.Join(directory, "workspace")
	if err := os.Mkdir(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	fakePlatform, _ := platformfake.New(platform.FamilyDarwin)
	fakePlatform.FakeDirectoryPicker().Set(platform.DirectorySelection{Path: workspacePath, DisplayName: "Chosen workspace"})
	if err := fakePlatform.FakeWorkspaces().Register(workspacePath, platform.WorkspaceFacts{CanonicalPath: workspacePath, FilesystemRoot: "/", FileIdentity: "workspace-identity", IsDirectory: true}); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "node", "config.toml")
	root := filepath.Dir(configPath)
	controller := newNodeSetupController(fakePlatform, paths{root: root, config: configPath, database: filepath.Join(root, "node.db"), log: filepath.Join(root, "node.log")}, configPath, false, nil)
	picked := controller.handle(context.Background(), localRequest{Command: "setup_pick", localSession: "session-a"})
	if !picked.OK || picked.WorkspaceToken == "" || picked.WorkspaceName != "Chosen workspace" {
		t.Fatalf("pick response = %#v", picked)
	}
	joinSecret := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	joinKey := "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	request := localRequest{Command: "setup_complete", localSession: "session-b", Name: "Office Mac", RelayURL: "wss://relay.example.test/node/connect", JoinURL: "https://relay.example.test/join#enrollment." + joinSecret + "." + joinKey, WorkspaceToken: picked.WorkspaceToken, WorkspaceName: "Project", PermissionProfile: "read-only", CodexBinary: "codex"}
	if result := controller.handle(context.Background(), request); result.OK || result.Error != "setup_failed" {
		t.Fatalf("cross-session setup = %#v", result)
	}

	picked = controller.handle(context.Background(), localRequest{Command: "setup_pick", localSession: "session-a"})
	request.localSession, request.WorkspaceToken = "session-a", picked.WorkspaceToken
	result := controller.handle(context.Background(), request)
	if !result.OK {
		t.Fatalf("setup result = %#v", result)
	}
	loaded, err := config.NewFileStore(configPath)
	if err != nil {
		t.Fatal(err)
	}
	value, err := loaded.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if value.Config.Host.Name != "Office Mac" || value.Config.Relay.URL != request.RelayURL || len(value.Config.Workspaces) != 1 || value.Config.Workspaces[0].Path != workspacePath || value.Config.Workspaces[0].AllowNetwork {
		t.Fatalf("saved configuration = %#v", value.Config)
	}
	if value.Config.Workspaces[0].PermissionProfile != config.PermissionReadOnly {
		t.Fatalf("permission = %q", value.Config.Workspaces[0].PermissionProfile)
	}
	if _, err := fakePlatform.SecureStore().Get(context.Background(), value.Config.Identity.PrivateKeyRef); err != nil {
		t.Fatalf("identity was not stored securely: %v", err)
	}
	if _, err := fakePlatform.SecureStore().Get(context.Background(), value.Config.Relay.CredentialRef); err != nil {
		t.Fatalf("credential was not stored securely: %v", err)
	}
}

func TestNodeSetupWorkspaceTokenExpiresAndWorkspaceBoundaryIsRechecked(t *testing.T) {
	directory := t.TempDir()
	workspacePath := filepath.Join(directory, "workspace")
	fakePlatform, _ := platformfake.New(platform.FamilyWindows)
	fakePlatform.FakeDirectoryPicker().Set(platform.DirectorySelection{Path: workspacePath, DisplayName: "Workspace"})
	facts := platform.WorkspaceFacts{CanonicalPath: workspacePath, FilesystemRoot: directory, FileIdentity: "workspace-id", IsDirectory: true}
	_ = fakePlatform.FakeWorkspaces().Register(workspacePath, facts)
	now := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	controller := newNodeSetupController(fakePlatform, paths{root: directory, database: filepath.Join(directory, "node.db")}, filepath.Join(directory, "config.toml"), false, nil)
	controller.clock = func() time.Time { return now }
	picked := controller.handle(context.Background(), localRequest{Command: "setup_pick", localSession: "session"})
	now = now.Add(setupWorkspaceTokenTTL + time.Second)
	request := localRequest{Command: "setup_complete", localSession: "session", Name: "PC", RelayURL: "wss://relay.example.test/node/connect", JoinURL: "https://relay.example.test/join#value", WorkspaceToken: picked.WorkspaceToken, WorkspaceName: "Workspace", PermissionProfile: "read-only"}
	if result := controller.handle(context.Background(), request); result.OK || result.Error != "setup_failed" {
		t.Fatalf("expired token result = %#v", result)
	}

	now = now.Add(time.Second)
	picked = controller.handle(context.Background(), localRequest{Command: "setup_pick", localSession: "session"})
	facts.IsHome = true
	_ = fakePlatform.FakeWorkspaces().Register(workspacePath, facts)
	request.WorkspaceToken = picked.WorkspaceToken
	if result := controller.handle(context.Background(), request); result.OK || result.Error != "workspace_denied" {
		t.Fatalf("boundary recheck result = %#v", result)
	}
}

func TestNodeSetupFailsClosedWithoutSecureStore(t *testing.T) {
	directory := t.TempDir()
	fakePlatform, _ := platformfake.New(platform.FamilyDarwin)
	fakePlatform.FakeSecureStore().SetError(platform.ErrUnavailable)
	workspacePath := filepath.Join(directory, "workspace")
	fakePlatform.FakeDirectoryPicker().Set(platform.DirectorySelection{Path: workspacePath, DisplayName: "Workspace"})
	_ = fakePlatform.FakeWorkspaces().Register(workspacePath, platform.WorkspaceFacts{CanonicalPath: workspacePath, FilesystemRoot: directory, FileIdentity: "id", IsDirectory: true})
	controller := newNodeSetupController(fakePlatform, paths{root: directory, database: filepath.Join(directory, "node.db")}, filepath.Join(directory, "config.toml"), false, nil)
	picked := controller.handle(context.Background(), localRequest{Command: "setup_pick", localSession: "session"})
	request := localRequest{Command: "setup_complete", localSession: "session", Name: "Mac", RelayURL: "wss://relay.example.test/node/connect", JoinURL: "https://relay.example.test/join#value", WorkspaceToken: picked.WorkspaceToken, WorkspaceName: "Workspace", PermissionProfile: "read-only"}
	result := controller.handle(context.Background(), request)
	if result.OK || result.Error != "setup_failed" {
		t.Fatalf("secure store result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(directory, "config.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("configuration was written after secure store failure")
	}
}
