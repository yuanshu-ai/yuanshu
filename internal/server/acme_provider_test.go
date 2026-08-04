package server

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/go-acme/lego/v4/challenge/tlsalpn01"
)

func TestACMEProviderServesChallengeOnlyForACMETLSALPN(t *testing.T) {
	root := t.TempDir()
	certPath, keyPath, _ := writeServerTestCertificate(t, root, []string{"8.8.8.8"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	pair, leaf, err := loadValidatedKeyPair(certPath, keyPath, "8.8.8.8", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pair.Leaf = leaf
	provider := &acmeCertificateProvider{host: "8.8.8.8", clock: time.Now, certificate: &pair, status: CertificateStatus{Provider: "acme-ip", State: "ready", NotAfter: leaf.NotAfter}}
	if err := provider.Present("8.8.8.8", "token", "key-authorization"); err != nil {
		t.Fatal(err)
	}
	challenge, err := provider.getCertificate(&tls.ClientHelloInfo{SupportedProtos: []string{tlsalpn01.ACMETLS1Protocol}})
	if err != nil || challenge == nil || challenge == &pair {
		t.Fatalf("challenge certificate unavailable: %v", err)
	}
	normal, err := provider.getCertificate(&tls.ClientHelloInfo{SupportedProtos: []string{"h2"}})
	if err != nil || normal != &pair {
		t.Fatalf("normal certificate was replaced: %v", err)
	}
	if err := provider.CleanUp("8.8.8.8", "token", "key-authorization"); err != nil {
		t.Fatal(err)
	}
	if provider.challenge != nil {
		t.Fatal("ACME challenge certificate was retained")
	}
}

func TestPublicACMEHostClassification(t *testing.T) {
	for _, value := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !validPublicACMEHost(value) {
			t.Fatalf("public IP %s rejected", value)
		}
	}
	for _, value := range []string{"127.0.0.1", "192.168.1.20", "fd00::20", "0.0.0.0", "192.0.2.1", "198.51.100.2", "203.0.113.20", "2001:db8::1", "example.test"} {
		if validPublicACMEHost(value) {
			t.Fatalf("non-public IP %s accepted", value)
		}
	}
}

func TestACMERequestUsesIPIdentifierAndShortLivedProfile(t *testing.T) {
	request := acmeObtainRequest("8.8.8.8")
	if len(request.Domains) != 1 || request.Domains[0] != "8.8.8.8" || request.Profile != "shortlived" || !request.AlwaysDeactivateAuthorizations {
		t.Fatalf("request=%+v", request)
	}
}
