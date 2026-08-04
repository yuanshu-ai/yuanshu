// Package server contains the Yuanshu Server composition boundary.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
	"github.com/yuanshu-ai/yuanshu/internal/transport"
)

type LocalNodeSession struct {
	OwnerID          string
	NodeID           string
	Transport        transport.Transport
	SessionToken     []byte
	SessionExpiresAt time.Time
}

type Options struct {
	DataDir               string
	CertificateDataDir    string
	Listen                string
	DeploymentMode        DeploymentMode
	PublicURL             string
	TLSCertFile           string
	TLSKeyFile            string
	TLSTermination        string
	ACME                  ACMEConfig
	ACMEDirectoryURL      string
	ACMEHTTPClient        *http.Client
	AllowedControlOrigins []string
	WebEnabled            *bool
	AdminEnabled          *bool
	AdminSessionIdle      time.Duration
	AdminSessionMax       time.Duration
	AdminAuditRetention   time.Duration
	ConfigRevision        string
	Stdout                io.Writer
	Random                io.Reader
	Clock                 func() time.Time
	Listener              net.Listener
	ShutdownTimeout       time.Duration
	LocalNode             *LocalNodeSession
}

func certificateDataDir(options Options) string {
	if options.CertificateDataDir != "" {
		return options.CertificateDataDir
	}
	return options.DataDir
}

