// Package node contains the local Yuanshu Node composition boundary.
package node

import (
	"context"
	"errors"
	"net/http"

	"github.com/yuanshu-ai/yuanshu/internal/poc/codex"
	"github.com/yuanshu-ai/yuanshu/internal/poc/config"
	pocnode "github.com/yuanshu-ai/yuanshu/internal/poc/node"
	"github.com/yuanshu-ai/yuanshu/internal/poc/protocol"
	"github.com/yuanshu-ai/yuanshu/internal/poc/transport"
)

var ErrNotImplemented = errors.New("node PoC configuration is not available")

// Run starts the outbound-only WSS bridge and Node-owned stdio Runtime.
func Run(ctx context.Context) error {
	cfg, err := config.NodeFromEnv()
	if err != nil {
		return errors.Join(ErrNotImplemented, err)
	}
	runtime, err := codex.Start(ctx, cfg.Workspace, cfg.ArchiveOnClose)
	if err != nil {
		return err
	}
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+cfg.NodeToken)
	ep, err := transport.DialWebSocket(ctx, cfg.ServerURL+"/poc/node", header, protocol.MaxControlBytes, protocol.MaxEventBytes)
	if err != nil {
		_ = runtime.Close()
		return err
	}
	controller := pocnode.New("poc-node", runtime)
	if cfg.ArchiveOnClose {
		controller.StopAfterTerminal()
	}
	defer controller.Close()
	return controller.Run(ctx, ep)
}
