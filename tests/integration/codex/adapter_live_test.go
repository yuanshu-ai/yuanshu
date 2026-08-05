package codex_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	adapterpkg "github.com/yuanshu-ai/yuanshu/internal/adapter"
	formal "github.com/yuanshu-ai/yuanshu/internal/adapter/codex"
	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/node/workspace"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	protocol "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

const (
	adapterLiveEnvironment = "YUANSHU_CODEX_ADAPTER_LIVE"
	adapterLiveTurnLimit   = 2
)

func TestAdapterRuntimeCloseLive(t *testing.T) {
	if os.Getenv(adapterLiveEnvironment) != "1" {
		t.Skip("set YUANSHU_CODEX_ADAPTER_LIVE=1 to run the formal Runtime close check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dataPath := t.TempDir()
	nodeStore, err := store.Open(ctx, filepath.Join(dataPath, "node.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer nodeStore.Close()
	workspaceManager, err := workspace.NewManager(platform.Current().Workspaces(), nodeStore)
	if err != nil {
		t.Fatal(err)
	}
	formalAdapter, err := formal.New(formal.Options{
		Config:     config.CodexAdapterConfig{Enabled: true, Binary: "codex", RuntimeMode: "stdio"},
		Processes:  platform.Current().Processes(),
		Workspaces: workspaceManager,
		Threads:    nodeStore,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := formalAdapter.StartRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatalf("zero-Turn formal Runtime close: %v", err)
	}
}

func TestAdapterLive(t *testing.T) {
	if os.Getenv(adapterLiveEnvironment) != "1" {
		t.Skip("set YUANSHU_CODEX_ADAPTER_LIVE=1 to run the bounded formal Adapter test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()

	workspacePath := liveWorkspace(t)
	dataPath := t.TempDir()
	nodeStore, err := store.Open(ctx, filepath.Join(dataPath, "node.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer nodeStore.Close()
	workspaceManager, err := workspace.NewManager(platform.Current().Workspaces(), nodeStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspaceManager.Reconcile(ctx, []config.WorkspaceConfig{{
		ID: "ac203-live-workspace", DisplayName: "AC-203 Live Workspace", Path: workspacePath,
		AllowedAgentInstances: []string{config.DefaultCodexInstanceID}, DefaultAgentInstance: config.DefaultCodexInstanceID,
		PermissionProfile: config.PermissionReadOnly,
	}}); err != nil {
		t.Fatal(err)
	}

	processes := &recordingProcessManager{delegate: platform.Current().Processes()}
	formalAdapter, err := formal.New(formal.Options{
		Config:    config.CodexAdapterConfig{Enabled: true, Binary: "codex", RuntimeMode: "stdio"},
		Processes: processes, Workspaces: workspaceManager, Threads: nodeStore,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := formalAdapter.StartRuntime(ctx)
	if err != nil {
		t.Fatalf("start formal Runtime: %v", err)
	}
	var threadID string
	persisted := false
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cleanupCancel()
		if err := runtime.Close(cleanupCtx); err != nil {
			t.Errorf("close formal Runtime: %v", err)
		}
		if persisted && threadID != "" {
			client, err := startLiveClient(cleanupCtx, workspacePath)
			if err != nil {
				t.Errorf("start archive client: %v", err)
				return
			}
			if err := client.Call(cleanupCtx, "thread/archive", map[string]any{"threadId": threadID}, nil); err != nil {
				t.Errorf("archive formal Adapter Thread: %v", safeClientError(client, err))
			}
			if err := client.Close(); err != nil {
				t.Errorf("close archive client: %v", safeClientError(client, err))
			}
		}
	}()

	thread, err := runtime.StartThread(ctx, adapterpkg.StartThreadRequest{WorkspaceID: "ac203-live-workspace"})
	if err != nil {
		t.Fatalf("start Thread: %v", err)
	}
	threadID = thread.ID
	turns := 0
	startTurn := func(prompt string) adapterpkg.Turn {
		t.Helper()
		if turns >= adapterLiveTurnLimit {
			t.Fatalf("formal Adapter live turn limit %d exceeded", adapterLiveTurnLimit)
		}
		turns++
		turn, err := runtime.StartTurn(ctx, adapterpkg.StartTurnRequest{WorkspaceID: "ac203-live-workspace", ThreadID: threadID, Input: prompt})
		if err != nil {
			t.Fatalf("start Turn %d: %v", turns, err)
		}
		persisted = true
		return turn
	}

	first := startTurn("Request approval to run a harmless command that prints YUANSHU_AC203_ONE. Do not inspect files. If the command is declined, finish without using another tool.")
	firstApproval := waitAdapterApproval(t, ctx, runtime.Events(), first.ID)
	if err := runtime.ResolveApproval(ctx, adapterpkg.ApprovalDecision{
		WorkspaceID: "ac203-live-workspace", ThreadID: threadID, TurnID: first.ID,
		ItemID: firstApproval.ItemID, ApprovalID: firstApproval.Approval.ID, Decision: "decline",
	}); err != nil {
		t.Fatalf("decline first approval: %v", err)
	}
	waitAdapterTerminal(t, ctx, runtime.Events(), first.ID, false)

	if _, err := runtime.ReadThread(ctx, adapterpkg.ReadThreadRequest{WorkspaceID: "ac203-live-workspace", ThreadID: threadID, IncludeTurns: true}); err != nil {
		t.Fatalf("read Thread: %v", err)
	}
	page, err := runtime.ListThreads(ctx, adapterpkg.ListThreadsRequest{WorkspaceID: "ac203-live-workspace", Limit: 100})
	if err != nil || !containsAdapterThread(page.Data, threadID) {
		t.Fatalf("list Thread: %#v, %v", page, err)
	}

	appServer := processes.Last()
	if appServer == nil {
		t.Fatal("no managed app-server process recorded")
	}
	stopCtx, stopCancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = appServer.Stop(stopCtx)
	stopCancel()
	if err != nil {
		t.Fatalf("stop idle app-server: %v", err)
	}
	waitRuntimeState(t, ctx, runtime, "unavailable")
	if _, err := runtime.ReadThread(ctx, adapterpkg.ReadThreadRequest{WorkspaceID: "ac203-live-workspace", ThreadID: threadID, IncludeTurns: true}); err != nil {
		t.Fatalf("read after app-server restart: %v", err)
	}

	second := startTurn("Request approval to run a harmless command that prints YUANSHU_AC203_TWO. Do not inspect files. Wait for the approval decision.")
	secondApproval := waitAdapterApproval(t, ctx, runtime.Events(), second.ID)
	if err := runtime.SteerTurn(ctx, adapterpkg.SteerTurnRequest{WorkspaceID: "ac203-live-workspace", ThreadID: threadID, TurnID: second.ID, Input: "Remain idle while this synthetic verification interrupts the Turn."}); err != nil {
		t.Fatalf("steer second Turn: %v", err)
	}
	if err := runtime.InterruptTurn(ctx, adapterpkg.InterruptTurnRequest{WorkspaceID: "ac203-live-workspace", ThreadID: threadID, TurnID: second.ID}); err != nil {
		t.Fatalf("interrupt second Turn: %v", err)
	}
	waitAdapterTerminal(t, ctx, runtime.Events(), second.ID, true)
	if secondApproval.Approval == nil {
		t.Fatal("second approval was not retained until interrupt")
	}
	if turns != adapterLiveTurnLimit {
		t.Fatalf("used %d Turns, want %d", turns, adapterLiveTurnLimit)
	}
	health := runtime.Health()
	t.Logf("AC-203 formal Adapter result: codex=%s auth=%s transport=stdio turns=%d restart=passed approvals=passed", health.CodexVersion, health.Authentication, turns)
}

func waitAdapterApproval(t *testing.T, ctx context.Context, events <-chan adapterpkg.AgentEvent, turnID string) adapterpkg.AgentEvent {
	t.Helper()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("Adapter events closed while waiting for approval")
			}
			if event.Type == protocol.EventApprovalRequested && event.TurnID == turnID {
				return event
			}
			if isTerminalEvent(event.Type) && event.TurnID == turnID {
				t.Fatalf("Turn ended before approval: %s", event.Type)
			}
		case <-ctx.Done():
			t.Fatalf("wait for Adapter approval: %v", ctx.Err())
		}
	}
}

func waitAdapterTerminal(t *testing.T, ctx context.Context, events <-chan adapterpkg.AgentEvent, turnID string, wantInterrupted bool) {
	t.Helper()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("Adapter events closed while waiting for terminal Turn")
			}
			if event.TurnID != turnID || !isTerminalEvent(event.Type) {
				continue
			}
			if wantInterrupted && event.Type != protocol.EventTurnInterrupted {
				t.Fatalf("terminal event = %s, want interrupted", event.Type)
			}
			if !wantInterrupted && event.Type != protocol.EventTurnCompleted {
				t.Fatalf("terminal event = %s, want completed", event.Type)
			}
			return
		case <-ctx.Done():
			t.Fatalf("wait for terminal Adapter event: %v", ctx.Err())
		}
	}
}

func isTerminalEvent(value protocol.EventType) bool {
	return value == protocol.EventTurnCompleted || value == protocol.EventTurnFailed || value == protocol.EventTurnInterrupted
}

func containsAdapterThread(threads []adapterpkg.Thread, id string) bool {
	for _, thread := range threads {
		if thread.ID == id {
			return true
		}
	}
	return false
}

func waitRuntimeState(t *testing.T, ctx context.Context, runtime adapterpkg.Runtime, state string) {
	t.Helper()
	for runtime.Health().State != state {
		select {
		case <-ctx.Done():
			t.Fatalf("wait for Runtime state %s: %v", state, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

type recordingProcessManager struct {
	delegate  platform.ProcessManager
	mu        sync.RWMutex
	processes []platform.Process
}

func (r *recordingProcessManager) Available() bool { return r.delegate.Available() }

func (r *recordingProcessManager) Start(ctx context.Context, spec platform.ProcessSpec) (platform.Process, error) {
	process, err := r.delegate.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.processes = append(r.processes, process)
	r.mu.Unlock()
	return process, nil
}

func (r *recordingProcessManager) Last() platform.Process {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.processes) == 0 {
		return nil
	}
	return r.processes[len(r.processes)-1]
}

var _ = errors.Is
