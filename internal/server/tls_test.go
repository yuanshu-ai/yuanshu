package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublicServerOptionsAndTLSMaterial(t *testing.T) {
	root := t.TempDir()
	certPath, keyPath, _ := writeServerTestCertificate(t, root, []string{"localhost"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	options := Options{
		DataDir: root, Listen: "0.0.0.0:7444", PublicURL: "https://localhost:7444",
		TLSCertFile: certPath, TLSKeyFile: keyPath,
	}
	configuration, err := loadTLSConfig(options)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %x", configuration.MinVersion)
	}

	wrongCert, wrongKey, _ := writeServerTestCertificate(t, filepath.Join(root, "wrong"), []string{"other.example"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	options.TLSCertFile, options.TLSKeyFile = wrongCert, wrongKey
	if _, err := loadTLSConfig(options); err == nil {
		t.Fatal("certificate with wrong SAN was accepted")
	}
	expiredCert, expiredKey, _ := writeServerTestCertificate(t, filepath.Join(root, "expired"), []string{"localhost"}, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	options.TLSCertFile, options.TLSKeyFile = expiredCert, expiredKey
	if _, err := loadTLSConfig(options); err == nil {
		t.Fatal("expired certificate was accepted")
	}
}

func TestPublicServerServesTLS13(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	root := t.TempDir()
	certPath, keyPath, roots := writeServerTestCertificate(t, root, []string{"localhost"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	var output bytes.Buffer
	go func() {
		done <- Run(ctx, Options{
			DataDir: filepath.Join(root, "data"), Listen: listener.Addr().String(), Listener: listener,
			PublicURL: "https://localhost:" + big.NewInt(int64(port)).String(), TLSCertFile: certPath, TLSKeyFile: keyPath,
			Stdout: &output,
		})
	}()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS13}}}
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, requestErr := client.Get("https://localhost:" + big.NewInt(int64(port)).String() + "/healthz")
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK || response.TLS == nil || response.TLS.Version != tls.VersionTLS13 {
				t.Fatalf("status=%d TLS=%v", response.StatusCode, response.TLS)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("TLS server did not start: %v", requestErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	page, err := client.Get("https://localhost:" + big.NewInt(int64(port)).String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer page.Body.Close()
	if page.StatusCode != http.StatusOK || !strings.Contains(page.Header.Get("Content-Type"), "text/html") || !strings.Contains(output.String(), "Yuanshu Web: https://localhost:") {
		t.Fatalf("page status=%d headers=%v output=%q", page.StatusCode, page.Header, output.String())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() = %v", err)
	}
}

func writeServerTestCertificate(t *testing.T, directory string, names []string, notBefore, notAfter time.Time) (string, string, *x509.CertPool) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Yuanshu synthetic TLS"},
		NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, name := range names {
		if ip := net.ParseIP(name); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
			continue
		}
		template.DNSNames = append(template.DNSNames, name)
	}
	der, err := x509.CreateCertificate(cryptorand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(directory, "server.crt"), filepath.Join(directory, "server.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(templateFromDER(t, der))
	return certPath, keyPath, pool
}

func templateFromDER(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}
