package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/pelletier/go-toml/v2"
	platformpkg "github.com/yuanshu-ai/yuanshu/internal/platform"
	"github.com/yuanshu-ai/yuanshu/internal/platform/fake"
)

func TestValidateConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"version zero", func(value *Config) { value.ConfigVersion = 0 }},
		{"future version", func(value *Config) { value.ConfigVersion = 2 }},
		{"empty host", func(value *Config) { value.Host.Name = "" }},
		{"control in host", func(value *Config) { value.Host.Name = "bad\nname" }},
		{"direct transport", func(value *Config) { value.Transport.Mode = "direct" }},
		{"empty relay URL", func(value *Config) { value.Relay.URL = "" }},
		{"non WSS relay", func(value *Config) { value.Relay.URL = "https://relay.example.test" }},
		{"relay userinfo", func(value *Config) { value.Relay.URL = "wss://user:pass@relay.example.test" }},
		{"relay query", func(value *Config) { value.Relay.URL = "wss://relay.example.test?token=value" }},
		{"bad proxy", func(value *Config) { value.Relay.ProxyURL = "ftp://proxy.example.test" }},
		{"proxy userinfo", func(value *Config) { value.Relay.ProxyURL = "http://user:pass@proxy.example.test" }},
		{"short timeout", func(value *Config) { value.Relay.ConnectTimeoutSeconds = 0 }},
		{"long timeout", func(value *Config) { value.Relay.ConnectTimeoutSeconds = 121 }},
		{"control in secret ref", func(value *Config) { value.Identity.PrivateKeyRef = "bad\nref" }},
		{"daemon runtime", func(value *Config) { value.Adapters.Codex.RuntimeMode = "daemon" }},
		{"enabled without binary", func(value *Config) { value.Adapters.Codex.Binary = "" }},
		{"event age", func(value *Config) { value.Events.MaxAgeHours = 0 }},
		{"event size", func(value *Config) { value.Events.MaxSizeMiB = 15 }},
		{"duplicate workspace ID", func(value *Config) {
			copy := value.Workspaces[0]
			copy.Path = "SECOND_SYNTHETIC_PATH"
			value.Workspaces = append(value.Workspaces, copy)
		}},
		{"duplicate workspace path", func(value *Config) {
			copy := value.Workspaces[0]
			copy.ID = "workspace-2"
			value.Workspaces = append(value.Workspaces, copy)
		}},
		{"unknown adapter", func(value *Config) { value.Workspaces[0].AllowedAdapters = []string{"other"} }},
		{"default adapter", func(value *Config) { value.Workspaces[0].DefaultAdapter = "other" }},
		{"permission", func(value *Config) { value.Workspaces[0].PermissionProfile = "dangerous" }},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			value := validConfig("host")
			testCase.mutate(&value)
			if err := Validate(value); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}

	standalone := validConfig("standalone")
	standalone.Transport.Mode = TransportStandalone
	standalone.Relay.URL = ""
	if err := Validate(standalone); err != nil {
		t.Fatalf("standalone configuration: %v", err)
	}
}

func TestDecodeIsStrictAndVersioned(t *testing.T) {
	valid := mustEncode(t, validConfig("strict"))
	tests := []struct {
		name string
		raw  []byte
		want error
	}{
		{"unknown field", append(valid, []byte("\nunknown_field = \"SENSITIVE_CANARY\"\n")...), ErrInvalid},
		{"duplicate key", append(valid, []byte("\nconfig_version = 1\n")...), ErrInvalid},
		{"invalid type", []byte("config_version = \"one\""), ErrInvalid},
		{"missing version", []byte("[host]\nname = \"node\""), ErrInvalid},
		{"missing sections", []byte("config_version = 1\n"), ErrInvalid},
		{"zero version", []byte("config_version = 0"), ErrInvalid},
		{"future version", []byte("config_version = 2"), ErrUnsupportedVersion},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, err := decode(testCase.raw)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
			if strings.Contains(err.Error(), "SENSITIVE_CANARY") {
				t.Fatal("decode error exposed configuration content")
			}
		})
	}
	decoded, migrated, err := decode(valid)
	if err != nil || migrated || !reflect.DeepEqual(decoded, validConfig("strict")) {
		t.Fatalf("v1 identity decode = %+v, migrated=%v, error=%v", decoded, migrated, err)
	}
}

