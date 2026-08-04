package adapter_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/adapter/adaptertest"
)

func TestInventoryStableSnapshotAndOwnershipIsolation(t *testing.T) {
	claude := adaptertest.NewDetector("claude-code", "Claude Code", adapter.InstallationDescriptor{
		ID: "claude-code-fixture", State: adapter.InstallationInstalled, Process: adapter.ProcessRunning,
		Installation: adapter.Installation{Detected: true, Version: "fixture", Compatibility: adapter.CompatibilityUnverified},
	})
	opencode := adaptertest.NewDetector("opencode", "OpenCode", adapter.InstallationDescriptor{
		ID: "opencode-fixture", State: adapter.InstallationInstalled, Process: adapter.ProcessStopped,
		Installation: adapter.Installation{Detected: true, Version: "fixture", Compatibility: adapter.CompatibilityKnown},
	})
	inventory, err := adapter.NewInventory(opencode, claude)
	if err != nil {
		t.Fatalf("NewInventory: %v", err)
	}
	snapshot, err := inventory.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if snapshot.Generation != 1 || len(snapshot.Installations) != 2 || snapshot.Installations[0].Agent.Type != "claude-code" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	snapshot.Installations[0].ID = "mutated"
	if current := inventory.Snapshot(); current.Installations[0].ID != "claude-code-fixture" {
		t.Fatalf("snapshot ownership leaked: %#v", current)
	}
}

func TestInventoryDetectorFailureIsContained(t *testing.T) {
	good := adaptertest.NewDetector("codex", "Codex", adapter.InstallationDescriptor{
		ID: "codex-fixture", State: adapter.InstallationInstalled, Process: adapter.ProcessStopped,
	})
	failed := adaptertest.NewDetector("opencode", "OpenCode", adapter.InstallationDescriptor{
		ID: "opencode-fixture", State: adapter.InstallationInstalled, Process: adapter.ProcessRunning,
	})
	failed.Error = errors.New("sensitive detector detail")
	inventory, err := adapter.NewInventory(good, failed)
	if err != nil {
		t.Fatalf("NewInventory: %v", err)
	}
	snapshot, err := inventory.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(snapshot.Installations) != 2 || snapshot.Installations[1].State != adapter.InstallationUnavailable || snapshot.Installations[1].ID != "opencode-unavailable" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestInventoryConcurrentRefreshIsCoalesced(t *testing.T) {
	detector := &blockingDetector{started: make(chan struct{}), release: make(chan struct{})}
	inventory, err := adapter.NewInventory(detector)
	if err != nil {
		t.Fatalf("NewInventory: %v", err)
	}
	results := make(chan adapter.InventorySnapshot, 2)
	go func() { value, _ := inventory.Refresh(context.Background()); results <- value }()
	<-detector.started
	go func() { value, _ := inventory.Refresh(context.Background()); results <- value }()
	time.Sleep(10 * time.Millisecond)
	close(detector.release)
	first, second := <-results, <-results
	if detector.calls.Load() != 1 || first.Generation != 1 || second.Generation != 1 {
		t.Fatalf("calls=%d first=%#v second=%#v", detector.calls.Load(), first, second)
	}
}

func TestInventoryRejectsConflictingDetectors(t *testing.T) {
	left := adaptertest.NewDetector("codex", "Codex", adapter.InstallationDescriptor{ID: "one", State: adapter.InstallationInstalled, Process: adapter.ProcessUnknown})
	right := adaptertest.NewDetector("codex", "Other", adapter.InstallationDescriptor{ID: "two", State: adapter.InstallationInstalled, Process: adapter.ProcessUnknown})
	if _, err := adapter.NewInventory(left, right); !errors.Is(err, adapter.ErrConflict) {
		t.Fatalf("error = %v", err)
	}
}

type blockingDetector struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (*blockingDetector) Agent() adapter.AgentDescriptor {
	return adapter.AgentDescriptor{Type: "synthetic", DisplayName: "Synthetic"}
}

func (d *blockingDetector) Detect(ctx context.Context) ([]adapter.InstallationDescriptor, error) {
	d.calls.Add(1)
	d.once.Do(func() { close(d.started) })
	select {
	case <-d.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []adapter.InstallationDescriptor{{
		ID: "synthetic", Agent: d.Agent(), State: adapter.InstallationInstalled, Process: adapter.ProcessStopped,
	}}, nil
}
