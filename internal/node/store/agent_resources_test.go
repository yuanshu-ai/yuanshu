package store

import (
	"context"
	"errors"
	"testing"
)

func TestAgentResourcesAndTaskBindingsAreIsolated(t *testing.T) {
	local, _ := openTestStore(t)
	for _, installation := range []AgentInstallationRecord{
		{AdapterType: "opencode", InstallationState: "installed", Version: "unknown", Compatibility: "unverified", ProcessState: "running", ProcessCount: 1},
		{AdapterType: "codex", InstallationState: "installed", Version: "0.144.6", Compatibility: "known", ProcessState: "running", ProcessCount: 1},
	} {
		if err := local.SaveAgentInstallation(context.Background(), installation); err != nil {
			t.Fatal(err)
		}
	}
	installations, err := local.AgentInstallations(context.Background())
	if err != nil || len(installations) != 2 || installations[0].AdapterType != "codex" || installations[1].AdapterType != "opencode" {
		t.Fatalf("installations=%+v err=%v", installations, err)
	}
	if err := local.ReplaceWorkspaces(context.Background(), []WorkspaceRecord{{
		ID: "workspace", DisplayName: "Workspace", CanonicalPath: "/synthetic/workspace", FilesystemRoot: "/",
		FileIdentity: "identity", Adapter: "codex", PermissionProfile: workspaceReadOnly,
	}}); err != nil {
		t.Fatal(err)
	}
	instances := []AgentInstanceRecord{
		{InstanceID: "codex-a", AdapterType: "codex", DisplayName: "Codex A", Enabled: true, Default: true, RuntimeMode: AgentRuntimeManaged, ConfigRevision: "revision-a"},
		{InstanceID: "codex-b", AdapterType: "codex", DisplayName: "Codex B", Enabled: true, RuntimeMode: AgentRuntimeManaged, ConfigRevision: "revision-b"},
	}
	endpoints := []RuntimeEndpointRecord{
		{EndpointID: "codex-a-managed", InstanceID: "codex-a", Mode: AgentRuntimeManaged, Ownership: EndpointOwnerNode},
		{EndpointID: "codex-b-managed", InstanceID: "codex-b", Mode: AgentRuntimeManaged, Ownership: EndpointOwnerNode},
	}
	links := []WorkspaceAgentRecord{{WorkspaceID: "workspace", InstanceID: "codex-a", Default: true}, {WorkspaceID: "workspace", InstanceID: "codex-b"}}
	if err := local.ReplaceAgentResources(context.Background(), instances, endpoints, links); err != nil {
		t.Fatal(err)
	}
	for _, binding := range []TaskBindingRecord{
		{TaskID: "task-a", InstanceID: "codex-a", EndpointID: "codex-a-managed", WorkspaceID: "workspace", NativeSessionID: "same-native", Ownership: "created", State: RuntimeThreadIdle},
		{TaskID: "task-b", InstanceID: "codex-b", EndpointID: "codex-b-managed", WorkspaceID: "workspace", NativeSessionID: "same-native", Ownership: "created", State: RuntimeThreadIdle},
	} {
		if err := local.SaveTaskBinding(context.Background(), binding); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := local.TaskBindingByNativeSession(context.Background(), "codex-a", "same-native"); err != nil || got.TaskID != "task-a" {
		t.Fatalf("binding=%+v err=%v", got, err)
	}
	if got, err := local.TaskBindingByNativeSession(context.Background(), "codex-b", "same-native"); err != nil || got.TaskID != "task-b" {
		t.Fatalf("binding=%+v err=%v", got, err)
	}
	duplicate := TaskBindingRecord{TaskID: "task-c", InstanceID: "codex-a", EndpointID: "codex-a-managed", WorkspaceID: "workspace", NativeSessionID: "same-native", Ownership: "created", State: RuntimeThreadIdle}
	if err := local.SaveTaskBinding(context.Background(), duplicate); err == nil {
		t.Fatal("duplicate native session accepted within one instance")
	}
}

func TestAgentResourcesRejectAttachedAndInvalidWorkspaceDefaults(t *testing.T) {
	local, _ := openTestStore(t)
	instances := []AgentInstanceRecord{{InstanceID: "codex", AdapterType: "codex", DisplayName: "Codex", Enabled: true, Default: true, RuntimeMode: "attached", ConfigRevision: "revision"}}
	if err := local.ReplaceAgentResources(context.Background(), instances, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("attached error=%v", err)
	}
}
