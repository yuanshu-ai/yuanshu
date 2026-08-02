// Package server contains the Yuanshu Server composition boundary.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
)

type Options struct {
	DataDir         string
	Listen          string
	Stdout          io.Writer
	Random          io.Reader
	Clock           func() time.Time
	Listener        net.Listener
	ShutdownTimeout time.Duration
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
	listener := options.Listener
	if listener == nil {
		listener, err = net.Listen("tcp", options.Listen)
		if err != nil {
			return errors.New("server listener is unavailable")
		}
	}
	defer listener.Close()
	if err := validateListener(listener); err != nil {
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
	handler, err := NewHandler(service, local)
	if err != nil {
		return err
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
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			_ = httpServer.Shutdown(shutdownCtx)
		case <-done:
		}
	}()
	err = httpServer.Serve(listener)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return errors.New("server HTTP service failed")
}

func validateRunOptions(options Options) error {
	if options.DataDir == "" || !filepath.IsAbs(options.DataDir) || options.Listen == "" || options.ShutdownTimeout < 0 {
		return ErrInvalid
	}
	host, port, err := net.SplitHostPort(options.Listen)
	if err != nil || port == "" {
		return ErrInvalid
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return ErrInvalid
	}
	if host != "127.0.0.1" && host != "::1" {
		return ErrInvalid
	}
	return nil
}

func validateListener(listener net.Listener) error {
	if listener == nil {
		return ErrInvalid
	}
	host, _, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return ErrInvalid
	}
	if host != "127.0.0.1" && host != "::1" {
		return ErrInvalid
	}
	return nil
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
