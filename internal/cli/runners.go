package cli

import (
	"github.com/yuanshu-ai/yuanshu/internal/node"
	"github.com/yuanshu-ai/yuanshu/internal/server"
	"github.com/yuanshu-ai/yuanshu/internal/standalone"
)

// DefaultRunners returns the pre-alpha role entry points.
func DefaultRunners() Runners {
	return Runners{
		Server:     server.Run,
		Node:       node.Run,
		Standalone: standalone.Run,
	}
}
