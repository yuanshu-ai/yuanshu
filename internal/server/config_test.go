package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerConfigValidatesIPTLSAndOrigins(t *testing.T) {
	enabled, disabled := true, false
	valid := []ConfigFile{
		{ConfigVersion: 1, DataDir: "/tmp/server", Listen: "127.0.0.1:9527"},
		{ConfigVersion: 1, DataDir: "/tmp/server", Listen: "0.0.0.0:9527", PublicURL: "https://192.168.1.20:9527", TLSCertFile: "/tmp/server.crt", TLSKeyFile: "/tmp/server.key", AllowedControlOrigins: []string{"https://192.168.1.20:4173"}, Web: WebConfig{Enabled: &enabled}},
		{ConfigVersion: 1, DataDir: "/tmp/server", Listen: "[::]:9527", PublicURL: "https://[fd00::20]:9527", TLSCertFile: "/tmp/server.crt", TLSKeyFile: "/tmp/server.key", Web: WebConfig{Enabled: &disabled}},
		{ConfigVersion: 1, DataDir: "/tmp/server", Listen: "127.0.0.1:9527", Admin: AdminConfig{Enabled: &enabled, SessionIdleMinutes: 15, SessionMaxHours: 4, AuditRetentionDays: 30}},
	}
	for _, value := range valid {
		if err := ValidateConfigFile(value); err != nil {
			t.Fatalf("valid configuration rejected: %#v: %v", value, err)
		}
	}
	invalid := []ConfigFile{
		{ConfigVersion: 1, DataDir: "/tmp/server", Listen: "0.0.0.0:9527"},
		{ConfigVersion: 1, DataDir: "/tmp/server", Listen: "127.0.0.1:9527", PublicURL: "http://127.0.0.1:9527", TLSCertFile: "/tmp/server.crt", TLSKeyFile: "/tmp/server.key"},
		{ConfigVersion: 1, DataDir: "/tmp/server", Listen: "127.0.0.1:9527", AllowedControlOrigins: []string{"http://web.example.test"}},
		{ConfigVersion: 1, DataDir: "/tmp/server", Listen: "127.0.0.1:9527", Admin: AdminConfig{SessionIdleMinutes: 2}},
	}
	for _, value := range invalid {
		if err := ValidateConfigFile(value); err == nil {
			t.Fatalf("invalid configuration accepted: %#v", value)
		}
	}
}

func TestServerConfigV2DeploymentModesAndLegacyMigration(t *testing.T) {
	root := t.TempDir()
	valid := []ConfigFile{
		{ConfigVersion: 2, DeploymentMode: DeploymentLocal, DataDir: root, Listen: "127.0.0.1:9527", AllowedControlOrigins: []string{"http://127.0.0.1:9527"}},
		{ConfigVersion: 2, DeploymentMode: DeploymentLocal, DataDir: root, Listen: "[::1]:9527"},
		{ConfigVersion: 2, DeploymentMode: DeploymentLANManaged, DataDir: root, Listen: "0.0.0.0:9527", PublicURL: "https://192.168.10.20:9527"},
		{ConfigVersion: 2, DeploymentMode: DeploymentLANManaged, DataDir: root, Listen: "[::]:9527", PublicURL: "https://[fd00::20]:9527"},
		{ConfigVersion: 2, DeploymentMode: DeploymentPublicIPACME, DataDir: root, Listen: "0.0.0.0:9527", PublicURL: "https://8.8.8.8", ACME: ACMEConfig{Environment: "staging", AcceptTerms: true}},
		{ConfigVersion: 2, DeploymentMode: DeploymentExternal, DataDir: root, Listen: "0.0.0.0:9527", PublicURL: "https://example.test", TLS: TLSFileConfig{Termination: "server", CertFile: filepath.Join(root, "cert.pem"), KeyFile: filepath.Join(root, "key.pem")}},
		{ConfigVersion: 2, DeploymentMode: DeploymentExternal, DataDir: root, Listen: "127.0.0.1:9527", PublicURL: "https://example.test", TLS: TLSFileConfig{Termination: "proxy"}},
	}
	for _, value := range valid {
		if err := ValidateConfigFile(value); err != nil {
			t.Fatalf("valid v2 mode rejected: %#v: %v", value, err)
		}
	}
	invalid := []ConfigFile{
		{ConfigVersion: 2, DeploymentMode: DeploymentLocal, DataDir: root, Listen: "0.0.0.0:9527"},
		{ConfigVersion: 2, DeploymentMode: DeploymentLANManaged, DataDir: root, Listen: "0.0.0.0:9527", PublicURL: "http://192.168.10.20:9527"},
		{ConfigVersion: 2, DeploymentMode: DeploymentLANManaged, DataDir: root, Listen: "0.0.0.0:9527", PublicURL: "https://8.8.8.8:9527"},
		{ConfigVersion: 2, DeploymentMode: DeploymentPublicIPACME, DataDir: root, Listen: "0.0.0.0:9527", PublicURL: "https://192.168.10.20", ACME: ACMEConfig{Environment: "production", AcceptTerms: true}},
		{ConfigVersion: 2, DeploymentMode: DeploymentPublicIPACME, DataDir: root, Listen: "0.0.0.0:9527", PublicURL: "https://8.8.8.8:9527", ACME: ACMEConfig{Environment: "production", AcceptTerms: true}},
		{ConfigVersion: 2, DeploymentMode: DeploymentExternal, DataDir: root, Listen: "0.0.0.0:9527", PublicURL: "https://example.test", TLS: TLSFileConfig{Termination: "proxy"}},
	}
	for _, value := range invalid {
		if err := ValidateConfigFile(value); err == nil {
			t.Fatalf("invalid v2 mode accepted: %#v", value)
		}
	}
	legacy, err := normalizeConfigFile(ConfigFile{ConfigVersion: 1, DataDir: root, Listen: "0.0.0.0:9527", PublicURL: "https://example.test", TLSCertFile: filepath.Join(root, "cert.pem"), TLSKeyFile: filepath.Join(root, "key.pem")})
	if err != nil || legacy.ConfigVersion != 2 || legacy.DeploymentMode != DeploymentExternal || legacy.TLS.Termination != "server" || legacy.TLSCertFile != "" {
		t.Fatalf("legacy migration=%#v err=%v", legacy, err)
	}
}

