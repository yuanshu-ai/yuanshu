package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
)

func TestServerInitCreatesPrivateLoopbackConfiguration(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config", "server.toml")
	dataDir := filepath.Join(root, "data")
	var output bytes.Buffer
	err := initializeServer(context.Background(), []string{
		"--config", configPath, "--mode", "loopback", "--data-dir", dataDir,
		"--listen", "127.0.0.1:7555", "--non-interactive",
	}, strings.NewReader(""), &output)
	if err != nil {
		t.Fatal(err)
	}
	value, err := LoadConfigFile(configPath)
	if err != nil || value.DataDir != dataDir || value.Listen != "127.0.0.1:7555" {
		t.Fatalf("config=%+v err=%v", value, err)
	}
	for _, path := range []string{filepath.Dir(configPath), dataDir} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("directory %s mode=%v err=%v", path, info.Mode().Perm(), err)
		}
	}
	if info, err := os.Stat(configPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode=%v err=%v", info.Mode().Perm(), err)
	}
	if !strings.Contains(output.String(), "Workbench: http://127.0.0.1:7555/") {
		t.Fatalf("output=%q", output.String())
	}
	if err := initializeServer(context.Background(), []string{"--config", configPath, "--mode", "loopback", "--non-interactive"}, strings.NewReader(""), &output); err == nil {
		t.Fatal("existing configuration was overwritten without --replace")
	}
}

