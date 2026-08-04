// Package builtin composes Yuanshu's statically linked Agent adapters.
// It is the only production composition package that imports concrete
// Adapter implementations.
package builtin

import (
	"context"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex"
	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/node/workspace"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

const (
	CodexAgentType         = "codex"
	CodexDefaultInstanceID = "codex-default"
)

type WorkspaceResolver interface {
	Resolve(context.Context, string) (workspace.ResolvedWorkspace, error)
	ResolvePath(context.Context, string, string, workspace.PathIntent) (workspace.ResolvedPath, error)
}

type RuntimeThreadStore interface {
	SaveRuntimeThread(context.Context, store.RuntimeThreadRecord) error
	RuntimeThread(context.Context, string) (store.RuntimeThreadRecord, error)
	RuntimeThreads(context.Context) ([]store.RuntimeThreadRecord, error)
}

type Options struct {
	CodexConfig     config.CodexAdapterConfig
	Processes       platform.ProcessManager
	Workspaces      WorkspaceResolver
	Threads         RuntimeThreadStore
	EventCapacity   int
	ApprovalTimeout time.Duration
}

func NewRegistry(options Options) (*adapter.Registry, error) {
	return adapter.NewRegistry(adapter.Registration{
		Agent: adapter.AgentDescriptor{Type: CodexAgentType, DisplayName: "Codex"},
		Instance: adapter.InstanceDescriptor{
			ID: CodexDefaultInstanceID, AgentType: CodexAgentType, DisplayName: "Codex",
		},
		Default: true,
		Factory: func() (adapter.AgentAdapter, error) {
			return codex.New(codex.Options{
				Config: options.CodexConfig, Processes: options.Processes,
				Workspaces: options.Workspaces, Threads: options.Threads,
				EventCapacity: options.EventCapacity, ApprovalTimeout: options.ApprovalTimeout,
			})
		},
	})
}