func TestEmbeddedSchemaMatchesPublishedSchema(t *testing.T) {
	published, err := os.ReadFile(filepath.Join("..", "..", "schemas", "config", "v1", "node-config.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var embeddedDocument, publishedDocument any
	if err := json.Unmarshal([]byte(embeddedSchemaJSON), &embeddedDocument); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(published, &publishedDocument); err != nil {
		t.Fatal(err)
	}
	embeddedCanonical, _ := json.Marshal(embeddedDocument)
	publishedCanonical, _ := json.Marshal(publishedDocument)
	if !bytes.Equal(embeddedCanonical, publishedCanonical) {
		t.Fatal("embedded configuration Schema drifted from the published Schema")
	}
}

func TestFileStoreRoundTripBackupAndRecovery(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "node.toml")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	first := validConfig("first")
	if err := store.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	firstBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil || loaded.RecoveredFromBackup || loaded.Migrated || !reflect.DeepEqual(loaded.Config, first) {
		t.Fatalf("first load = %+v, error=%v", loaded, err)
	}

	second := validConfig("second")
	if err := store.Save(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	backupBytes, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if string(backupBytes) != string(firstBytes) {
		t.Fatal("backup did not preserve the previous valid configuration")
	}
	secondBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("SENSITIVE_CORRUPT_CANARY = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Load(context.Background())
	if err != nil || !recovered.RecoveredFromBackup || !reflect.DeepEqual(recovered.Config, first) {
		t.Fatalf("recovery = %+v, error=%v", recovered, err)
	}
	stillCorrupt, _ := os.ReadFile(path)
	if string(stillCorrupt) != "SENSITIVE_CORRUPT_CANARY = [" {
		t.Fatal("Load rewrote the corrupt primary")
	}

	if err := store.Save(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	preservedBackup, _ := os.ReadFile(path + ".bak")
	if string(preservedBackup) != string(firstBytes) {
		t.Fatal("saving over a corrupt primary replaced the known-good backup")
	}
	stableSecond, _ := os.ReadFile(path)
	if string(stableSecond) != string(secondBytes) {
		t.Fatal("deterministic TOML encoding changed across saves")
	}
}

func TestLoadBackupRules(t *testing.T) {
	t.Run("missing primary", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "node.toml")
		if err := os.WriteFile(path+".bak", mustEncode(t, validConfig("backup")), 0o600); err != nil {
			t.Fatal(err)
		}
		store, _ := NewFileStore(path)
		result, err := store.Load(context.Background())
		if err != nil || !result.RecoveredFromBackup || result.Config.Host.Name != "backup" {
			t.Fatalf("result=%+v error=%v", result, err)
		}
	})

	t.Run("future primary never downgrades", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "node.toml")
		if err := os.WriteFile(path, []byte("config_version = 2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".bak", mustEncode(t, validConfig("backup")), 0o600); err != nil {
			t.Fatal(err)
		}
		store, _ := NewFileStore(path)
		if _, err := store.Load(context.Background()); !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("unsafe primary never falls back", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "node.toml")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".bak", mustEncode(t, validConfig("backup")), 0o600); err != nil {
			t.Fatal(err)
		}
		store, _ := NewFileStore(path)
		if _, err := store.Load(context.Background()); !errors.Is(err, ErrUnsafeFile) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestSaveFailureAndCancellationPreservePrimary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.toml")
	store, _ := NewFileStore(path)
	old := validConfig("old")
	if err := store.Save(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	oldBytes, _ := os.ReadFile(path)
	if err := store.Save(context.Background(), validConfig("backup-seed")); err != nil {
		t.Fatal(err)
	}
	// Restore old as the primary while retaining a valid backup.
	if err := os.WriteFile(path, oldBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	calls := 0
	store.writeAtomic = func(ctx context.Context, target string, value []byte) error {
		calls++
		if calls == 1 {
			return errors.New("synthetic backup failure SENSITIVE_CANARY")
		}
		return atomicWriteFile(ctx, target, value)
	}
	if err := store.Save(context.Background(), validConfig("new")); !errors.Is(err, ErrIO) {
		t.Fatalf("backup failure error=%v", err)
	}
	assertFileEquals(t, path, oldBytes)

	calls = 0
	store.writeAtomic = func(ctx context.Context, target string, value []byte) error {
		calls++
		if calls == 2 {
			return errors.New("synthetic primary failure SENSITIVE_CANARY")
		}
		return atomicWriteFile(ctx, target, value)
	}
	if err := store.Save(context.Background(), validConfig("new-primary-failure")); !errors.Is(err, ErrIO) || strings.Contains(err.Error(), "SENSITIVE") {
		t.Fatalf("primary failure error=%v", err)
	}
	assertFileEquals(t, path, oldBytes)

	store.writeAtomic = atomicWriteFile
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Save(canceled, validConfig("canceled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Save error=%v", err)
	}
	assertFileEquals(t, path, oldBytes)
}

func TestAtomicWriteCleansTemporaryFileAfterRenameFailure(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(context.Background(), target, []byte("synthetic")); err == nil {
		t.Fatal("rename over directory unexpectedly succeeded")
	}
	matches, err := filepath.Glob(filepath.Join(directory, ".yuanshu-config-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}

func TestConcurrentStoresAlwaysLeaveValidConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.toml")
	const writers = 24
	var wg sync.WaitGroup
	for index := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store, err := NewFileStore(path)
			if err != nil {
				t.Errorf("NewFileStore: %v", err)
				return
			}
			if err := store.Save(context.Background(), validConfig("concurrent-"+string(rune('a'+index)))); err != nil {
				t.Errorf("Save: %v", err)
			}
		}()
	}
	wg.Wait()
	store, _ := NewFileStore(path)
	if _, err := store.Load(context.Background()); err != nil {
		t.Fatalf("final configuration: %v", err)
	}
	if _, err := loadFile(context.Background(), path+".bak"); err != nil {
		t.Fatalf("final backup: %v", err)
	}
}

func TestSecretReferenceStatesAndSanitization(t *testing.T) {
	value := validConfig("secrets")
	value.Identity.PrivateKeyRef = "identity-ref"
	value.Relay.CredentialRef = "relay-ref"
	value.Relay.ProxyCredentialRef = ""
	store := fake.NewSecureStore()
	if err := store.Put(context.Background(), "identity-ref", []byte("SENSITIVE_SECRET_CANARY")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "node.toml")
	fileStore, _ := NewFileStore(path)
	if err := fileStore.Save(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	serialized, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "SENSITIVE_SECRET_CANARY") {
		t.Fatal("configuration serialized a Secret value")
	}
	report, err := CheckSecretRefs(context.Background(), value, store)
	if err != nil {
		t.Fatal(err)
	}
	if report[SecretIdentityPrivateKey] != SecretAvailable ||
		report[SecretRelayCredential] != SecretMissing ||
		report[SecretProxyCredential] != SecretUnset {
		t.Fatalf("report=%v", report)
	}

	unavailable, err := CheckSecretRefs(context.Background(), value, nil)
	if err != nil || unavailable[SecretIdentityPrivateKey] != SecretUnavailable ||
		unavailable[SecretRelayCredential] != SecretUnavailable ||
		unavailable[SecretProxyCredential] != SecretUnset {
		t.Fatalf("unavailable report=%v error=%v", unavailable, err)
	}

	tracking := &trackingSecretStore{secret: []byte("SENSITIVE_TRACKED_SECRET")}
	value.Relay.CredentialRef = ""
	if _, err := CheckSecretRefs(context.Background(), value, tracking); err != nil {
		t.Fatal(err)
	}
	for _, character := range tracking.secret {
		if character != 0 {
			t.Fatal("secret copy was not cleared")
		}
	}

	store.SetError(errors.New("SENSITIVE_ERROR_CANARY"))
	_, err = CheckSecretRefs(context.Background(), value, store)
	if !errors.Is(err, ErrSecretCheck) || strings.Contains(err.Error(), "SENSITIVE") {
		t.Fatalf("secret error=%v", err)
	}
}

type trackingSecretStore struct{ secret []byte }

func (*trackingSecretStore) Available() bool { return true }
func (*trackingSecretStore) Put(context.Context, platformpkg.SecretRef, []byte) error {
	return errors.New("not used")
}
func (s *trackingSecretStore) Get(context.Context, platformpkg.SecretRef) ([]byte, error) {
	return s.secret, nil
}
func (*trackingSecretStore) Delete(context.Context, platformpkg.SecretRef) error {
	return errors.New("not used")
}

func validConfig(host string) Config {
	return Config{
		ConfigVersion: CurrentVersion,
		Host:          HostConfig{Name: host},
		Transport:     TransportConfig{Mode: TransportRelay},
		Relay: RelayConfig{
			URL:                   "wss://relay.example.test/node/connect",
			ConnectTimeoutSeconds: 15,
		},
		Adapters: AdaptersConfig{Codex: CodexAdapterConfig{
			Enabled: true, Binary: "codex", RuntimeMode: "stdio",
		}},
		Events: EventsConfig{MaxAgeHours: 24, MaxSizeMiB: 256},
		Workspaces: []WorkspaceConfig{{
			ID: "workspace-1", DisplayName: "Synthetic workspace", Path: "SYNTHETIC_WORKSPACE_PATH",
			AllowedAdapters: []string{"codex"}, DefaultAdapter: "codex",
			PermissionProfile: PermissionWorkspaceWrite,
		}},
	}
}

func mustEncode(t *testing.T, value Config) []byte {
	t.Helper()
	encoded, err := encodeForTest(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func encodeForTest(value Config) ([]byte, error) {
	return toml.Marshal(value)
}

func assertFileEquals(t *testing.T, path string, expected []byte) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(expected) {
		t.Fatal("configuration file changed after failed save")
	}
}
