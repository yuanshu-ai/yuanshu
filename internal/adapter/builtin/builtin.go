// Package builtin composes Yuanshu's statically linked Agent adapters.
// It is the only production composition package that imports concrete
// Adapter implementations.
package builtin

import (
	"context"
	"strings"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex"
	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/node/workspace"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

func ManagedEndpointID(instanceID string) string {
	return strings.TrimSpace(instanceID) + "-managed"
}

const (
	CodexAgentType         = "codex"
	CodexDefaultInstanceID = "codex-default"
	CodexDefaultEndpointID = "codex-default-managed"
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

type instanceRuntimeThreadStore interface {
	RuntimeThreadForInstance(context.Context, string, string) (store.RuntimeThreadRecord, error)
	RuntimeThreadsForInstance(context.Context, string) ([]store.RuntimeThreadRecord, error)
}

type scopedThreadStore struct {
	instanceID string
	base       RuntimeThreadStore
}

func (s scopedThreadStore) SaveRuntimeThread(ctx context.Context, record store.RuntimeThreadRecord) error {
	record.AgentInstanceID = s.instanceID
	return s.base.SaveRuntimeThread(ctx, record)
}

func (s scopedThreadStore) RuntimeThread(ctx context.Context, threadID string) (store.RuntimeThreadRecord, error) {
	if scoped, ok := s.base.(instanceRuntimeThreadStore); ok {
		return scoped.RuntimeThreadForInstance(ctx, s.instanceID, threadID)
	}
	return s.base.RuntimeThread(ctx, threadID)
}

func (s scopedThreadStore) RuntimeThreads(ctx context.Context) ([]store.RuntimeThreadRecord, error) {
	if scoped, ok := s.base.(instanceRuntimeThreadStore); ok {
		return scoped.RuntimeThreadsForInstance(ctx, s.instanceID)
	}
	return s.base.RuntimeThreads(ctx)
}

type Options struct {
	CodexConfig     config.CodexAdapterConfig
	AgentInstances  []config.AgentInstanceConfig
	Processes       platform.ProcessManager
	Inspector       platform.ProcessInspector
	Workspaces      WorkspaceResolver
	Threads         RuntimeThreadStore
	EventCapacity   int
	ApprovalTimeout time.Duration
}

func NewInventory(options Options) (*adapter.Inventory, error) {
	detector, err := codex.NewDetector(codex.DetectorOptions{
		Config: options.CodexConfig, Processes: options.Processes, Inspector: options.Inspector,
	})
	if err != nil {
		return nil, err
	}
	return adapter.NewInventory(detector)
}

func NewRegistry(options Options) (*adapter.Registry, error) {
	instances := options.AgentInstances
	if len(instances) == 0 {
		instances = []config.AgentInstanceConfig{{
			ID: CodexDefaultInstanceID, AdapterType: CodexAgentType, DisplayName: "Codex",
			Enabled: true, IsDefault: true, RuntimeMode: config.AgentRuntimeManaged,
			Codex: &options.CodexConfig,
		}}
	}
	registrations := make([]adapter.Registration, 0, len(instances))
	for _, configured := range instances {
		if !configured.Enabled || configured.RuntimeMode != config.AgentRuntimeManaged {
			continue
		}
		if configured.AdapterType != CodexAgentType || configured.Codex == nil {
			return nil, adapter.ErrUnsupported
		}
		instance := configured
		codexConfig := *configured.Codex
		threads := scopedThreadStore{instanceID: instance.ID, base: options.Threads}
		registrations = append(registrations, adapter.Registration{
			Agent: adapter.AgentDescriptor{Type: CodexAgentType, DisplayName: "Codex"},
			Instance: adapter.InstanceDescriptor{
				ID: instance.ID, AgentType: CodexAgentType, DisplayName: instance.DisplayName,
			},
			Default: instance.IsDefault,
			Factory: func() (adapter.AgentAdapter, error) {
				return codex.New(codex.Options{
					Config: codexConfig, Processes: options.Processes,
					Workspaces: options.Workspaces, Threads: threads,
					EventCapacity: options.EventCapacity, ApprovalTimeout: options.ApprovalTimeout,
				})
			},
		})
	}
	return adapter.NewRegistry(registrations...)
}
