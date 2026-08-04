package node

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
)

func TestNodeConfigControllerRedactsAndUsesRevisionedPendingChanges(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "node.toml")
	file, err := config.NewFileStore(configPath)
	if err != nil {
		t.Fatal(err)
	}
	initial := testRemoteConfig()
	if err := file.Save(ctx, initial); err != nil {
		t.Fatal(err)
	}
	local, err := store.Open(ctx, filepath.Join(directory, "node.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	controller, err := newNodeConfigController(configPath, local, func() time.Time { return time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}

	view, err := controller.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := view["identity"]; ok {
		t.Fatal("redacted config exposed identity")
	}
	if _, ok := view["workspaces"].([]any); !ok {
		t.Fatalf("workspaces view has type %T", view["workspaces"])
	}
	if got := view["revision"].(string); got == "" {
		t.Fatal("missing config revision")
	}
	baseRevision := view["revision"].(string)

	if _, err := controller.Update(ctx, "stale", map[string]any{"hostName": "ignored"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale revision error = %v", err)
	}
	result, err := controller.Update(ctx, baseRevision, map[string]any{"hostName": "Updated Node", "eventsMaxAgeHours": 72})
	if err != nil || !result.Reload {
		t.Fatalf("safe update result=%+v err=%v", result, err)
	}
	loaded, err := file.Load(ctx)
	if err != nil || loaded.Config.Host.Name != "Updated Node" || loaded.Config.Events.MaxAgeHours != 72 {
		t.Fatalf("safe update config=%+v err=%v", loaded.Config, err)
	}

	view, err = controller.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err = controller.Update(ctx, view["revision"].(string), map[string]any{"relayUrl": "wss://new-relay.example.test"})
	if err != nil || result.Reload || result.Payload["requiresLocalConfirmation"] != true {
		t.Fatalf("protected update result=%+v err=%v", result, err)
	}
	pending, err := controller.Pending(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	summary := configChangeSummary(pending[0], loaded.Config)
	if len(summary.Fields) != 1 || summary.Fields[0] != "relayUrl" {
		t.Fatalf("pending summary fields = %v", summary.Fields)
	}
	if summary.Risk != "high" || !summary.RelayReconnect || summary.PermissionChange != "unchanged" || len(summary.Details) != 1 || summary.Details[0].Before != "wss://relay.example.test" || summary.Details[0].After != "wss://new-relay.example.test" {
		t.Fatalf("pending safety summary = %#v", summary)
	}
	if _, err := controller.Approve(ctx, pending[0].ID); err != nil {
		t.Fatal(err)
	}
	loaded, err = file.Load(ctx)
	if err != nil || loaded.Config.Relay.URL != "wss://new-relay.example.test" {
		t.Fatalf("approved relay=%q err=%v", loaded.Config.Relay.URL, err)
	}
}

func TestNodeConfigControllerRejectsExpiredPendingChange(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "node.toml")
	file, _ := config.NewFileStore(configPath)
	if err := file.Save(ctx, testRemoteConfig()); err != nil {
		t.Fatal(err)
	}
	local, err := store.Open(ctx, filepath.Join(directory, "node.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	controller, _ := newNodeConfigController(configPath, local, func() time.Time { return now })
	view, _ := controller.Read(ctx)
	result, err := controller.Update(ctx, view["revision"].(string), map[string]any{"relayUrl": "wss://new.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(configChangeTTL + time.Second)
	if _, err := controller.Approve(ctx, result.Payload["changeId"].(string)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expired approval error = %v", err)
	}
	change, err := local.ConfigChange(ctx, result.Payload["changeId"].(string))
	if err != nil || change.State != store.ConfigChangeRejected || change.ErrorCode != "config_change_expired" {
		t.Fatalf("expired change = %#v err=%v", change, err)
	}
}

func testRemoteConfig() config.Config {
	return config.Config{
		ConfigVersion: 1,
		Host:          config.HostConfig{Name: "Test Node"},
		Transport:     config.TransportConfig{Mode: config.TransportRelay},
		Relay:         config.RelayConfig{URL: "wss://relay.example.test", ConnectTimeoutSeconds: 30, CredentialRef: "relay-secret"},
		Identity:      config.IdentityConfig{PrivateKeyRef: "private-key"},
		Adapters:      config.AdaptersConfig{Codex: config.CodexAdapterConfig{RuntimeMode: "stdio"}},
		Events:        config.EventsConfig{MaxAgeHours: 168, MaxSizeMiB: 256},
		Workspaces: []config.WorkspaceConfig{{
			ID: "workspace-1", DisplayName: "Work", Path: "/private/workspace", AllowedAdapters: []string{"codex"}, DefaultAdapter: "codex", PermissionProfile: config.PermissionReadOnly,
		}},
	}
}
