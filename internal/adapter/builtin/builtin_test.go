package builtin_test

import (
	"context"
	"testing"

	"github.com/yuanshu-ai/yuanshu/internal/adapter/builtin"
	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/node/workspace"
	platformfake "github.com/yuanshu-ai/yuanshu/internal/platform/fake"
)

func TestBuiltinRegistryProvidesCodexDefault(t *testing.T) {
	registry, err := builtin.NewRegistry(builtin.Options{
		CodexConfig: config.CodexAdapterConfig{Enabled: true, Binary: "codex", RuntimeMode: "stdio"},
		Processes:   platformfake.NewProcessManager(),
		Workspaces:  workspaceResolver{},
		Threads:     threadStore{},
	})
	if err != nil {
		t.Fatal(err)
	}
	agents := registry.Agents()
	instances := registry.Instances()
	if len(agents) != 1 || agents[0].Type != builtin.CodexAgentType || agents[0].DisplayName != "Codex" {
		t.Fatalf("Agents() = %+v", agents)
	}
	if len(instances) != 1 || instances[0].ID != builtin.CodexDefaultInstanceID ||
		instances[0].AgentType != builtin.CodexAgentType {
		t.Fatalf("Instances() = %+v", instances)
	}
	handle, err := registry.CreateDefault()
	if err != nil || handle.Adapter.ID() != builtin.CodexAgentType {
		t.Fatalf("CreateDefault() = %+v, %v", handle, err)
	}
}

type workspaceResolver struct{}

func (workspaceResolver) Resolve(context.Context, string) (workspace.ResolvedWorkspace, error) {
	return workspace.ResolvedWorkspace{}, workspace.ErrNotFound
}

func (workspaceResolver) ResolvePath(context.Context, string, string, workspace.PathIntent) (workspace.ResolvedPath, error) {
	return workspace.ResolvedPath{}, workspace.ErrNotFound
}

type threadStore struct{}

func (threadStore) SaveRuntimeThread(context.Context, store.RuntimeThreadRecord) error {
	return store.ErrClosed
}

func (threadStore) RuntimeThread(context.Context, string) (store.RuntimeThreadRecord, error) {
	return store.RuntimeThreadRecord{}, store.ErrNotFound
}

func (threadStore) RuntimeThreads(context.Context) ([]store.RuntimeThreadRecord, error) {
	return nil, nil
}
