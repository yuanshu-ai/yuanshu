package node

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/eventlog"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/node/workspace"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	platformfake "github.com/yuanshu-ai/yuanshu/internal/platform/fake"
)

func TestReloadConfigurationPreservesRuntimeAndEventPumpBoundary(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "node.toml")
	initial := testRemoteConfig()
	file, err := config.NewFileStore(configPath)
	if err != nil || file.Save(ctx, initial) != nil {
		t.Fatalf("save initial config: %v", err)
	}
	local, err := store.Open(ctx, filepath.Join(directory, "node.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	fakePlatform, _ := platformfake.New(platform.FamilyDarwin)
	if err := fakePlatform.FakeWorkspaces().Register(initial.Workspaces[0].Path, platform.WorkspaceFacts{
		CanonicalPath: initial.Workspaces[0].Path, FilesystemRoot: "/", FileIdentity: "workspace-identity", IsDirectory: true,
	}); err != nil {
		t.Fatal(err)
	}
	workspaceManager, err := workspace.NewManager(fakePlatform.Workspaces(), local)
	if err != nil || workspaceManager.Reconcile(ctx, initial.Workspaces) != nil {
		t.Fatalf("workspace setup: %v", err)
	}
	events, err := eventlog.NewManager(local, eventlog.Options{OwnerID: "owner", NodeID: "node", MaxAge: 168 * time.Hour, MaxBytes: 256 << 20})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controlRuntime{events: make(chan adapter.AgentEvent)}
	h := &host{
		options: runOptions{configPath: configPath, platform: fakePlatform}, status: newStatusStore("darwin"), log: newOperationalLog(filepath.Join(directory, "node.log")),
		runCtx: ctx, local: local, runtime: runtime, controlSession: &ControlSession{}, controlEvents: events,
		workspaceManager: workspaceManager, activeConfig: initial,
	}
	updated := cloneNodeConfig(initial)
	updated.Host.Name = "Renamed without restart"
	updated.Events.MaxAgeHours = 48
	updated.Events.MaxSizeMiB = 128
	updated.Workspaces[0].DisplayName = "Renamed workspace"
	if err := file.Save(ctx, updated); err != nil {
		t.Fatal(err)
	}
	if err := h.reloadConfiguration(ctx); err != nil {
		t.Fatal(err)
	}
	if h.runtime != runtime || h.controlSession == nil || h.controlEvents != events {
		t.Fatal("live settings replaced the Runtime or persistent event pump")
	}
	if h.activeConfig.Host.Name != updated.Host.Name || h.activeConfig.Events.MaxAgeHours != 48 {
		t.Fatalf("active config = %+v", h.activeConfig)
	}
	record, err := local.Workspace(ctx, initial.Workspaces[0].ID)
	if err != nil || record.DisplayName != "Renamed workspace" {
		t.Fatalf("workspace record = %+v, %v", record, err)
	}
}

func TestRelayHTTPClientUsesAdditionalCAWithoutDisablingHostnameValidation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	certificate := server.Certificate()
	if certificate == nil {
		t.Fatal("test certificate unavailable")
	}
	caPath := filepath.Join(t.TempDir(), "relay-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := relayHTTPClient("", time.Second, caPath)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("custom CA connection failed: %v", err)
	}
	_ = response.Body.Close()
	transport := client.Transport.(*http.Transport)
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.MinVersion != 0x0304 {
		t.Fatalf("unsafe TLS configuration: %#v", transport.TLSClientConfig)
	}
	if _, err := relayHTTPClient("", time.Second, filepath.Join(t.TempDir(), "missing.pem")); err == nil {
		t.Fatal("missing custom CA accepted")
	}
	if _, err := x509.SystemCertPool(); err != nil {
		t.Logf("system roots unavailable in test environment: %v", err)
	}
}

func TestSameRuntimeBoundaryAllowsOnlyLiveNodeSettings(t *testing.T) {
	base := testRemoteConfig()
	changed := cloneNodeConfig(base)
	changed.Host.Name = "Renamed"
	changed.Relay.URL = "wss://new-relay.example.test"
	changed.Relay.ProxyURL = "http://127.0.0.1:8080"
	changed.Relay.ConnectTimeoutSeconds = 45
	changed.Events.MaxAgeHours = 48
	changed.Events.MaxSizeMiB = 512
	changed.Workspaces[0].DisplayName = "Renamed workspace"
	changed.Workspaces[0].PermissionProfile = config.PermissionWorkspaceWrite
	changed.Workspaces[0].AllowNetwork = true
	if !sameRuntimeBoundary(base, changed) {
		t.Fatal("live Node settings unexpectedly require a Runtime restart")
	}
	if !reflect.DeepEqual(base, testRemoteConfig()) {
		t.Fatal("runtime boundary comparison mutated the active configuration")
	}

	changed = cloneNodeConfig(base)
	changed.AgentInstances[0].Codex.Binary = "/different/codex"
	if sameRuntimeBoundary(base, changed) {
		t.Fatal("Codex executable change did not require a Runtime restart")
	}
	changed = cloneNodeConfig(base)
	changed.Workspaces[0].Path = "/different/workspace"
	if sameRuntimeBoundary(base, changed) {
		t.Fatal("workspace path change did not require a Runtime restart")
	}
}

func TestRelayHTTPClientUsesExplicitSafeProxy(t *testing.T) {
	client, err := relayHTTPClient("http://127.0.0.1:8080", 7*time.Second)
	if err != nil || client.Timeout != 7*time.Second {
		t.Fatalf("relay client = %#v, %v", client, err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		t.Fatal("relay proxy was not configured")
	}
	if _, err := relayHTTPClient("http://user:secret@127.0.0.1:8080", time.Second); err == nil {
		t.Fatal("proxy userinfo was accepted")
	}
}

func cloneNodeConfig(value config.Config) config.Config {
	value.Workspaces = append([]config.WorkspaceConfig(nil), value.Workspaces...)
	value.AgentInstances = append([]config.AgentInstanceConfig(nil), value.AgentInstances...)
	for index := range value.AgentInstances {
		if value.AgentInstances[index].Codex != nil {
			copy := *value.AgentInstances[index].Codex
			value.AgentInstances[index].Codex = &copy
		}
	}
	return value
}
