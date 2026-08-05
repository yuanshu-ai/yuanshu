package node

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	platformfake "github.com/yuanshu-ai/yuanshu/internal/platform/fake"
)

func TestPrepareNodeIdentityRepairMovesLegacyProfile(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	locations := paths{root: root, config: configPath, database: filepath.Join(root, "node.db"), log: filepath.Join(root, "node.log")}
	configurationStore, err := config.NewFileStore(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := configurationStore.Save(context.Background(), config.Config{
		ConfigVersion:  config.CurrentVersion,
		Host:           config.HostConfig{Name: "legacy-node"},
		Transport:      config.TransportConfig{Mode: config.TransportRelay},
		Relay:          config.RelayConfig{URL: "wss://relay.example.test/node/connect", ConnectTimeoutSeconds: 15},
		Identity:       config.IdentityConfig{PrivateKeyRef: "legacy/identity"},
		AgentInstances: []config.AgentInstanceConfig{{ID: config.DefaultCodexInstanceID, AdapterType: "codex", DisplayName: "Codex", Enabled: true, IsDefault: true, RuntimeMode: config.AgentRuntimeManaged, Codex: &config.CodexAdapterConfig{Enabled: true, Binary: "codex", RuntimeMode: "stdio"}}},
		Events:         config.EventsConfig{MaxAgeHours: 24, MaxSizeMiB: 16},
		Workspaces:     []config.WorkspaceConfig{},
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{locations.database, locations.log, filepath.Join(root, "relay-ca.pem"), filepath.Join(root, "identity.key")} {
		if err := os.WriteFile(path, []byte("legacy"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	current, _ := platformfake.New(platform.FamilyDarwin)
	backup, err := prepareNodeIdentityRepair(context.Background(), current, locations, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(backup) != root {
		t.Fatalf("backup path = %q", backup)
	}
	if info, err := os.Stat(backup); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("backup directory = %v, %v", info, err)
	}
	for _, path := range []string{configPath, locations.database, locations.log, filepath.Join(root, "relay-ca.pem"), filepath.Join(root, "identity.key")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("legacy file %q still exists: %v", path, err)
		}
		if _, err := os.Stat(filepath.Join(backup, filepath.Base(path))); err != nil {
			t.Fatalf("backup file for %q: %v", path, err)
		}
	}
}

func TestPrepareNodeIdentityRepairRejectsUnsafeConfig(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := os.Symlink(filepath.Join(root, "missing.toml"), configPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	current, _ := platformfake.New(platform.FamilyDarwin)
	_, err := prepareNodeIdentityRepair(context.Background(), current, paths{root: root}, configPath)
	if err == nil {
		t.Fatal("unsafe legacy config was accepted")
	}
	if err == platform.ErrInvalidArgument {
		t.Fatal("unexpected platform argument error")
	}
}
