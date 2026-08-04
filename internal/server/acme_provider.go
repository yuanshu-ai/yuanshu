package server

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/tlsalpn01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

const acmeProfile = "shortlived"

type acmeUser struct {
	email        string
	privateKey   crypto.PrivateKey
	registration *registration.Resource
}

func (u *acmeUser) GetEmail() string                        { return u.email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.registration }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.privateKey }

type acmeCertificateProvider struct {
	mu               sync.RWMutex
	renewMu          sync.Mutex
	host             string
	directory        string
	certPath         string
	keyPath          string
	accountPath      string
	registrationPath string
	environment      string
	email            string
	directoryURL     string
	httpClient       *http.Client
	clock            func() time.Time
	certificate      *tls.Certificate
	challenge        *tls.Certificate
	status           CertificateStatus
	cancel           context.CancelFunc
}

func newACMECertificateProvider(ctx context.Context, options Options) (certificateProvider, error) {
	host := publicURLHost(options.PublicURL)
	if !validPublicACMEHost(host) || !options.ACME.AcceptTerms {
		return nil, errors.New("public IP ACME configuration is invalid")
	}
	directory := filepath.Join(certificateDataDir(options), "pki", "acme")
	if err := preparePKIDirectory(directory); err != nil {
		return nil, err
	}
	provider := &acmeCertificateProvider{
		host: host, directory: directory, certPath: filepath.Join(directory, "server-bundle.pem"), keyPath: filepath.Join(directory, "server-bundle.pem"),
		accountPath: filepath.Join(directory, "account-key.pem"), registrationPath: filepath.Join(directory, "registration.json"),
		environment: options.ACME.Environment, email: options.ACME.Email, directoryURL: options.ACMEDirectoryURL,
		httpClient: options.ACMEHTTPClient, clock: time.Now,
		status: CertificateStatus{Provider: "acme-ip", State: "issuing"},
	}
	if options.Clock != nil {
		provider.clock = options.Clock
	}
	if provider.environment == "" {
		provider.environment = "production"
	}
	if provider.directoryURL == "" {
		if provider.environment == "staging" {
			provider.directoryURL = lego.LEDirectoryStaging
		} else {
			provider.directoryURL = lego.LEDirectoryProduction
		}
	}
	if pair, leaf, err := loadValidatedKeyPair(provider.certPath, provider.keyPath, host, provider.clock()); err == nil {
		provider.install(pair, leaf)
	}
	workerContext, cancel := context.WithCancel(ctx)
	provider.cancel = cancel
	go provider.renewalLoop(workerContext)
	return provider, nil
}

func validPublicACMEHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && isGlobalRoutableIP(ip)
}

func (p *acmeCertificateProvider) TLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{tlsalpn01.ACMETLS1Protocol, "h2", "http/1.1"}, GetCertificate: p.getCertificate}
}

func (p *acmeCertificateProvider) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if containsString(hello.SupportedProtos, tlsalpn01.ACMETLS1Protocol) && p.challenge != nil {
		return p.challenge, nil
	}
	if p.certificate == nil || p.certificate.Leaf == nil || !p.clock().Before(p.certificate.Leaf.NotAfter) {
		return nil, errors.New("server TLS certificate is unavailable")
	}
	return p.certificate, nil
}

func (p *acmeCertificateProvider) Status() CertificateStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := p.status
	result.SAN = append([]string(nil), p.status.SAN...)
	return result
}

func (p *acmeCertificateProvider) PublicCACertificate() ([]byte, bool) { return nil, false }

func (p *acmeCertificateProvider) Close() error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}

func (p *acmeCertificateProvider) Renew(context.Context) error {
	p.renewMu.Lock()
	defer p.renewMu.Unlock()
	client, user, err := p.client()
	if err != nil {
		p.setError("acme_client_unavailable")
		return err
	}
	if user.registration == nil {
		resource, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			p.setError("acme_registration_failed")
			return err
		}
		user.registration = resource
		if err := p.storeRegistration(resource); err != nil {
			p.setError("acme_registration_store_failed")
			return err
		}
	}
	if err := client.Challenge.SetTLSALPN01Provider(p); err != nil {
		p.setError("acme_challenge_unavailable")
		return err
	}
	request := acmeObtainRequest(p.host)
	resource, err := client.Certificate.Obtain(request)
	if err != nil {
		p.setError("acme_issuance_failed")
		return err
	}
	if len(resource.Certificate) == 0 || len(resource.PrivateKey) == 0 {
		p.setError("acme_certificate_invalid")
		return errors.New("ACME returned incomplete certificate material")
	}
	stagedBundle := filepath.Join(p.directory, ".server-bundle.pem.new")
	bundle := make([]byte, 0, len(resource.Certificate)+len(resource.PrivateKey))
	bundle = append(bundle, resource.Certificate...)
	bundle = append(bundle, resource.PrivateKey...)
	if atomicWriteServerFile(stagedBundle, bundle) != nil {
		p.setError("acme_certificate_store_failed")
		return errors.New("ACME certificate could not be staged")
	}
	pair, leaf, err := loadValidatedKeyPair(stagedBundle, stagedBundle, p.host, p.clock())
	if err != nil {
		_ = os.Remove(stagedBundle)
		p.setError("acme_certificate_invalid")
		return err
	}
	if err := os.Rename(stagedBundle, p.certPath); err != nil || syncServerDirectory(p.directory) != nil {
		_ = os.Remove(stagedBundle)
		p.setError("acme_certificate_store_failed")
		return errors.New("ACME certificate could not be installed")
	}
	p.install(pair, leaf)
	p.updateARIRenewal(client, leaf)
	return nil
}

