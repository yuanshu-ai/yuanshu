package cli

import (
	"context"
	"io"

	"github.com/yuanshu-ai/yuanshu/internal/node"
	"github.com/yuanshu-ai/yuanshu/internal/server"
	"github.com/yuanshu-ai/yuanshu/internal/standalone"
)

// DefaultRunners returns the pre-alpha role entry points.
func DefaultRunners() Runners {
	return Runners{
		Server:     func(ctx context.Context, _ []string, _ io.Writer, _ io.Writer) error { return server.Run(ctx) },
		Node:       node.Command,
		Standalone: func(ctx context.Context, _ []string, _ io.Writer, _ io.Writer) error { return standalone.Run(ctx) },
	}
}