func Run(ctx context.Context, options Options) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRunOptions(options); err != nil {
		return err
	}
	if err := prepareDataDir(options.DataDir); err != nil {
		return err
	}
	lock, err := acquireDataLock(filepath.Join(options.DataDir, "server.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	if certificateDir := certificateDataDir(options); certificateDir != options.DataDir {
		if err := prepareDataDir(certificateDir); err != nil {
			return err
		}
		certificateLock, lockErr := acquireDataLock(filepath.Join(certificateDir, "server.lock"))
		if lockErr != nil {
			return errors.New("certificate provider is already in use")
		}
		defer certificateLock.Close()
	}
	local, err := serverstore.Open(ctx, filepath.Join(options.DataDir, "server.db"), serverstore.Options{Clock: options.Clock})
	if err != nil {
		return err
	}
	defer local.Close()
	certificateService, err := newCertificateProvider(ctx, options)
	if err != nil {
		return err
	}
	if certificateService != nil {
		defer certificateService.Close()
	}
	var tlsConfig *tls.Config
	if certificateService != nil {
		tlsConfig = certificateService.TLSConfig()
	}
	listener := options.Listener
	if listener == nil {
		listener, err = net.Listen("tcp", options.Listen)
		if err != nil {
			return errors.New("server listener is unavailable")
		}
	}
	defer listener.Close()
	if err := validateListener(listener, tlsConfig != nil); err != nil {
		return err
	}
	service, err := NewBootstrapService(local, BootstrapOptions{Random: options.Random, Clock: options.Clock})
	if err != nil {
		return err
	}
	secret, issued, err := service.Rotate(ctx)
	if err != nil {
		return err
	}
	if issued {
		writer := options.Stdout
		if writer == nil {
			writer = io.Discard
		}
		if _, err := fmt.Fprintf(writer, "Yuanshu Server bootstrap secret (shown once): %s\n", secret); err != nil {
			return errors.New("server bootstrap output failed")
		}
	}
	origins := append([]string(nil), options.AllowedControlOrigins...)
	if len(origins) == 0 && options.PublicURL != "" {
		origins = []string{controlOrigin(options.PublicURL)}
	} else if len(origins) == 0 && effectiveDeploymentMode(options) == DeploymentLocal {
		origins = []string{"http://" + listener.Addr().String()}
	}
	proxyPlain := effectiveDeploymentMode(options) == DeploymentExternal && effectiveTLSTermination(options) == "proxy"
	hub, err := NewHub(local, HubOptions{
		Random: options.Random, Clock: options.Clock, AllowedControlOrigins: origins,
		AllowLoopbackPlain: effectiveDeploymentMode(options) == DeploymentLocal, LoopbackAuthority: listener.Addr().String(),
		AllowLoopbackProxyPlain: proxyPlain, ProxyPublicHost: publicURLAuthority(options.PublicURL),
	})
	if err != nil {
		return err
	}
	defer hub.Close()
	var tlsSAN []string
	var tlsNotAfter time.Time
	var tlsFingerprint string
	if certificateService != nil {
		certificateStatus := certificateService.Status()
		tlsSAN = append(tlsSAN, certificateStatus.SAN...)
		tlsNotAfter = certificateStatus.NotAfter.UTC()
		tlsFingerprint = certificateStatus.Fingerprint
	}
	handler, err := newHandler(service, serverReadiness{database: local, certificate: certificateService, clock: options.Clock}, local, hub, adminHandlerOptions{
		Enabled: adminEnabled(options.AdminEnabled), PublicURL: options.PublicURL,
		Listen: options.Listen, WebEnabled: embeddedWebEnabled(options.WebEnabled),
		TLSConfigured: tlsConfig != nil || effectiveDeploymentMode(options) == DeploymentExternal && effectiveTLSTermination(options) == "proxy", AllowedOrigins: origins,
		SessionIdle: options.AdminSessionIdle, SessionMax: options.AdminSessionMax,
		AuditRetention: options.AdminAuditRetention, Random: options.Random, Clock: options.Clock,
		StartedAt: time.Now().UTC(), DatabasePath: filepath.Join(options.DataDir, "server.db"), ConfigRevision: options.ConfigRevision,
		DeploymentMode: string(effectiveDeploymentMode(options)), CertificateProvider: certificateProviderName(certificateService),
		Certificate: certificateService,
		TLSSAN:      tlsSAN, TLSNotAfter: tlsNotAfter, TLSFingerprint: tlsFingerprint,
	})
	if err != nil {
		return err
	}
	handler = withWellKnownDiscovery(handler, wellKnownDiscovery{
		DeploymentMode: string(effectiveDeploymentMode(options)),
		PublicURL:      webAccessURL(options.PublicURL, listener.Addr()),
		CAFingerprint:  shortCertificateFingerprint(tlsFingerprint),
		Invitations:    adminEnabled(options.AdminEnabled),
	})
	handler, err = newWebDeliveryHandler(handler, webDeliveryOptions{
		Enabled:      embeddedWebEnabled(options.WebEnabled),
		PublicURL:    options.PublicURL,
		AdminEnabled: adminEnabled(options.AdminEnabled),
		Certificate:  certificateService,
	})
	if err != nil {
		return err
	}
	if effectiveDeploymentMode(options) == DeploymentLocal {
		handler = requireRequestHost(handler, listener.Addr().String())
	}
	if embeddedWebEnabled(options.WebEnabled) {
		writer := options.Stdout
		if writer == nil {
			writer = io.Discard
		}
		if _, err := fmt.Fprintf(writer, "Yuanshu Web: %s\n", webAccessURL(options.PublicURL, listener.Addr())); err != nil {
			return errors.New("server web address output failed")
		}
	}
	shutdownTimeout := options.ShutdownTimeout
	if shutdownTimeout == 0 {
		shutdownTimeout = 5 * time.Second
	}
	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	done := make(chan struct{})
	localDone := make(chan error, 1)
	if options.LocalNode != nil {
		go func() {
			localDone <- hub.AttachLocalNodeSession(ctx, options.LocalNode.OwnerID, options.LocalNode.NodeID, options.LocalNode.Transport, options.LocalNode.SessionToken, options.LocalNode.SessionExpiresAt)
		}()
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-localDone:
		case <-done:
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	serveListener := listener
	if tlsConfig != nil {
		serveListener = tls.NewListener(listener, tlsConfig)
	}
	err = httpServer.Serve(serveListener)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return errors.New("server HTTP service failed")
}

func requireRequestHost(next http.Handler, authority string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Host != authority {
			writeError(writer, http.StatusMisdirectedRequest, "invalid_host")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

type serverReadiness struct {
	database    readiness
	certificate certificateProvider
	clock       func() time.Time
}

func (r serverReadiness) QuickCheck(ctx context.Context) error {
	if r.database == nil {
		return ErrInvalid
	}
	if err := r.database.QuickCheck(ctx); err != nil {
		return err
	}
	if r.certificate == nil {
		return nil
	}
	now := time.Now()
	if r.clock != nil {
		now = r.clock()
	}
	status := r.certificate.Status()
	if status.NotAfter.IsZero() || !now.Before(status.NotAfter) {
		return errors.New("server TLS certificate is unavailable")
	}
	return nil
}

func embeddedWebEnabled(value *bool) bool {
	return value == nil || *value
}

func webAccessURL(publicURL string, address net.Addr) string {
	if publicURL != "" {
		return strings.TrimSuffix(publicURL, "/") + "/"
	}
	host := address.String()
	if tcpAddress, ok := address.(*net.TCPAddr); ok && tcpAddress.IP.IsUnspecified() {
		host = net.JoinHostPort("127.0.0.1", strconv.Itoa(tcpAddress.Port))
	}
	return "http://" + host + "/"
}

func validateRunOptions(options Options) error {
	if options.DataDir == "" || !filepath.IsAbs(options.DataDir) || options.CertificateDataDir != "" && !filepath.IsAbs(options.CertificateDataDir) || options.Listen == "" || options.ShutdownTimeout < 0 || !validPublicOptions(options) {
		return ErrInvalid
	}
	_, port, err := net.SplitHostPort(options.Listen)
	if err != nil || port == "" {
		return ErrInvalid
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return ErrInvalid
	}
	if options.LocalNode != nil && (options.LocalNode.Transport == nil || options.LocalNode.OwnerID == "" || options.LocalNode.NodeID == "") {
		return ErrInvalid
	}
	if !validControlOriginsForMode(options.AllowedControlOrigins, effectiveDeploymentMode(options) == DeploymentLocal) {
		return ErrInvalid
	}
	return nil
}

func certificateProviderName(provider certificateProvider) string {
	if provider == nil {
		return "none"
	}
	return provider.Status().Provider
}

func publicURLAuthority(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	return parsed.Host
}

func validControlOrigins(origins []string) bool {
	for _, origin := range origins {
		if !validateControlOrigin(origin) {
			return false
		}
	}
	return true
}

func validateListener(listener net.Listener, public bool) error {
	if listener == nil {
		return ErrInvalid
	}
	host, _, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return ErrInvalid
	}
	if net.ParseIP(host) == nil || (!public && host != "127.0.0.1" && host != "::1") {
		return ErrInvalid
	}
	return nil
}

func loadTLSConfig(options Options) (*tls.Config, error) {
	if options.PublicURL == "" {
		return nil, nil
	}
	host := publicURLHost(options.PublicURL)
	pair, leaf, err := loadValidatedKeyPair(options.TLSCertFile, options.TLSKeyFile, host, time.Now())
	if err != nil {
		return nil, err
	}
	pair.Leaf = leaf
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}, nil
}

func prepareDataDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return errors.New("server data directory is unavailable")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("server data directory is unavailable")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return errors.New("server data directory permissions could not be applied")
	}
	return nil
}