func TestServerConfigFileStoreRoundTripAndBackup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "server.toml")
	store, err := NewConfigFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	first := ConfigFile{ConfigVersion: 1, DataDir: filepath.Join(root, "data"), Listen: "127.0.0.1:9527"}
	if err := os.MkdirAll(first.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Listen = "127.0.0.1:7555"
	if err := store.Save(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil || loaded.Listen != second.Listen {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil || len(backup) == 0 {
		t.Fatalf("backup unavailable: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestParseServerOptionsConfigPrecedence(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "server.toml")
	store, err := NewConfigFileStore(configPath)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if err := store.Save(context.Background(), ConfigFile{ConfigVersion: 1, DataDir: data, Listen: "127.0.0.1:9527", Web: WebConfig{Enabled: &disabled}}); err != nil {
		t.Fatal(err)
	}
	options, err := parseServerOptions([]string{"--listen", "127.0.0.1:7555", "--config", configPath, "--web"})
	if err != nil || options.DataDir != data || options.Listen != "127.0.0.1:7555" || options.WebEnabled == nil || !*options.WebEnabled {
		t.Fatalf("options=%#v err=%v", options, err)
	}
}

func TestParseServerOptionsLegacyTLSOverridesLocalConfigAsExternal(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "server.toml")
	store, _ := NewConfigFileStore(configPath)
	if err := store.Save(context.Background(), ConfigFile{ConfigVersion: 2, DeploymentMode: DeploymentLocal, DataDir: data, Listen: "127.0.0.1:9527"}); err != nil {
		t.Fatal(err)
	}
	certPath, keyPath, _ := writeServerTestCertificate(t, filepath.Join(root, "tls"), []string{"example.test"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	options, err := parseServerOptions([]string{"--config", configPath, "--listen", "0.0.0.0:9527", "--public-url", "https://example.test", "--tls-cert", certPath, "--tls-key", keyPath})
	if err != nil || options.DeploymentMode != DeploymentExternal || options.TLSTermination != "server" {
		t.Fatalf("options=%+v err=%v", options, err)
	}
}

func TestParseServerOptionsRejectsConflictingWebFlags(t *testing.T) {
	if _, err := parseServerOptions([]string{"--web", "--no-web"}); !errors.Is(err, ErrUsage) {
		t.Fatalf("err=%v", err)
	}
}

func TestServerDoctorReportsTLSIdentity(t *testing.T) {
	root := t.TempDir()
	certPath, keyPath, _ := writeServerTestCertificate(t, filepath.Join(root, "tls"), []string{"localhost"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	configPath := filepath.Join(root, "server.toml")
	store, err := NewConfigFileStore(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), ConfigFile{ConfigVersion: 1, DataDir: filepath.Join(root, "data"), Listen: "127.0.0.1:9527", PublicURL: "https://localhost:9527", TLSCertFile: certPath, TLSKeyFile: keyPath}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := doctor(context.Background(), []string{"--config", configPath, "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	var status doctorStatus
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.State != "ready" || status.TLS != "ready" || len(status.TLSSAN) != 1 || status.TLSSAN[0] != "localhost" || status.TLSNotAfter == "" {
		t.Fatalf("doctor status=%+v", status)
	}
	if status.Web != "enabled" {
		t.Fatalf("web status=%q", status.Web)
	}
}
