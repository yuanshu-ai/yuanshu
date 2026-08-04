package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	managedRootLifetime = 10 * 365 * 24 * time.Hour
	managedLeafLifetime = 90 * 24 * time.Hour
	managedRenewBefore  = 30 * 24 * time.Hour
)

type CertificateStatus struct {
	Provider      string    `json:"provider"`
	State         string    `json:"state"`
	SAN           []string  `json:"san,omitempty"`
	Fingerprint   string    `json:"fingerprint,omitempty"`
	NotAfter      time.Time `json:"notAfter,omitzero"`
	LastRenewed   time.Time `json:"lastRenewed,omitzero"`
	NextRenewal   time.Time `json:"nextRenewal,omitzero"`
	LastErrorCode string    `json:"lastErrorCode,omitempty"`
	CAFingerprint string    `json:"caFingerprint,omitempty"`
	CABackupAt    time.Time `json:"caBackupAt,omitzero"`
}

type certificateProvider interface {
	TLSConfig() *tls.Config
	Status() CertificateStatus
	PublicCACertificate() ([]byte, bool)
	Renew(context.Context) error
	Close() error
}

type dynamicCertificateProvider struct {
	mu          sync.RWMutex
	provider    string
	host        string
	certPath    string
	keyPath     string
	caCertPath  string
	caKeyPath   string
	certificate *tls.Certificate
	status      CertificateStatus
	clock       func() time.Time
	cancel      context.CancelFunc
}

func newCertificateProvider(ctx context.Context, options Options) (certificateProvider, error) {
	mode := effectiveDeploymentMode(options)
	switch mode {
	case DeploymentLocal:
		return nil, nil
	case DeploymentExternal:
		if effectiveTLSTermination(options) == "proxy" {
			return nil, nil
		}
		return newExternalCertificateProvider(ctx, options)
	case DeploymentLANManaged:
		return newManagedCertificateProvider(ctx, options)
	case DeploymentPublicIPACME:
		return newACMECertificateProvider(ctx, options)
	default:
		return nil, errors.New("server deployment mode is invalid")
	}
}

func effectiveDeploymentMode(options Options) DeploymentMode {
	if options.DeploymentMode != "" {
		return options.DeploymentMode
	}
	if options.PublicURL == "" {
		return DeploymentLocal
	}
	return DeploymentExternal
}

func effectiveTLSTermination(options Options) string {
	if options.TLSTermination != "" {
		return options.TLSTermination
	}
	return "server"
}

func newExternalCertificateProvider(ctx context.Context, options Options) (certificateProvider, error) {
	provider := &dynamicCertificateProvider{
		provider: "external", host: publicURLHost(options.PublicURL), certPath: options.TLSCertFile,
		keyPath: options.TLSKeyFile, clock: time.Now,
	}
	if options.Clock != nil {
		provider.clock = options.Clock
	}
	if err := provider.reloadExternal(); err != nil {
		return nil, err
	}
	watchCtx, cancel := context.WithCancel(ctx)
	provider.cancel = cancel
	go provider.watchExternal(watchCtx)
	return provider, nil
}

func newManagedCertificateProvider(ctx context.Context, options Options) (certificateProvider, error) {
	dataDir := certificateDataDir(options)
	if dataDir == "" || !filepath.IsAbs(dataDir) {
		return nil, errors.New("managed certificate data directory is invalid")
	}
	host := publicURLHost(options.PublicURL)
	if net.ParseIP(host) == nil || !isPrivateAddress(net.ParseIP(host)) {
		return nil, errors.New("managed certificate host is invalid")
	}
	directory := filepath.Join(dataDir, "pki", "managed")
	if err := preparePKIDirectory(directory); err != nil {
		return nil, err
	}
	provider := &dynamicCertificateProvider{
		provider: "managed-ca", host: host, certPath: filepath.Join(directory, "server.pem"),
		keyPath: filepath.Join(directory, "server-key.pem"), caCertPath: filepath.Join(directory, "ca.pem"),
		caKeyPath: filepath.Join(directory, "ca-key.pem"), clock: time.Now,
	}
	if options.Clock != nil {
		provider.clock = options.Clock
	}
	if err := provider.ensureManaged(); err != nil {
		return nil, err
	}
	watchCtx, cancel := context.WithCancel(ctx)
	provider.cancel = cancel
	go provider.watchManaged(watchCtx)
	return provider, nil
}

func preparePKIDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return errors.New("server PKI directory is unavailable")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || os.Chmod(path, 0o700) != nil {
		return errors.New("server PKI directory is unsafe")
	}
	return nil
}

func (p *dynamicCertificateProvider) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS13,
		NextProtos:     []string{"h2", "http/1.1"},
		GetCertificate: p.getCertificate,
	}
}

func (p *dynamicCertificateProvider) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.certificate == nil || p.certificate.Leaf == nil || !p.clock().Before(p.certificate.Leaf.NotAfter) {
		return nil, errors.New("server TLS certificate is unavailable")
	}
	return p.certificate, nil
}

func (p *dynamicCertificateProvider) Status() CertificateStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := p.status
	result.SAN = append([]string(nil), p.status.SAN...)
	if p.provider == "managed-ca" {
		result.CABackupAt = readManagedCABackupTime(filepath.Dir(p.caCertPath), result.CAFingerprint)
	}
	return result
}

func (p *dynamicCertificateProvider) Close() error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}

func (p *dynamicCertificateProvider) PublicCACertificate() ([]byte, bool) {
	if p.provider != "managed-ca" || p.caCertPath == "" {
		return nil, false
	}
	if err := validateManagedPKIFile(p.caCertPath); err != nil {
		return nil, false
	}
	raw, err := os.ReadFile(p.caCertPath)
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		return nil, false
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !certificate.IsCA {
		return nil, false
	}
	return append([]byte(nil), raw...), true
}

func (p *dynamicCertificateProvider) Renew(ctx context.Context) error {
	if p.provider == "managed-ca" {
		return p.renewManaged(ctx, true)
	}
	return p.reloadExternal()
}

func (p *dynamicCertificateProvider) reloadExternal() error {
	pair, leaf, err := loadValidatedKeyPair(p.certPath, p.keyPath, p.host, p.clock())
	if err != nil {
		p.setError("certificate_unavailable_or_mismatched")
		return err
	}
	p.install(pair, leaf, "")
	return nil
}

func (p *dynamicCertificateProvider) watchExternal(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = p.reloadExternal()
		}
	}
}

func (p *dynamicCertificateProvider) ensureManaged() error {
	if _, err := os.Lstat(p.caCertPath); errors.Is(err, os.ErrNotExist) {
		if err := p.createManagedRoot(); err != nil {
			return err
		}
	} else if err != nil {
		return errors.New("managed CA is unavailable")
	}
	return p.renewManaged(context.Background(), false)
}

func (p *dynamicCertificateProvider) createManagedRoot() error {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return errors.New("managed CA generation failed")
	}
	serial, err := randomSerial()
	if err != nil {
		return errors.New("managed CA generation failed")
	}
	now := p.clock().UTC()
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "Yuanshu Local CA"},
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(managedRootLifetime),
		IsCA: true, BasicConstraintsValid: true, MaxPathLen: 0,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if err != nil {
		return errors.New("managed CA generation failed")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return errors.New("managed CA generation failed")
	}
	if err := atomicWriteServerFile(p.caKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		return errors.New("managed CA key could not be stored")
	}
	if err := atomicWriteServerFile(p.caCertPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return errors.New("managed CA certificate could not be stored")
	}
	return nil
}

