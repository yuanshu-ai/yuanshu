package codex_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

const desktopSharedRuntimeLiveEnvironment = "YUANSHU_CODEX_DESKTOP_SHARED_RUNTIME_LIVE"

// TestDesktopSharedRuntimeLive verifies whether the installed macOS Desktop
// exposes a documented way to join an explicitly owned shared app-server. It
// does not inspect Desktop processes, discover private endpoints, open a task,
// or invoke a model.
func TestDesktopSharedRuntimeLive(t *testing.T) {
	if os.Getenv(desktopSharedRuntimeLiveEnvironment) != "1" {
		t.Skip("set YUANSHU_CODEX_DESKTOP_SHARED_RUNTIME_LIVE=1 to run the bounded Desktop probe")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("PF-092 currently verifies the installed macOS Desktop surface")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	binary := sharedRuntimeCodexBinary(t)
	version := sharedRuntimeCodexVersion(t, ctx, binary)

	help, err := exec.CommandContext(ctx, binary, "app", "--help").Output()
	if err != nil {
		t.Fatal("Codex Desktop launcher help is unavailable")
	}
	remoteDocumented := bytes.Contains(help, []byte("--remote")) ||
		bytes.Contains(help, []byte("app-server")) || bytes.Contains(help, []byte("socket"))

	unsupportedOutput, unsupportedErr := exec.CommandContext(ctx, binary, "app", "--remote",
		"unix:///private/tmp/yuanshu-pf092-nonexistent.sock").CombinedOutput()
	remoteRejected := unsupportedErr != nil && bytes.Contains(unsupportedOutput, []byte("unexpected argument '--remote'"))

	daemonRunning, daemonMode, doctorOK := desktopDaemonStatus(ctx, binary)
	t.Logf("PF-092 Desktop shared-runtime evidence: codex=%s os=%s app_installed=true remote_documented=%t remote_rejected=%t daemon_running=%t daemon_mode=%s model_turn=false private_endpoint_scan=false",
		version, runtime.GOOS, remoteDocumented, remoteRejected, daemonRunning, daemonMode)

	if !doctorOK {
		t.Fatal("redacted Codex doctor did not report app-server status")
	}
	if remoteDocumented || !remoteRejected {
		t.Fatal("the public Desktop launcher surface changed; run a positive shared-endpoint Gate before updating compatibility")
	}
}

func desktopDaemonStatus(ctx context.Context, binary string) (bool, string, bool) {
	output, err := exec.CommandContext(ctx, binary, "doctor", "--json").Output()
	if err != nil && len(output) == 0 {
		return false, "unknown", false
	}
	var report struct {
		Checks map[string]struct {
			ID      string         `json:"id"`
			Summary string         `json:"summary"`
			Details map[string]any `json:"details"`
		} `json:"checks"`
	}
	if json.Unmarshal(output, &report) != nil {
		return false, "unknown", false
	}
	check, ok := report.Checks["app_server.status"]
	if !ok || check.ID != "app_server.status" {
		return false, "unknown", false
	}
	mode, _ := check.Details["mode"].(string)
	status, _ := check.Details["status"].(string)
	running := strings.EqualFold(status, "running") || strings.Contains(strings.ToLower(check.Summary), "running") && !strings.Contains(strings.ToLower(check.Summary), "not running")
	if mode == "" {
		mode = "unknown"
	}
	return running, mode, true
}
