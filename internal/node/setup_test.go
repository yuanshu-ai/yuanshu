package node

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
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
	if value.Config.Relay.CredentialRef != "" {
		t.Fatalf("legacy relay credential reference = %q", value.Config.Relay.CredentialRef)
	}
}

func TestNodeSetupUsesLocalCLIWorkspaceWithoutExposingPath(t *testing.T) {
	directory := t.TempDir()
	workspacePath := filepath.Join(directory, "workspace")
	if err := os.Mkdir(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	fakePlatform, _ := platformfake.New(platform.FamilyDarwin)
	fakePlatform.FakeDirectoryPicker().SetError(platform.ErrUnavailable)
	if err := fakePlatform.FakeWorkspaces().Register(workspacePath, platform.WorkspaceFacts{CanonicalPath: workspacePath, FilesystemRoot: directory, FileIdentity: "cli-workspace", IsDirectory: true}); err != nil {
		t.Fatal(err)
	}
	controller := newNodeSetupController(fakePlatform, paths{root: directory}, filepath.Join(directory, "config.toml"), false, nil)
	controller.setLocalWorkspace(workspacePath)
	view := controller.view()
	if view == nil || !view.PickerAvailable {
		t.Fatalf("setup view = %#v", view)
	}
	picked := controller.handle(context.Background(), localRequest{Command: "setup_pick", localSession: "local-session"})
	if !picked.OK || picked.WorkspaceToken == "" || picked.WorkspaceName != "workspace" {
		t.Fatalf("pick response = %#v", picked)
	}
	encoded, err := json.Marshal(picked)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), workspacePath) {
		t.Fatal("local workspace path was exposed to the browser")
	}
}

func TestNodeSetupEmptyCLIWorkspaceDoesNotBecomeCurrentDirectory(t *testing.T) {
	directory := t.TempDir()
	fakePlatform, _ := platformfake.New(platform.FamilyDarwin)
	fakePlatform.FakeDirectoryPicker().SetError(platform.ErrUnavailable)
	controller := newNodeSetupController(fakePlatform, paths{root: directory}, filepath.Join(directory, "config.toml"), false, nil)
	controller.setLocalWorkspace("")
	view := controller.view()
	if view == nil || view.WorkspacePreselected {
		t.Fatalf("empty CLI workspace changed setup capabilities: %#v", view)
	}
	if result := controller.handle(context.Background(), localRequest{Command: "setup_pick", localSession: "session"}); result.OK || result.Error != "native_picker_unavailable" {
		t.Fatalf("empty CLI workspace selected the process directory: %#v", result)
	}
}

func TestNodeSetupClaimsFirstNodeBootstrapOverTrustedTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/healthz":
			_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ok"})
		case "/v1/bootstrap/claim":
			if request.Header.Get("Authorization") != "Bearer bootstrap-secret" {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]string{"ownerId": "owner-1", "nodeId": "node-1", "status": "enrolled"})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	directory := t.TempDir()
	workspacePath := filepath.Join(directory, "workspace")
	fakePlatform, _ := platformfake.New(platform.FamilyDarwin)
	fakePlatform.FakeDirectoryPicker().Set(platform.DirectorySelection{Path: workspacePath, DisplayName: "Workspace"})
	_ = fakePlatform.FakeWorkspaces().Register(workspacePath, platform.WorkspaceFacts{CanonicalPath: workspacePath, FilesystemRoot: directory, FileIdentity: "bootstrap-workspace", IsDirectory: true})
	configPath := filepath.Join(directory, "config.toml")
	controller := newNodeSetupController(fakePlatform, paths{root: directory, database: filepath.Join(directory, "node.db")}, configPath, false, nil)
	controller.httpClient = server.Client()
	relayURL := "wss" + server.URL[len("https"):] + "/node/connect"
	if result := controller.handle(context.Background(), localRequest{Command: "setup_test", RelayURL: relayURL}); !result.OK {
		t.Fatalf("relay test = %#v", result)
	}
	picked := controller.handle(context.Background(), localRequest{Command: "setup_pick", localSession: "session"})
	result := controller.handle(context.Background(), localRequest{Command: "setup_complete", localSession: "session", Name: "First Mac", RelayURL: relayURL, BootstrapSecret: "bootstrap-secret", WorkspaceToken: picked.WorkspaceToken, WorkspaceName: "Workspace", PermissionProfile: "read-only"})
	if !result.OK {
		t.Fatalf("bootstrap setup = %#v", result)
	}
	local, err := store.Open(context.Background(), filepath.Join(directory, "node.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	identity, err := local.Identity(context.Background())
	if err != nil || identity.OwnerID != "owner-1" || identity.NodeID != "node-1" {
		t.Fatalf("bound identity = %#v err=%v", identity, err)
	}
}

func TestNodeSetupStoresValidatedCustomCAInPrivateDirectory(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/healthz" {
			_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ok"})
			return
		}
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	directory := t.TempDir()
	controller := newNodeSetupController(nil, paths{root: directory}, filepath.Join(directory, "config.toml"), false, nil)
	relayURL := "wss" + server.URL[len("https"):] + "/node/connect"
	result := controller.handle(context.Background(), localRequest{Command: "setup_test", RelayURL: relayURL, RelayCABundle: string(ca)})
	if !result.OK {
		t.Fatalf("custom CA relay test=%#v", result)
	}
	if result := controller.handle(context.Background(), localRequest{Command: "setup_test", RelayURL: relayURL, RelayCABundle: string(append(ca, []byte("PRIVATE KEY")...))}); result.OK {
		t.Fatalf("invalid CA bundle accepted: %#v", result)
	}
	caPath := filepath.Join(directory, "relay-ca.crt")
	if err := os.WriteFile(caPath, ca, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := controller.setLocalRelayCA(caPath); err != nil {
		t.Fatal(err)
	}
	if result := controller.handle(context.Background(), localRequest{Command: "setup_test", RelayURL: relayURL}); !result.OK {
		t.Fatalf("CLI-selected CA relay test=%#v", result)
	}
	invalidPath := filepath.Join(directory, "invalid-ca.crt")
	if err := os.WriteFile(invalidPath, []byte("not a CA"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := controller.setLocalRelayCA(invalidPath); err == nil {
		t.Fatal("invalid CLI-selected CA was accepted")
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
	if result.OK || result.Error != "setup_secure_store_unavailable" {
		t.Fatalf("secure store result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(directory, "config.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("configuration was written after secure store failure")
	}
	fakePlatform.FakeSecureStore().SetError(nil)
	if retry := controller.handle(context.Background(), request); retry.Error == "workspace token is invalid" {
		t.Fatalf("workspace token was consumed by a failed setup: %#v", retry)
	}
}
