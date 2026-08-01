package poc_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex/probe"
)

// TestArchivePOCSyntheticThread is a zero-Turn cleanup guard for interrupted
// archive-on-close validation. It only archives Threads whose cwd exactly
// matches the explicitly supplied disposable PoC workspace.
func TestArchivePOCSyntheticThread(t *testing.T) {
	if os.Getenv("YUANSHU_POC_CLEANUP_LIVE") != "1" {
		t.Skip("set YUANSHU_POC_CLEANUP_LIVE=1 for bounded PoC cleanup")
	}
	workspace, err := filepath.Abs(os.Getenv("YUANSHU_POC_WORKSPACE"))
	if err != nil || workspace == "" {
		t.Fatal("disposable PoC workspace is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, err := probe.Start(ctx, probe.Options{Dir: workspace, Env: probe.Environment()})
	if err != nil {
		t.Fatal("start cleanup client")
	}
	defer client.Close()
	title := "Yuanshu M0 Cleanup"
	if _, err := client.Initialize(ctx, probe.ClientInfo{Name: "yuanshu_m0_cleanup", Title: &title, Version: "0.0.0"}); err != nil {
		t.Fatal("initialize cleanup client")
	}
	type thread struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	}
	var list struct {
		Data []thread `json:"data"`
	}
	if err := client.Call(ctx, "thread/list", map[string]any{"limit": 100, "sourceKinds": []string{"cli", "vscode", "exec", "appServer", "subAgent", "subAgentReview", "subAgentCompact", "subAgentThreadSpawn", "subAgentOther", "unknown"}}, &list); err != nil {
		t.Fatal("list cleanup candidates")
	}
	archived := 0
	for _, item := range list.Data {
		candidate, err := filepath.Abs(item.Cwd)
		if err != nil || !samePath(candidate, workspace) {
			continue
		}
		if err := client.Call(ctx, "thread/archive", map[string]any{"threadId": item.ID}, nil); err != nil {
			t.Fatal("archive synthetic PoC Thread")
		}
		archived++
	}
	if archived != 1 {
		t.Fatalf("archived synthetic PoC Thread count=%d, want 1", archived)
	}
}

func samePath(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }
