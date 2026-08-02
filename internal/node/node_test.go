package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex"
	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	platformfake "github.com/yuanshu-ai/yuanshu/internal/platform/fake"
)

func TestLocalManagementStatusAndStop(t *testing.T) {
	fakePlatform, _ := platformfake.New(platform.FamilyWindows)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := startLocalServer(ctx, fakePlatform.IPC(), func() Status {
		return Status{Version: 1, State: "unpaired", Platform: "windows"}
	}, cancel)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	response, err := callLocal(context.Background(), fakePlatform.IPC(), "status")
	if err != nil || !response.OK || response.Status == nil || response.Status.State != "unpaired" {
		t.Fatalf("status = %+v, %v", response, err)
	}
	if _, err := callLocal(context.Background(), fakePlatform.IPC(), "stop"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel Node")
	}
}

func TestHostKeepsManagementAvailableForInvalidConfiguration(t *testing.T) {
	root := t.TempDir()
	fakePlatform, _ := platformfake.New(platform.FamilyWindows)
	tray := &testTray{started: make(chan trayCallbacks, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runHost(ctx, runOptions{
			paths:    paths{root: root, config: filepath.Join(root, "missing.toml"), database: filepath.Join(root, "node.db"), log: filepath.Join(root, "node.log")},
			platform: fakePlatform, tray: tray,
		})
	}()
	select {
	case <-tray.started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("tray did not start")
	}
	response, err := callLocal(context.Background(), fakePlatform.IPC(), "status")
	if err != nil || response.Status == nil || response.Status.State != "needs_attention" {
		cancel()
		t.Fatalf("status = %+v, %v", response, err)
	}
	if _, err := callLocal(context.Background(), fakePlatform.IPC(), "stop"); err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHostAssemblesFormalUnpairedNode(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	configuration := config.Config{
		ConfigVersion: config.CurrentVersion,
		Host:          config.HostConfig{Name: "synthetic-node"},
		Transport:     config.TransportConfig{Mode: config.TransportStandalone},
		Relay:         config.RelayConfig{ConnectTimeoutSeconds: 15},
		Identity:      config.IdentityConfig{PrivateKeyRef: "identity/synthetic"},
		Adapters:      config.AdaptersConfig{Codex: config.CodexAdapterConfig{Enabled: true, Binary: "codex", RuntimeMode: "stdio"}},
		Events:        config.EventsConfig{MaxAgeHours: 24, MaxSizeMiB: 16},
		Workspaces:    []config.WorkspaceConfig{},
	}
	configurationStore, err := config.NewFileStore(configPath)
	if err != nil || configurationStore.Save(context.Background(), configuration) != nil {
		t.Fatalf("configuration setup: %v", err)
	}
	fakePlatform, _ := platformfake.New(platform.FamilyWindows)
	processes := fakePlatform.FakeProcesses()
	processReady := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if process := processes.LastProcess(); process != nil {
				if err := process.WriteStdout([]byte("codex-cli " + codex.SupportedVersion + "\n")); err != nil {
					processReady <- err
					return
				}
				processReady <- process.Complete(0)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		processReady <- errors.New("Codex version process did not start")
	}()
	tray := &testTray{started: make(chan trayCallbacks, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runHost(ctx, runOptions{
			paths:    paths{root: root, config: configPath, database: filepath.Join(root, "node.db"), log: filepath.Join(root, "node.log")},
			platform: fakePlatform, tray: tray,
		})
	}()
	select {
	case err := <-processReady:
		if err != nil {
			cancel()
			t.Fatalf("prepare Codex version process: %v", err)
		}
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("Codex version process did not finish")
	}
	select {
	case <-tray.started:
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("formal Node did not finish assembly")
	}
	response, err := callLocal(context.Background(), fakePlatform.IPC(), "status")
	if err != nil || response.Status == nil || response.Status.State != "unpaired" || response.Status.Codex != "ready" || response.Status.Database != "ready" {
		cancel()
		t.Fatalf("status = %+v, %v", response, err)
	}
	if _, err := callLocal(context.Background(), fakePlatform.IPC(), "stop"); err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticsAndLogDoNotExposeCanaries(t *testing.T) {
	canary := "secret=credential-canary"
	status := Status{Version: 1, State: "needs_attention", Platform: "windows", Config: "invalid", RemoteControl: "not_available"}
	encoded, err := marshalStatus(status, true)
	if err != nil || strings.Contains(string(encoded), "credential-canary") {
		t.Fatal("status exposed canary")
	}
	path := filepath.Join(t.TempDir(), "node.log")
	logger := newOperationalLog(path)
	logger.write("node_error", canary, 0)
	if value, err := os.ReadFile(path); err == nil && strings.Contains(string(value), "credential-canary") {
		t.Fatal("log exposed canary")
	}
	if _, err := json.Marshal(status); err != nil {
		t.Fatal(err)
	}
}

type testTray struct {
	mu      sync.Mutex
	status  Status
	started chan trayCallbacks
}

func (t *testTray) Run(ctx context.Context, callbacks trayCallbacks) error {
	t.started <- callbacks
	<-ctx.Done()
	return nil
}
func (t *testTray) Update(status Status) {
	t.mu.Lock()
	t.status = status
	t.mu.Unlock()
}

func TestNodeFlagParsing(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	parsed, background, jsonOutput, err := parseNodeFlags([]string{"--background", "--config", configPath}, "default", false, true)
	if err != nil || parsed != configPath || !background || jsonOutput {
		t.Fatalf("parse = %q %v %v %v", parsed, background, jsonOutput, err)
	}
	if _, _, _, err := parseNodeFlags([]string{"--json"}, "default", false, false); !errors.Is(err, ErrUsage) {
		t.Fatalf("unexpected flag error = %v", err)
	}
}