func (p *dynamicCertificateProvider) renewManaged(_ context.Context, force bool) error {
	if !force {
		if pair, leaf, err := loadValidatedKeyPair(p.certPath, p.keyPath, p.host, p.clock()); err == nil && leaf.NotAfter.Sub(p.clock()) > managedRenewBefore {
			caFingerprint, _ := managedCAFingerprint(p.caCertPath)
			p.install(pair, leaf, caFingerprint)
			return nil
		}
	}
	caCert, caKey, err := loadManagedCA(p.caCertPath, p.caKeyPath, p.clock())
	if err != nil {
		p.setError("managed_ca_invalid")
		return err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return errors.New("managed certificate generation failed")
	}
	serial, err := randomSerial()
	if err != nil {
		return errors.New("managed certificate generation failed")
	}
	now := p.clock().UTC()
	leafTemplate := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "Yuanshu Server"},
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(managedLeafLifetime),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP(p.host)}, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return errors.New("managed certificate generation failed")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return errors.New("managed certificate generation failed")
	}
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})...)
	if err := atomicWriteServerFile(p.keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		return errors.New("managed certificate key could not be stored")
	}
	if err := atomicWriteServerFile(p.certPath, chain); err != nil {
		return errors.New("managed certificate could not be stored")
	}
	pair, leaf, err := loadValidatedKeyPair(p.certPath, p.keyPath, p.host, p.clock())
	if err != nil {
		return err
	}
	p.install(pair, leaf, certificateFingerprint(caCert.Raw))
	return nil
}

func (p *dynamicCertificateProvider) watchManaged(ctx context.Context) {
	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = p.renewManaged(ctx, false)
		}
	}
}

func (p *dynamicCertificateProvider) install(pair tls.Certificate, leaf *x509.Certificate, caFingerprint string) {
	pair.Leaf = leaf
	status := CertificateStatus{
		Provider: p.provider, State: "ready", Fingerprint: certificateFingerprint(leaf.Raw),
		NotAfter: leaf.NotAfter.UTC(), LastRenewed: p.clock().UTC(), NextRenewal: leaf.NotAfter.Add(-managedRenewBefore).UTC(),
		CAFingerprint: caFingerprint,
	}
	status.SAN = append(status.SAN, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		status.SAN = append(status.SAN, ip.String())
	}
	p.mu.Lock()
	p.certificate, p.status = &pair, status
	p.mu.Unlock()
}

func (p *dynamicCertificateProvider) setError(code string) {
	p.mu.Lock()
	p.status.Provider, p.status.State, p.status.LastErrorCode = p.provider, "needs_attention", code
	p.mu.Unlock()
}

func loadValidatedKeyPair(certPath, keyPath, host string, now time.Time) (tls.Certificate, *x509.Certificate, error) {
	for _, path := range []string{certPath, keyPath} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return tls.Certificate{}, nil, errors.New("server TLS material is unavailable")
		}
	}
	if err := validatePrivateKeyPermissions(keyPath); err != nil {
		return tls.Certificate{}, nil, err
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil || len(pair.Certificate) == 0 {
		return tls.Certificate{}, nil, errors.New("server TLS material is invalid")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) || host == "" || leaf.VerifyHostname(host) != nil {
		return tls.Certificate{}, nil, errors.New("server TLS certificate is invalid or mismatched")
	}
	return pair, leaf, nil
}

func loadManagedCA(certPath, keyPath string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	if validateManagedPKIFile(certPath) != nil || validateManagedPKIFile(keyPath) != nil {
		return nil, nil, errors.New("managed CA material is unsafe")
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, errors.New("managed CA certificate is unavailable")
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return nil, nil, errors.New("managed CA certificate is invalid")
	}
	certificate, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil || !certificate.IsCA || now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return nil, nil, errors.New("managed CA certificate is invalid")
	}
	if err := validatePrivateKeyPermissions(keyPath); err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, errors.New("managed CA key is unavailable")
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, errors.New("managed CA key is invalid")
	}
	keyValue, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	private, ok := keyValue.(*ecdsa.PrivateKey)
	if err != nil || !ok || !private.PublicKey.Equal(certificate.PublicKey) {
		return nil, nil, errors.New("managed CA key does not match certificate")
	}
	return certificate, private, nil
}

func validateManagedPKIFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || validatePrivateKeyPermissions(path) != nil {
		return errors.New("managed PKI file is unsafe")
	}
	return nil
}

func managedCAFingerprint(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return "", ErrInvalid
	}
	return certificateFingerprint(block.Bytes), nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}
