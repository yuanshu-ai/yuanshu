package codex_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex/probe"
)

const runtimeModeLiveEnvironment = "YUANSHU_CODEX_RUNTIME_LIVE"

func TestRuntimeModeLive(t *testing.T) {
	if os.Getenv(runtimeModeLiveEnvironment) != "1" {
		t.Skip("set YUANSHU_CODEX_RUNTIME_LIVE=1 to run the zero-Turn runtime mode probe")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	versionOutput, err := exec.CommandContext(ctx, "codex", "--version").Output()
	if err != nil {
		t.Fatalf("read Codex version: %v", err)
	}
	version := strings.TrimSpace(string(versionOutput))
	if version != "codex-cli "+schemaVersion {
		t.Fatalf("Codex version mismatch: got a different version, want %s", schemaVersion)
	}

	assertRuntimeCLISurface(t, ctx)

	workspace := t.TempDir()
	authModes := make([]probe.AuthMode, 0, 2)
	for attempt := 1; attempt <= 2; attempt++ {
		client, err := startRuntimeModeClient(ctx, workspace)
		if err != nil {
			t.Fatalf("stdio lifecycle attempt %d: %v", attempt, err)
		}

		var accountResult json.RawMessage
		if err := client.Call(ctx, "account/read", map[string]any{"refreshToken": false}, &accountResult); err != nil {
			failure := safeClientError(client, err)
			_ = client.Close()
			t.Fatalf("stdio lifecycle attempt %d account/read: %v", attempt, failure)
		}
		authMode, err := probe.ClassifyAuth(accountResult)
		accountResult = nil
		if err != nil {
			_ = client.Close()
			t.Fatalf("stdio lifecycle attempt %d classify authentication: %v", attempt, err)
		}
		if authMode == probe.AuthNone {
			_ = client.Close()
			t.Fatalf("stdio lifecycle attempt %d returned no reusable authentication", attempt)
		}
		authModes = append(authModes, authMode)

		if err := client.Close(); err != nil && !errors.Is(err, probe.ErrClosed) {
			t.Fatalf("stdio lifecycle attempt %d close: %v", attempt, safeClientError(client, err))
		}
	}

	if authModes[0] != authModes[1] {
		t.Fatal("stdio app-server restarts did not preserve the coarse authentication mode")
	}

	daemonResult := "not-evaluated-on-this-platform"
	if runtime.GOOS == "windows" {
		output, err := exec.CommandContext(ctx, "codex", "app-server", "daemon", "version").CombinedOutput()
		if err == nil {
			t.Fatal("app-server daemon unexpectedly reported lifecycle support on Windows")
		}
		redacted := strings.ToLower(probe.RedactText(string(output)))
		if !strings.Contains(redacted, "only supported on unix platforms") {
			t.Fatal("app-server daemon did not report the expected Unix-only lifecycle boundary")
		}
		daemonResult = "unsupported"
	}

	t.Logf("AC-004 runtime result: codex=%s platform=%s transport=stdio starts=%d auth=%s daemon=%s turns=0", schemaVersion, runtime.GOOS, len(authModes), authModes[0], daemonResult)
}

func startRuntimeModeClient(ctx context.Context, workspace string) (*probe.Client, error) {
	client, err := probe.Start(ctx, probe.Options{Dir: workspace})
	if err != nil {
		return nil, err
	}
	title := "Yuanshu AC-004 Runtime Probe"
	if _, err := client.Initialize(ctx, probe.ClientInfo{Name: "yuanshu_runtime_probe", Title: &title, Version: "0.0.0"}); err != nil {
		failure := safeClientError(client, err)
		_ = client.Close()
		return nil, failure
	}
	return client, nil
}

func assertRuntimeCLISurface(t *testing.T, ctx context.Context) {
	t.Helper()

	daemonHelp, err := exec.CommandContext(ctx, "codex", "app-server", "daemon", "--help").Output()
	if err != nil {
		t.Fatalf("read app-server daemon help: %v", err)
	}
	if !strings.Contains(string(daemonHelp), "start") || !strings.Contains(string(daemonHelp), "version") {
		t.Fatal("app-server daemon help is missing the expected lifecycle commands")
	}

	proxyHelp, err := exec.CommandContext(ctx, "codex", "app-server", "proxy", "--help").Output()
	if err != nil {
		t.Fatalf("read app-server proxy help: %v", err)
	}
	if !strings.Contains(string(proxyHelp), "Proxy stdio bytes") {
		t.Fatal("app-server proxy help is missing the expected control-socket description")
	}
}
