package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceRejectsMissingAndRoot(t *testing.T) {
	if _, err := workspace(""); err == nil {
		t.Fatal("missing workspace accepted")
	}
	root := filepath.VolumeName(t.TempDir()) + string(os.PathSeparator)
	if _, err := workspace(root); err == nil {
		t.Fatal("filesystem root accepted")
	}
	dir := t.TempDir()
	got, err := workspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(dir) {
		t.Fatalf("workspace=%q", got)
	}
}

func TestStandaloneDoesNotRequireRemoteServerURL(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(cert, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YUANSHU_POC_TLS_CERT", cert)
	t.Setenv("YUANSHU_POC_TLS_KEY", key)
	t.Setenv("YUANSHU_POC_NODE_TOKEN", "0123456789abcdef0123456789abcdef")
	t.Setenv("YUANSHU_POC_WORKSPACE", dir)
	t.Setenv("YUANSHU_POC_SERVER_URL", "")
	if _, _, err := StandaloneFromEnv(); err != nil {
		t.Fatalf("StandaloneFromEnv() = %v", err)
	}
}
