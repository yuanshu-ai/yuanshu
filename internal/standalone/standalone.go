// Package standalone contains the combined Server and local Node boundary.
package standalone

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/poc/codex"
	"github.com/yuanshu-ai/yuanshu/internal/poc/config"
	pocnode "github.com/yuanshu-ai/yuanshu/internal/poc/node"
	"github.com/yuanshu-ai/yuanshu/internal/poc/relay"
	"github.com/yuanshu-ai/yuanshu/internal/poc/transport"
)

var ErrNotImplemented = errors.New("standalone PoC configuration is not available")

// Run combines the Server and Node but retains the exact Node controller path.
func Run(ctx context.Context) error {
	serverCfg, nodeCfg, err := config.StandaloneFromEnv()
	if err != nil {
		return errors.Join(ErrNotImplemented, err)
	}
	runtime, err := codex.Start(ctx, nodeCfg.Workspace, nodeCfg.ArchiveOnClose)
	if err != nil {
		return err
	}
	hub, err := relay.New(serverCfg.NodeToken)
	if err != nil {
		_ = runtime.Close()
		return err
	}
	serverSide, nodeSide := transport.StandalonePair(64)
	if err := hub.AttachNode(ctx, serverSide); err != nil {
		_ = runtime.Close()
		return err
	}
	controller := pocnode.New("poc-node", runtime)
	if nodeCfg.ArchiveOnClose {
		controller.StopAfterTerminal()
	}
	defer controller.Close()
	nodeDone := make(chan error, 1)
	go func() { nodeDone <- controller.Run(ctx, nodeSide) }()
	srv := &http.Server{Addr: serverCfg.Listen, Handler: hub.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		select {
		case <-ctx.Done():
		case <-nodeDone:
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	err = srv.ListenAndServeTLS(serverCfg.Cert, serverCfg.Key)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
