package store

import (
	"context"
	"errors"
	"testing"
)

func TestRuntimeThreadStoreLifecycle(t *testing.T) {
	database, _ := openTestStore(t)
	record := RuntimeThreadRecord{AgentInstanceID: "codex-default", Adapter: "codex", ThreadID: "thread-1", WorkspaceID: "workspace-1", Ownership: "created", State: RuntimeThreadIdle}
	if err := database.SaveRuntimeThread(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	got, err := database.RuntimeThread(context.Background(), record.ThreadID)
	if err != nil || got != record {
		t.Fatalf("RuntimeThread = %#v, %v", got, err)
	}
	record.State, record.ActiveTurnID = RuntimeThreadActive, "turn-1"
	if err := database.SaveRuntimeThread(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	list, err := database.RuntimeThreads(context.Background())
	if err != nil || len(list) != 1 || list[0] != record {
		t.Fatalf("RuntimeThreads = %#v, %v", list, err)
	}
	if err := database.DeleteRuntimeThread(context.Background(), record.ThreadID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.RuntimeThread(context.Background(), record.ThreadID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read deleted error = %v", err)
	}
}

func TestRuntimeThreadValidationAndWorkspaceCleanup(t *testing.T) {
	database, _ := openTestStore(t)
	invalid := []RuntimeThreadRecord{
		{},
		{Adapter: "other", ThreadID: "thread", WorkspaceID: "workspace", Ownership: "created", State: RuntimeThreadIdle},
		{Adapter: "codex", ThreadID: "thread", WorkspaceID: "workspace", Ownership: "created", State: RuntimeThreadActive},
	}
	for _, record := range invalid {
		if err := database.SaveRuntimeThread(context.Background(), record); !errors.Is(err, ErrInvalid) {
			t.Fatalf("SaveRuntimeThread(%#v) error = %v", record, err)
		}
	}
	workspaceRecord := WorkspaceRecord{ID: "workspace", DisplayName: "Workspace", CanonicalPath: `C:\synthetic`, FilesystemRoot: `C:\`, FileIdentity: "volume:file", Adapter: "codex", PermissionProfile: workspaceReadOnly}
	if err := database.ReplaceWorkspaces(context.Background(), []WorkspaceRecord{workspaceRecord}); err != nil {
		t.Fatal(err)
	}
	record := RuntimeThreadRecord{Adapter: "codex", ThreadID: "thread", WorkspaceID: "workspace", Ownership: "created", State: RuntimeThreadIdle}
	if err := database.SaveRuntimeThread(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := database.ReplaceWorkspaces(context.Background(), []WorkspaceRecord{workspaceRecord}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.RuntimeThread(context.Background(), "thread"); err != nil {
		t.Fatalf("same workspace removed ownership: %v", err)
	}
	if err := database.ReplaceWorkspaces(context.Background(), []WorkspaceRecord{}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.RuntimeThread(context.Background(), "thread"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed workspace retained ownership: %v", err)
	}
}

func TestRuntimeThreadsAreScopedByAgentInstance(t *testing.T) {
	database, _ := openTestStore(t)
	for _, instanceID := range []string{"codex-office", "codex-home"} {
		record := RuntimeThreadRecord{AgentInstanceID: instanceID, Adapter: "codex", ThreadID: "same-native-session", WorkspaceID: "workspace", Ownership: "created", State: RuntimeThreadIdle}
		if err := database.SaveRuntimeThread(context.Background(), record); err != nil {
			t.Fatalf("SaveRuntimeThread(%s): %v", instanceID, err)
		}
	}
	for _, instanceID := range []string{"codex-office", "codex-home"} {
		record, err := database.RuntimeThreadForInstance(context.Background(), instanceID, "same-native-session")
		if err != nil || record.AgentInstanceID != instanceID {
			t.Fatalf("RuntimeThreadForInstance(%s) = %#v, %v", instanceID, record, err)
		}
	}
	records, err := database.RuntimeThreads(context.Background())
	if err != nil || len(records) != 2 {
		t.Fatalf("RuntimeThreads() = %#v, %v", records, err)
	}
}
