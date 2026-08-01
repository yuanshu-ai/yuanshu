// Package server contains the Yuanshu Server composition boundary.
package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/poc/config"
	"github.com/yuanshu-ai/yuanshu/internal/poc/relay"
)

var ErrNotImplemented = errors.New("server PoC configuration is not available")

// Run starts the explicitly configured, loopback-only M0 PoC Server.
func Run(ctx context.Context) error {
	cfg, err := config.ServerFromEnv()
	if err != nil {
		return errors.Join(ErrNotImplemented, err)
	}
	hub, err := relay.New(cfg.NodeToken)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: cfg.Listen, Handler: hub.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	err = srv.ListenAndServeTLS(cfg.Cert, cfg.Key)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
