// Package server contains the Yuanshu Server composition boundary.
package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
	"github.com/yuanshu-ai/yuanshu/internal/transport"
)

type LocalNodeSession struct {
	OwnerID   string
	NodeID    string
	Transport transport.Transport
}

type Options struct {
	DataDir               string
	Listen                string
	PublicURL             string
	TLSCertFile           string
	TLSKeyFile            string
	AllowedControlOrigins []string
	WebEnabled            *bool
	Stdout                io.Writer
	Random                io.Reader
	Clock                 func() time.Time
	Listener              net.Listener
	ShutdownTimeout       time.Duration
	LocalNode             *LocalNodeSession
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
	local, err := serverstore.Open(ctx, filepath.Join(options.DataDir, "server.db"), serverstore.Options{Clock: options.Clock})
	if err != nil {
		return err
	}
	defer local.Close()
	tlsConfig, err := loadTLSConfig(options)
	if err != nil {
		return err
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
	}
	hub, err := NewHub(local, HubOptions{Random: options.Random, Clock: options.Clock, AllowedControlOrigins: origins})
	if err != nil {
		return err
	}
	defer hub.Close()
	handler, err := NewHandler(service, local, hub)
	if err != nil {
		return err
	}
	handler, err = newWebDeliveryHandler(handler, webDeliveryOptions{
		Enabled:   embeddedWebEnabled(options.WebEnabled),
		PublicURL: options.PublicURL,
	})
	if err != nil {
		return err
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
			localDone <- hub.AttachLocalNode(ctx, options.LocalNode.OwnerID, options.LocalNode.NodeID, options.LocalNode.Transport)
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
	if options.DataDir == "" || !filepath.IsAbs(options.DataDir) || options.Listen == "" || options.ShutdownTimeout < 0 || !validPublicOptions(options) {
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
	if !validControlOrigins(options.AllowedControlOrigins) {
		return ErrInvalid
	}
	return nil
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
	for _, path := range []string{options.TLSCertFile, options.TLSKeyFile} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("server TLS material is unavailable")
		}
	}
	if err := validatePrivateKeyPermissions(options.TLSKeyFile); err != nil {
		return nil, err
	}
	pair, err := tls.LoadX509KeyPair(options.TLSCertFile, options.TLSKeyFile)
	if err != nil || len(pair.Certificate) == 0 {
		return nil, errors.New("server TLS material is invalid")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, errors.New("server TLS certificate is invalid")
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return nil, errors.New("server TLS certificate is not currently valid")
	}
	host := publicURLHost(options.PublicURL)
	if host == "" || leaf.VerifyHostname(host) != nil {
		return nil, errors.New("server TLS certificate does not match public URL")
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
