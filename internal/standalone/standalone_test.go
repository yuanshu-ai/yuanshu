package standalone

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	"github.com/yuanshu-ai/yuanshu/internal/platform/fake"
	"github.com/yuanshu-ai/yuanshu/internal/server"
)

func TestParseArgumentsDefinesFormalStandaloneSurface(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	defaults, err := parseArguments([]string{"--data-dir", root, "--config", configPath})
	if err != nil || defaults.Listen != server.DefaultListenAddress {
		t.Fatalf("default listen = %q, %v", defaults.Listen, err)
	}
	options, err := parseArguments([]string{"run", "--data-dir", root, "--config", configPath, "--listen", "127.0.0.1:7555", "--no-web"})
	if err != nil {
		t.Fatal(err)
	}
	if options.DataDir != root || options.Config != configPath || options.Listen != "127.0.0.1:7555" || options.WebEnabled == nil || *options.WebEnabled {
		t.Fatalf("options = %#v", options)
	}
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"--data-dir", "relative", "--config", configPath},
		{"--data-dir", root, "--config", "relative"},
		{"--data-dir", root, "--config", configPath, "--extra"},
		{"--data-dir", root, "--config", configPath, "--listen", "0.0.0.0:9527"},
		{"--data-dir", root, "--config", configPath, "--web", "--no-web"},
	} {
		if _, err := parseArguments(args); !errors.Is(err, ErrUsage) {
			t.Fatalf("parseArguments(%q) = %v", args, err)
		}
	}
}

func TestRunRejectsCanceledContextWithoutMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() = %v", err)
	}
}

func TestStandaloneCredentialIsCanonicalBearerValue(t *testing.T) {
	credential, err := newStandaloneCredential(strings.NewReader(strings.Repeat("x", 32)))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(credential)
	if len(credential) != 43 || !validStandaloneCredential(credential) {
		t.Fatalf("credential length=%d valid=%v", len(credential), validStandaloneCredential(credential))
	}
	if validStandaloneCredential(bytes.Repeat([]byte{0xff}, 32)) {
		t.Fatal("raw binary credential was accepted")
	}
}

func TestFormalStandaloneStartsServerAndLocalNodeWithSeparateStores(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	configured, err := fake.New(platform.FamilyLinux)
	if err != nil {
		t.Fatal(err)
	}
	if err := configured.FakeWorkspaces().Register(workspacePath, platform.WorkspaceFacts{
		CanonicalPath: workspacePath, FilesystemRoot: filepath.VolumeName(workspacePath) + string(filepath.Separator),
		FileIdentity: "synthetic-workspace", IsDirectory: true,
	}); err != nil {
		t.Fatal(err)
	}
	configuration := config.Config{
		ConfigVersion: config.CurrentVersion,
		Host:          config.HostConfig{Name: "Synthetic Standalone"},
		Transport:     config.TransportConfig{Mode: config.TransportStandalone},
		Relay:         config.RelayConfig{ConnectTimeoutSeconds: 15},
		Identity:      config.IdentityConfig{PrivateKeyRef: "standalone-identity"},
		Adapters:      config.AdaptersConfig{Codex: config.CodexAdapterConfig{Enabled: true, Binary: "synthetic-codex", RuntimeMode: "stdio"}},
		Events:        config.EventsConfig{MaxAgeHours: 24, MaxSizeMiB: 16},
		Workspaces: []config.WorkspaceConfig{{
			ID: "workspace", DisplayName: "Synthetic Workspace", Path: workspacePath,
			AllowedAdapters: []string{"codex"}, DefaultAdapter: "codex", PermissionProfile: config.PermissionReadOnly,
		}},
	}
	configPath := filepath.Join(root, "config.toml")
	fileStore, err := config.NewFileStore(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := fileStore.Save(context.Background(), configuration); err != nil {
		t.Fatalf("save config: %v", err)
	}
	listen := unusedLoopbackAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveSyntheticCodex(ctx, configured.FakeProcesses())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{DataDir: filepath.Join(root, "data"), Config: configPath, Listen: listen, Platform: configured})
	}()
	waitForHealth(t, "http://"+listen+"/healthz")
	for _, path := range []string{filepath.Join(root, "data", "server", "server.db"), filepath.Join(root, "data", "node", "node.db")} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("store %q: info=%v err=%v", filepath.Base(path), info, err)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Standalone did not stop")
	}
}

func unusedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func waitForHealth(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(url) // #nosec G107 -- fixed loopback test URL.
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Standalone health endpoint did not become ready")
}

func serveSyntheticCodex(ctx context.Context, manager *fake.ProcessManager) {
	seen := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		started := manager.Started()
		if len(started) > seen {
			spec := started[seen]
			process := manager.LastProcess()
			seen++
			go func() {
				if len(spec.Args) == 1 && spec.Args[0] == "--version" {
					_ = process.WriteStdout([]byte("codex-cli 0.144.6\n"))
					_ = process.Complete(0)
					return
				}
				scanner := bufio.NewScanner(process.Input())
				for scanner.Scan() {
					var request struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if json.Unmarshal(scanner.Bytes(), &request) != nil || len(request.ID) == 0 {
						continue
					}
					switch request.Method {
					case "initialize":
						_ = process.WriteStdout([]byte(fmt.Sprintf("{\"id\":%s,\"result\":{\"userAgent\":\"codex_cli_rs/0.144.6\"}}\n", request.ID)))
					case "account/read":
						_ = process.WriteStdout([]byte(fmt.Sprintf("{\"id\":%s,\"result\":{\"account\":{\"type\":\"apiKey\"}}}\n", request.ID)))
					}
				}
				_ = process.Complete(0)
			}()
		}
		time.Sleep(time.Millisecond)
	}
}
