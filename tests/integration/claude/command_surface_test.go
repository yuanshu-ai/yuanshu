package claude_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const claudeSurfaceLiveEnvironment = "YUANSHU_CLAUDE_SPIKE_LIVE"

func TestInstalledClaudeStructuredSurfaceWithoutTurn(t *testing.T) {
	if os.Getenv(claudeSurfaceLiveEnvironment) != "1" {
		t.Skip("set YUANSHU_CLAUDE_SPIKE_LIVE=1 to run the zero-Turn Claude Code surface probe")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("Claude Code is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	versionOutput, err := exec.CommandContext(ctx, "claude", "--version").Output()
	if err != nil {
		t.Fatal("Claude Code version is unavailable")
	}
	version := strings.TrimSpace(string(versionOutput))
	if version != "2.1.212 (Claude Code)" {
		t.Fatalf("Claude Code version is not the PF-087 baseline")
	}

	help, err := exec.CommandContext(ctx, "claude", "--help").Output()
	if err != nil {
		t.Fatal("Claude Code help is unavailable")
	}
	for _, required := range []string{
		"--input-format", "--output-format", "stream-json", "--resume", "--session-id",
		"--permission-mode", "--include-partial-messages", "--no-session-persistence", "--remote-control",
	} {
		if !bytes.Contains(help, []byte(required)) {
			t.Fatalf("Claude Code structured surface is missing a required flag")
		}
	}

	agentsHelp, err := exec.CommandContext(ctx, "claude", "agents", "--help").Output()
	if err != nil || !bytes.Contains(agentsHelp, []byte("--json")) || !bytes.Contains(agentsHelp, []byte("--cwd")) {
		t.Fatal("Claude Code background-agent inventory is unavailable")
	}

	workspace := t.TempDir()
	output, err := exec.CommandContext(ctx, "claude", "agents", "--json", "--cwd", workspace).Output()
	if err != nil {
		t.Fatal("Claude Code background-agent inventory failed")
	}
	if len(output) > 1<<20 {
		t.Fatal("Claude Code background-agent inventory exceeded the bound")
	}
	var agents []json.RawMessage
	if err := json.Unmarshal(output, &agents); err != nil {
		t.Fatal("Claude Code background-agent inventory is not JSON")
	}
	if len(agents) > 32 {
		t.Fatal("Claude Code background-agent inventory exceeded the item bound")
	}
	t.Logf("PF-087 zero-Turn surface: claude=%s stream_json=true resume=true background_inventory=true items=%d", version, len(agents))
}
