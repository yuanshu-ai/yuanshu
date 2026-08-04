package server

import (
	"bytes"
	"context"
	"database/sql"
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
		{-time.Second, "expired"}, {6 * 24 * time.Hour, "expires_within_7_days"},
		{10 * 24 * time.Hour, "expires_within_14_days"}, {20 * 24 * time.Hour, "expires_within_30_days"},
		{31 * 24 * time.Hour, ""},
	}
	for _, item := range cases {
		if got := certificateExpiryWarning(now, now.Add(item.duration)); got != item.want {
			t.Fatalf("duration=%v got=%q want=%q", item.duration, got, item.want)
		}
	}
}
