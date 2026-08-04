package runtime_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/adapter/adaptertest"
	noderuntime "github.com/yuanshu-ai/yuanshu/internal/node/runtime"
)

func TestManagerIsolatesRuntimeEventsAndBackpressure(t *testing.T) {
	manager, err := noderuntime.NewManager(noderuntime.Options{EventCapacity: 1, CloseTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	firstRuntime, secondRuntime := adaptertest.NewRuntime(), adaptertest.NewRuntime()
	first := openRuntime(t, manager, "codex-one", firstRuntime)
	second := openRuntime(t, manager, "codex-two", secondRuntime)

	if !firstRuntime.Emit(adapter.AgentEvent{ThreadID: "first-1"}) || !firstRuntime.Emit(adapter.AgentEvent{ThreadID: "first-2"}) {
		t.Fatal("first runtime did not accept events")
	}
	deadline := time.Now().Add(time.Second)
	for first.Health().FailureCode != "event_backpressure" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if first.Health().FailureCode != "event_backpressure" || !firstRuntime.Closed() {
		t.Fatalf("first health=%#v closed=%v", first.Health(), firstRuntime.Closed())
	}
	if !secondRuntime.Emit(adapter.AgentEvent{ThreadID: "second"}) {
		t.Fatal("second runtime was closed by first runtime failure")
	}
	select {
	case event := <-second.Events():
		if event.ThreadID != "second" {
			t.Fatalf("event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("second runtime event was blocked")
	}
	if second.Health().State != "ready" {
		t.Fatalf("second health = %#v", second.Health())
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestManagerConcurrentOpenAndFactorySanitization(t *testing.T) {
	manager, _ := noderuntime.NewManager(noderuntime.Options{})
	key := noderuntime.RuntimeKey{InstanceID: "codex-default", EndpointID: "codex-default-managed"}
	var calls atomic.Int32
	start := make(chan struct{})
	factory := func(context.Context) (adapter.Runtime, error) {
		calls.Add(1)
		<-start
		return adaptertest.NewRuntime(), nil
	}
	errorsChannel := make(chan error, 2)
	go func() {
		_, err := manager.Open(context.Background(), noderuntime.OpenRequest{Key: key, Mode: adapter.RuntimeManaged, Factory: factory})
		errorsChannel <- err
	}()
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	go func() {
		_, err := manager.Open(context.Background(), noderuntime.OpenRequest{Key: key, Mode: adapter.RuntimeManaged, Factory: factory})
		errorsChannel <- err
	}()
	close(start)
	first, second := <-errorsChannel, <-errorsChannel
	if !((first == nil && errors.Is(second, adapter.ErrConflict)) || (second == nil && errors.Is(first, adapter.ErrConflict))) || calls.Load() != 1 {
		t.Fatalf("first=%v second=%v calls=%d", first, second, calls.Load())
	}
	_, err := manager.Open(context.Background(), noderuntime.OpenRequest{
		Key: noderuntime.RuntimeKey{InstanceID: "other", EndpointID: "other-managed"}, Mode: adapter.RuntimeManaged,
		Factory: func(context.Context) (adapter.Runtime, error) { return nil, errors.New("secret path canary") },
	})
	if !errors.Is(err, adapter.ErrUnavailable) || err.Error() != adapter.ErrUnavailable.Error() {
		t.Fatalf("factory error = %v", err)
	}
	_ = manager.Close(context.Background())
}

func TestManagerClosesInReverseOpenOrder(t *testing.T) {
	manager, _ := noderuntime.NewManager(noderuntime.Options{})
	var mu sync.Mutex
	order := make([]string, 0, 2)
	for _, id := range []string{"first", "second"} {
		value := adaptertest.NewRuntime()
		captured := id
		value.CloseHook = func() { mu.Lock(); order = append(order, captured); mu.Unlock() }
		openRuntime(t, manager, id, value)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "second" || order[1] != "first" {
		t.Fatalf("close order = %#v", order)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
}

func TestManagerRejectsUnsupportedModes(t *testing.T) {
	manager, _ := noderuntime.NewManager(noderuntime.Options{})
	for _, mode := range []adapter.RuntimeMode{adapter.RuntimeHistoryOnly, adapter.RuntimeDetectedOnly} {
		_, err := manager.Open(context.Background(), noderuntime.OpenRequest{
			Key: noderuntime.RuntimeKey{InstanceID: "codex-default", EndpointID: "codex-default-managed"}, Mode: mode,
			Factory: func(context.Context) (adapter.Runtime, error) { return adaptertest.NewRuntime(), nil },
		})
		if !errors.Is(err, adapter.ErrUnsupported) {
			t.Fatalf("mode %q error=%v", mode, err)
		}
	}
}

func openRuntime(t *testing.T, manager *noderuntime.Manager, id string, value adapter.Runtime) noderuntime.Handle {
	t.Helper()
	handle, err := manager.Open(context.Background(), noderuntime.OpenRequest{
		Key: noderuntime.RuntimeKey{InstanceID: id, EndpointID: id + "-managed"}, Mode: adapter.RuntimeManaged,
		Factory: func(context.Context) (adapter.Runtime, error) { return value, nil },
	})
	if err != nil {
		t.Fatalf("Open(%s): %v", id, err)
	}
	return handle
}
