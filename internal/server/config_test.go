package server

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerConfigValidatesIPTLSAndOrigins(t *testing.T) {
	valid := []ConfigFile{
		{ConfigVersion: 1, DataDir: "/tmp/server", Listen: "127.0.0.1:7444"},
		{ConfigVersion: 1, DataDir: "/tmp/server", Listen: "0.0.0.0:7444", PublicURL: "https://192.168.1.20:7444", TLSCertFile: "/tmp/server.crt", TLSKeyFile: "/tmp/server.key", AllowedControlOrigins: []string{"https://192.168.1.20:4173"}},
		{ConfigVersion: 1, DataDir: "/tmp/server", Listen: "[::]:7444", PublicURL: "https://[fd00::20]:7444", TLSCertFile: "/tmp/server.crt", TLSKeyFile: "/tmp/server.key"},
	}
	for _, value := range valid {
		if err := ValidateConfigFile(value); err != nil {
			t.Fatalf("valid configuration rejected: %#v: %v", value, err)
		}
	}
	invalid := []ConfigFile{
		{ConfigVersion: 1, DataDir: "/tmp/server", Listen: "0.0.0.0:7444"},
		{ConfigVersion: 1, DataDir: "/tmp/server", Listen: "127.0.0.1:7444", PublicURL: "http://127.0.0.1:7444", TLSCertFile: "/tmp/server.crt", TLSKeyFile: "/tmp/server.key"},
		{ConfigVersion: 1, DataDir: "/tmp/server", Listen: "127.0.0.1:7444", AllowedControlOrigins: []string{"http://web.example.test"}},
	}
	for _, value := range invalid {
		if err := ValidateConfigFile(value); err == nil {
			t.Fatalf("invalid configuration accepted: %#v", value)
		}
	}
}

func TestServerConfigFileStoreRoundTripAndBackup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "server.toml")
	store, err := NewConfigFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	first := ConfigFile{ConfigVersion: 1, DataDir: filepath.Join(root, "data"), Listen: "127.0.0.1:7444"}
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
	if err := store.Save(context.Background(), ConfigFile{ConfigVersion: 1, DataDir: data, Listen: "127.0.0.1:7444"}); err != nil {
		t.Fatal(err)
	}
	options, err := parseServerOptions([]string{"--listen", "127.0.0.1:7555", "--config", configPath})
	if err != nil || options.DataDir != data || options.Listen != "127.0.0.1:7555" {
		t.Fatalf("options=%#v err=%v", options, err)
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
	if err := store.Save(context.Background(), ConfigFile{ConfigVersion: 1, DataDir: filepath.Join(root, "data"), Listen: "127.0.0.1:7444", PublicURL: "https://localhost:7444", TLSCertFile: certPath, TLSKeyFile: keyPath}); err != nil {
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
}
