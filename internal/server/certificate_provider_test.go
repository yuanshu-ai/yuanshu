package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type readinessFunc func(context.Context) error

func (f readinessFunc) QuickCheck(ctx context.Context) error { return f(ctx) }

type staticCertificateProvider struct{ status CertificateStatus }

func (p staticCertificateProvider) TLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13}
}
func (p staticCertificateProvider) Status() CertificateStatus           { return p.status }
func (p staticCertificateProvider) PublicCACertificate() ([]byte, bool) { return nil, false }
func (p staticCertificateProvider) Renew(context.Context) error         { return nil }
func (p staticCertificateProvider) Close() error                        { return nil }

func TestManagedCertificateProviderCreatesPrivateCAAndRenewsLeaf(t *testing.T) {
	root := t.TempDir()
	options := Options{DataDir: root, Listen: "0.0.0.0:9527", DeploymentMode: DeploymentLANManaged, PublicURL: "https://192.168.20.30:9527"}
	provider, err := newManagedCertificateProvider(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	status := provider.Status()
	if status.Provider != "managed-ca" || status.State != "ready" || len(status.SAN) != 1 || status.SAN[0] != "192.168.20.30" || status.CAFingerprint == "" {
		t.Fatalf("status=%+v", status)
	}
	ca, ok := provider.PublicCACertificate()
	if !ok || len(ca) == 0 {
		t.Fatal("public root CA unavailable")
	}
	block, rest := pem.Decode(ca)
	if block == nil || len(rest) != 0 {
		t.Fatal("CA PEM invalid")
	}
	certificate, parseErr := x509.ParseCertificate(block.Bytes)
	if parseErr != nil || !certificate.IsCA {
		t.Fatalf("CA invalid: %v", parseErr)
	}
	beforeCA, beforeLeaf := status.CAFingerprint, status.Fingerprint
	if err := provider.Renew(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := provider.Status()
	if after.CAFingerprint != beforeCA || after.Fingerprint == beforeLeaf {
		t.Fatalf("renewed status=%+v", after)
	}
	for _, name := range []string{"ca.pem", "ca-key.pem", "server.pem", "server-key.pem"} {
		info, err := os.Lstat(filepath.Join(root, "pki", "managed", name))
		if err != nil {
			t.Fatalf("%s unavailable: %v", name, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%v", name, info.Mode().Perm())
		}
	}
	configuration := provider.TLSConfig()
	pair, err := configuration.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil || pair.Leaf == nil || pair.Leaf.VerifyHostname("192.168.20.30") != nil {
		t.Fatalf("managed leaf unavailable: %v", err)
	}
}

func TestManagedCAEncryptedBackupAndRestore(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	configPath := filepath.Join(root, "server.toml")
	value := ConfigFile{ConfigVersion: 2, DeploymentMode: DeploymentLANManaged, DataDir: dataDir, Listen: "0.0.0.0:9527", PublicURL: "https://192.168.20.40:9527"}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, _ := NewConfigFileStore(configPath)
	if err := store.Save(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	provider, err := newManagedCertificateProvider(context.Background(), optionsFromConfig(value))
	if err != nil {
		t.Fatal(err)
	}
	original := provider.Status().CAFingerprint
	_ = provider.Close()
	backup := filepath.Join(root, "managed-ca.age")
	if err := backupManagedCA(configPath, backup, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(backup)
	if err != nil || strings.Contains(string(raw), "PRIVATE KEY") || strings.Contains(string(raw), "CERTIFICATE") {
		t.Fatal("encrypted CA backup exposed plaintext material")
	}
	certPath, keyPath := managedCAPaths(dataDir)
	if err := os.Remove(certPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if err := restoreManagedCA(configPath, backup, "wrong passphrase"); err == nil {
		t.Fatal("wrong recovery passphrase was accepted")
	}
	if err := restoreManagedCA(configPath, backup, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	restored, err := newManagedCertificateProvider(context.Background(), optionsFromConfig(value))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.Status().CAFingerprint != original {
		t.Fatalf("restored CA=%s want=%s", restored.Status().CAFingerprint, original)
	}
	if restored.Status().CABackupAt.IsZero() {
		t.Fatal("managed CA recovery backup metadata was not restored")
	}
}

func TestExternalCertificateProviderKeepsLastValidCertificateOnBadReplacement(t *testing.T) {
	root := t.TempDir()
	certPath, keyPath, _ := writeServerTestCertificate(t, root, []string{"example.test"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	providerValue, err := newExternalCertificateProvider(context.Background(), Options{DeploymentMode: DeploymentExternal, PublicURL: "https://example.test", TLSCertFile: certPath, TLSKeyFile: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	provider := providerValue.(*dynamicCertificateProvider)
	defer provider.Close()
	before := provider.Status().Fingerprint
	if err := os.WriteFile(certPath, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := provider.reloadExternal(); err == nil {
		t.Fatal("bad certificate replacement accepted")
	}
	pair, getErr := provider.TLSConfig().GetCertificate(&tls.ClientHelloInfo{})
	if getErr != nil || pair == nil || provider.Status().Fingerprint != before {
		t.Fatalf("last valid certificate lost: status=%+v err=%v", provider.Status(), getErr)
	}
}

func TestServerReadinessFailsClosedWithoutUsableCertificate(t *testing.T) {
	now := time.Now().UTC()
	database := readinessFunc(func(context.Context) error { return nil })
	for _, status := range []CertificateStatus{{Provider: "acme-ip", State: "issuing"}, {Provider: "external", State: "ready", NotAfter: now.Add(-time.Minute)}} {
		ready := serverReadiness{database: database, certificate: staticCertificateProvider{status: status}, clock: func() time.Time { return now }}
		if ready.QuickCheck(context.Background()) == nil {
			t.Fatalf("unusable certificate reported ready: %+v", status)
		}
	}
	ready := serverReadiness{database: database, certificate: staticCertificateProvider{status: CertificateStatus{Provider: "managed-ca", State: "ready", NotAfter: now.Add(time.Hour)}}, clock: func() time.Time { return now }}
	if err := ready.QuickCheck(context.Background()); err != nil {
		t.Fatalf("valid certificate rejected: %v", err)
	}
}