func acmeObtainRequest(host string) certificate.ObtainRequest {
	return certificate.ObtainRequest{Domains: []string{host}, Bundle: true, Profile: acmeProfile, AlwaysDeactivateAuthorizations: true}
}

func (p *acmeCertificateProvider) updateARIRenewal(client *lego.Client, leaf *x509.Certificate) {
	information, err := client.Certificate.GetRenewalInfo(certificate.RenewalInfoRequest{Cert: leaf})
	if err != nil {
		return
	}
	when := information.ShouldRenewAt(p.clock().UTC(), 12*time.Hour)
	if when != nil && when.After(p.clock()) && when.Before(leaf.NotAfter) {
		p.setNextRenewal(when.UTC())
	}
}

func (p *acmeCertificateProvider) Present(domain, _, keyAuth string) error {
	if domain != p.host {
		return errors.New("ACME challenge target is invalid")
	}
	challenge, err := tlsalpn01.ChallengeCert(domain, keyAuth)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.challenge = challenge
	p.status.State = "challenging"
	p.mu.Unlock()
	return nil
}

func (p *acmeCertificateProvider) CleanUp(domain, _, _ string) error {
	if domain != p.host {
		return errors.New("ACME challenge target is invalid")
	}
	p.mu.Lock()
	p.challenge = nil
	if p.certificate != nil {
		p.status.State = "ready"
	} else {
		p.status.State = "issuing"
	}
	p.mu.Unlock()
	return nil
}

func (p *acmeCertificateProvider) renewalLoop(ctx context.Context) {
	backoff := time.Second
	for {
		wait := p.renewalWait()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := p.Renew(ctx); err != nil {
			if backoff < time.Hour {
				backoff *= 2
				if backoff > time.Hour {
					backoff = time.Hour
				}
			}
			p.setNextRenewal(p.clock().Add(backoff))
			continue
		}
		backoff = time.Second
	}
}

func (p *acmeCertificateProvider) renewalWait() time.Duration {
	p.mu.RLock()
	certificateValue := p.certificate
	next := p.status.NextRenewal
	p.mu.RUnlock()
	if certificateValue == nil {
		return time.Second
	}
	if next.IsZero() {
		next = certificateValue.Leaf.NotBefore.Add(certificateValue.Leaf.NotAfter.Sub(certificateValue.Leaf.NotBefore) / 2)
	}
	duration := next.Sub(p.clock())
	if duration < 0 {
		return 0
	}
	if duration > 12*time.Hour {
		return 12 * time.Hour
	}
	return duration
}

func (p *acmeCertificateProvider) client() (*lego.Client, *acmeUser, error) {
	privateKey, err := p.loadOrCreateAccountKey()
	if err != nil {
		return nil, nil, err
	}
	user := &acmeUser{email: p.email, privateKey: privateKey}
	if raw, err := os.ReadFile(p.registrationPath); err == nil {
		if validateManagedPKIFile(p.registrationPath) != nil {
			return nil, nil, errors.New("ACME registration is unsafe")
		}
		var resource registration.Resource
		if json.Unmarshal(raw, &resource) != nil || resource.URI == "" {
			return nil, nil, errors.New("ACME registration is invalid")
		}
		user.registration = &resource
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, errors.New("ACME registration is unavailable")
	}
	configuration := lego.NewConfig(user)
	configuration.CADirURL = p.directoryURL
	configuration.Certificate.KeyType = certcrypto.EC256
	configuration.Certificate.Timeout = 2 * time.Minute
	if p.httpClient != nil {
		configuration.HTTPClient = p.httpClient
	}
	client, err := lego.NewClient(configuration)
	if err != nil {
		return nil, nil, err
	}
	return client, user, nil
}

func (p *acmeCertificateProvider) loadOrCreateAccountKey() (*ecdsa.PrivateKey, error) {
	if raw, err := os.ReadFile(p.accountPath); err == nil {
		if validateManagedPKIFile(p.accountPath) != nil {
			return nil, errors.New("ACME account key is unsafe")
		}
		block, rest := pem.Decode(raw)
		if block == nil || len(rest) != 0 {
			return nil, errors.New("ACME account key is invalid")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		privateKey, ok := key.(*ecdsa.PrivateKey)
		if err != nil || !ok {
			return nil, errors.New("ACME account key is invalid")
		}
		return privateKey, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("ACME account key is unavailable")
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil || atomicWriteServerFile(p.accountPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})) != nil {
		return nil, errors.New("ACME account key could not be stored")
	}
	return privateKey, nil
}

func (p *acmeCertificateProvider) storeRegistration(resource *registration.Resource) error {
	encoded, err := json.Marshal(resource)
	if err != nil {
		return err
	}
	return atomicWriteServerFile(p.registrationPath, encoded)
}

func (p *acmeCertificateProvider) install(pair tls.Certificate, leaf *x509.Certificate) {
	pair.Leaf = leaf
	next := leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) / 2).UTC()
	status := CertificateStatus{Provider: "acme-ip", State: "ready", Fingerprint: certificateFingerprint(leaf.Raw), NotAfter: leaf.NotAfter.UTC(), LastRenewed: p.clock().UTC(), NextRenewal: next}
	for _, ip := range leaf.IPAddresses {
		status.SAN = append(status.SAN, ip.String())
	}
	p.mu.Lock()
	p.certificate, p.status = &pair, status
	p.mu.Unlock()
}

func (p *acmeCertificateProvider) setError(code string) {
	p.mu.Lock()
	p.status.Provider, p.status.State, p.status.LastErrorCode = "acme-ip", "needs_attention", code
	p.mu.Unlock()
}

func (p *acmeCertificateProvider) setNextRenewal(value time.Time) {
	p.mu.Lock()
	p.status.NextRenewal = value.UTC()
	p.mu.Unlock()
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
