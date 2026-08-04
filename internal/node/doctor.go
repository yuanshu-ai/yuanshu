package node

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/adapter/builtin"
	"github.com/yuanshu-ai/yuanshu/internal/config"
	noderuntime "github.com/yuanshu-ai/yuanshu/internal/node/runtime"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/node/workspace"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

func diagnose(ctx context.Context, current platform.Platform, locations paths, configPath string) (Status, bool) {
	status := newStatusStore(string(current.Family())).snapshot()
	status.State = "needs_attention"
	status.RemoteControl = "not_available"
	if result, err := callLocal(ctx, current.IPC(), "status"); err == nil && result.OK && result.Status != nil {
		return *result.Status, result.Status.State == "ready" || result.Status.State == "unpaired"
	}
	if manager := current.Autostart(); manager != nil && manager.Available() {
		if result, err := manager.Status(ctx, autostartID); err == nil && result.Installed {
			status.Autostart = "enabled"
		} else {
			status.Autostart = "disabled"
		}
	} else {
		status.Autostart = "unavailable"
	}
	fileStore, err := config.NewFileStore(configPath)
	if err != nil {
		status.Config = "invalid"
		return status, false
	}
	loaded, err := fileStore.Load(ctx)
	if err != nil {
		status.Config = "unavailable"
		return status, false
	}
	if loaded.RecoveredFromBackup {
		status.Config = "recovered"
	} else {
		status.Config = "ready"
	}
	report, err := config.CheckSecretRefs(ctx, loaded.Config, current.SecureStore())
	if err != nil {
		status.Identity = "unavailable"
		return status, false
	}
	identityState := report[config.SecretIdentityPrivateKey]
	status.NodeAuthentication = "device_signature"
	if report[config.SecretRelayCredential] == config.SecretAvailable {
		status.Credential = "legacy_pending_cleanup"
	} else {
		status.Credential = "not_required"
	}
	switch identityState {
	case config.SecretAvailable:
		status.Identity = "available"
	case config.SecretMissing, config.SecretUnset:
		status.Identity = "not_initialized"
	default:
		status.Identity = "unavailable"
	}
	if _, err := os.Stat(locations.database); errors.Is(err, os.ErrNotExist) {
		status.Database = "not_initialized"
	} else if err != nil {
		status.Database = "unavailable"
		return status, false
	} else if inspection, err := store.Inspect(ctx, locations.database); err != nil || inspection.SchemaVersion != store.CurrentSchemaVersion {
		status.Database = "invalid"
		return status, false
	} else {
		status.Database = "ready"
	}
	status.Workspaces = len(loaded.Config.Workspaces)
	status.WorkspaceStatus = "ready"
	for _, configured := range loaded.Config.Workspaces {
		if _, err := current.Workspaces().Inspect(ctx, configured.Path); err != nil {
			status.State = "needs_attention"
			status.WorkspaceStatus = "unavailable"
			return status, false
		}
	}
	inventory, err := builtin.NewInventory(builtin.Options{
		CodexConfig: loaded.Config.Adapters.Codex, Processes: current.Processes(), Inspector: current.ProcessInspector(),
	})
	if err != nil {
		status.Codex, status.Compatibility = "unavailable", "unavailable"
		return status, false
	}
	inventorySnapshot, err := inventory.Refresh(ctx)
	if err != nil || len(inventorySnapshot.Installations) != 1 {
		status.Codex, status.Compatibility = "unavailable", "unavailable"
		return status, false
	}
	detected := inventorySnapshot.Installations[0]
	switch detected.State {
	case adapter.InstallationInstalled:
		status.Codex = "ready"
	case adapter.InstallationIncompatible:
		status.Codex, status.Compatibility = "unsupported", "unsupported"
		return status, false
	default:
		status.Codex, status.Compatibility = "unavailable", "unavailable"
		return status, false
	}
	status.Compatibility = string(detected.Installation.Compatibility)
	registry, err := builtin.NewRegistry(builtin.Options{
		CodexConfig: loaded.Config.Adapters.Codex, Processes: current.Processes(), Inspector: current.ProcessInspector(),
		Workspaces: diagnosticWorkspace{}, Threads: diagnosticThreads{}, ApprovalTimeout: time.Second,
	})
	if err != nil {
		status.Codex = "unavailable"
		return status, false
	}
	handle, err := registry.CreateDefault()
	if err != nil {
		status.Codex = "unavailable"
		return status, false
	}
	runtimeDescriptor, err := handle.Detect(ctx)
	if err != nil {
		if errors.Is(err, adapter.ErrUnsupported) {
			status.Codex = "unsupported"
			status.Compatibility = "unsupported"
		} else {
			status.Codex = "unavailable"
			status.Compatibility = "unavailable"
		}
		return status, false
	}
	status.Codex = "ready"
	status.Compatibility = string(runtimeDescriptor.Installation.Compatibility)
	if status.Compatibility == "" {
		status.Compatibility = "unverified"
	}
	runtimeManager, err := noderuntime.NewManager(noderuntime.Options{})
	if err != nil {
		status.Authentication = "unavailable"
		return status, false
	}
	runtime, err := runtimeManager.Open(ctx, noderuntime.OpenRequest{
		Key:  noderuntime.RuntimeKey{InstanceID: builtin.CodexDefaultInstanceID, EndpointID: builtin.CodexDefaultEndpointID},
		Mode: adapter.RuntimeManaged, Factory: handle.Adapter.StartRuntime,
	})
	if err != nil {
		status.Authentication = "unavailable"
		return status, false
	}
	health := runtime.Health()
	status.Authentication = health.Authentication
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	closeErr := runtimeManager.Close(closeCtx)
	cancel()
	if closeErr != nil {
		status.Authentication = "unavailable"
		return status, false
	}
	status.State = "unpaired"
	status.Recovery = "not_required"
	return status, true
}

type diagnosticWorkspace struct{}

func (diagnosticWorkspace) Resolve(context.Context, string) (workspace.ResolvedWorkspace, error) {
	return workspace.ResolvedWorkspace{}, workspace.ErrNotFound
}
func (diagnosticWorkspace) ResolvePath(context.Context, string, string, workspace.PathIntent) (workspace.ResolvedPath, error) {
	return workspace.ResolvedPath{}, workspace.ErrNotFound
}

type diagnosticThreads struct{}

func (diagnosticThreads) SaveRuntimeThread(context.Context, store.RuntimeThreadRecord) error {
	return store.ErrClosed
}
func (diagnosticThreads) RuntimeThread(context.Context, string) (store.RuntimeThreadRecord, error) {
	return store.RuntimeThreadRecord{}, store.ErrNotFound
}
func (diagnosticThreads) RuntimeThreads(context.Context) ([]store.RuntimeThreadRecord, error) {
	return nil, nil
}