func TestServerInitRequiresMatchingTLSIdentityForLAN(t *testing.T) {
	root := t.TempDir()
	certPath, keyPath, _ := writeServerTestCertificate(t, filepath.Join(root, "tls"), []string{"192.0.2.20"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	configPath := filepath.Join(root, "server.toml")
	args := []string{"--config", configPath, "--mode", "lan", "--data-dir", filepath.Join(root, "data"), "--listen", "0.0.0.0:7444", "--public-url", "https://192.0.2.20:7444", "--tls-cert", certPath, "--tls-key", keyPath, "--allowed-control-origin", "https://192.0.2.20:7444", "--non-interactive"}
	if err := initializeServer(context.Background(), args, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfigFile(configPath)
	if err != nil || loaded.PublicURL != "https://192.0.2.20:7444" || len(loaded.AllowedControlOrigins) != 1 {
		t.Fatalf("LAN config=%#v err=%v", loaded, err)
	}
	wrongCert, wrongKey, _ := writeServerTestCertificate(t, filepath.Join(root, "wrong"), []string{"192.0.2.21"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	wrong := append([]string(nil), args...)
	wrong[1], wrong[11], wrong[13] = filepath.Join(root, "wrong.toml"), wrongCert, wrongKey
	if err := initializeServer(context.Background(), wrong, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("LAN initialization accepted a certificate without the public IP SAN")
	}
}

func TestServerInitManagedLANCreatesCAWithoutCertificatePaths(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "server.toml")
	dataDir := filepath.Join(root, "data")
	var output bytes.Buffer
	err := initializeServer(context.Background(), []string{
		"--config", configPath, "--mode", "lan-managed", "--data-dir", dataDir,
		"--listen", "0.0.0.0:7444", "--public-url", "https://192.168.50.20:7444", "--non-interactive",
	}, strings.NewReader(""), &output)
	if err != nil {
		t.Fatal(err)
	}
	value, err := LoadConfigFile(configPath)
	if err != nil || value.DeploymentMode != DeploymentLANManaged || value.TLS.CertFile != "" || value.TLS.KeyFile != "" {
		t.Fatalf("managed config=%+v err=%v", value, err)
	}
	for _, name := range []string{"ca.pem", "ca-key.pem", "server.pem", "server-key.pem"} {
		if _, err := os.Stat(filepath.Join(dataDir, "pki", "managed", name)); err != nil {
			t.Fatalf("managed PKI %s unavailable: %v", name, err)
		}
	}
	if !strings.Contains(output.String(), "Mode: lan-managed") || !strings.Contains(output.String(), "managed-ca") {
		t.Fatalf("init output=%q", output.String())
	}
}

func TestServerInitManagedLANKeepsCAWhenIPAddressChanges(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "server.toml")
	dataDir := filepath.Join(root, "data")
	first := []string{"--config", configPath, "--mode", "lan-managed", "--data-dir", dataDir, "--listen", "0.0.0.0:7444", "--public-url", "https://192.168.20.10:7444", "--non-interactive"}
	if err := initializeServer(context.Background(), first, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	provider, err := newManagedCertificateProvider(context.Background(), Options{DataDir: dataDir, DeploymentMode: DeploymentLANManaged, PublicURL: "https://192.168.20.10:7444"})
	if err != nil {
		t.Fatal(err)
	}
	before := provider.Status()
	_ = provider.Close()
	second := []string{"--config", configPath, "--mode", "lan-managed", "--data-dir", dataDir, "--listen", "0.0.0.0:7444", "--public-url", "https://192.168.20.11:7444", "--non-interactive", "--replace"}
	if err := initializeServer(context.Background(), second, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	updated, err := newManagedCertificateProvider(context.Background(), Options{DataDir: dataDir, DeploymentMode: DeploymentLANManaged, PublicURL: "https://192.168.20.11:7444"})
	if err != nil {
		t.Fatal(err)
	}
	defer updated.Close()
	after := updated.Status()
	if before.CAFingerprint != after.CAFingerprint || before.Fingerprint == after.Fingerprint || len(after.SAN) != 1 || after.SAN[0] != "192.168.20.11" {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
}

func TestServerInitRejectsReplacementWhileServerLockIsHeld(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "server.toml")
	dataDir := filepath.Join(root, "data")
	arguments := []string{"--config", configPath, "--mode", "local", "--data-dir", dataDir, "--listen", "127.0.0.1:7444", "--non-interactive"}
	if err := initializeServer(context.Background(), arguments, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireDataLock(filepath.Join(dataDir, "server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := initializeServer(context.Background(), append(arguments, "--replace"), strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("running Server configuration replacement was accepted")
	}
}

func TestServerBackupAndRestoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "server.toml")
	configStore, _ := NewConfigFileStore(configPath)
	if err := configStore.Save(context.Background(), ConfigFile{ConfigVersion: CurrentConfigVersion, DataDir: dataDir, Listen: "127.0.0.1:7444"}); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(dataDir, "server.db")
	local, err := serverstore.Open(context.Background(), databasePath, serverstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(root, "backup.tar.gz")
	if err := backupServer(context.Background(), []string{"--config", configPath, "--output", backupPath}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := backupServer(context.Background(), []string{"--config", configPath, "--output", backupPath}, &bytes.Buffer{}); err == nil {
		t.Fatal("backup output was overwritten")
	}
	mutationDB, err := sql.Open("sqlite3", filepath.ToSlash(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutationDB.Exec(`INSERT INTO owners(id,singleton,status,created_at) VALUES('after-backup',1,'active',?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := mutationDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	if err := restoreServer(context.Background(), []string{"--config", configPath, "--from", backupPath}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.ToSlash(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM owners").Scan(&count); err != nil || count != 0 {
		t.Fatalf("restored owner count=%d err=%v", count, err)
	}
	matches, _ := filepath.Glob(databasePath + ".pre-restore-*")
	if len(matches) != 1 {
		t.Fatalf("pre-restore copies=%v", matches)
	}
	info, err := os.Stat(backupPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestServerDoctorReportsLatestDefaultBackupIntegrity(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "server.toml")
	configStore, _ := NewConfigFileStore(configPath)
	if err := configStore.Save(context.Background(), ConfigFile{ConfigVersion: CurrentConfigVersion, DataDir: dataDir, Listen: "127.0.0.1:7444"}); err != nil {
		t.Fatal(err)
	}
	local, err := serverstore.Open(context.Background(), filepath.Join(dataDir, "server.db"), serverstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	if err := backupServer(context.Background(), []string{"--config", configPath}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	readDoctor := func() doctorStatus {
		var output bytes.Buffer
		if err := doctor(context.Background(), []string{"--config", configPath, "--json"}, &output); err != nil {
			t.Fatal(err)
		}
		var status doctorStatus
		if err := json.Unmarshal(output.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	status := readDoctor()
	if status.Backup != "ready" || status.BackupLastAt == "" || status.BackupSizeBytes < 1 {
		t.Fatalf("doctor backup=%#v", status)
	}
	archives, err := listBackupArchives(filepath.Join(dataDir, "backups"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("archives=%v err=%v", archives, err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "backups", archives[0].Name()), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status = readDoctor(); status.Backup != "backup_invalid" {
		t.Fatalf("corrupt backup status=%#v", status)
	}
}

func TestRestoreRejectsRunningServerAndInvalidArchive(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "server.toml")
	store, _ := NewConfigFileStore(configPath)
	if err := store.Save(context.Background(), ConfigFile{ConfigVersion: CurrentConfigVersion, DataDir: dataDir, Listen: "127.0.0.1:7444"}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "invalid.tar.gz")
	if err := os.WriteFile(archive, []byte("not an archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireDataLock(filepath.Join(dataDir, "server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := restoreServer(context.Background(), []string{"--config", configPath, "--from", archive}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("running restore error=%v", err)
	}
	_ = lock.Close()
	if err := restoreServer(context.Background(), []string{"--config", configPath, "--from", archive}, &bytes.Buffer{}); err == nil {
		t.Fatal("invalid archive was accepted")
	}
}

func TestCertificateExpiryWarningThresholds(t *testing.T) {
	now := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		duration time.Duration
		want     string
	}{
		{-time.Second, "certificate_expired"}, {6 * 24 * time.Hour, "certificate_expiring_7d"},
		{10 * 24 * time.Hour, "certificate_expiring_14d"}, {20 * 24 * time.Hour, "certificate_expiring_30d"},
		{31 * 24 * time.Hour, ""},
	}
	for _, item := range cases {
		if got := certificateExpiryWarning(now, now.Add(item.duration)); got != item.want {
			t.Fatalf("duration=%v got=%q want=%q", item.duration, got, item.want)
		}
	}
}
