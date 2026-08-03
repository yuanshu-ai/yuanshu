package node

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/transport"
)

func TestRelaySupervisorBackoffUsesBoundedJitter(t *testing.T) {
	if got := nextRelayInterval(time.Second, 30*time.Second); got != 2*time.Second {
		t.Fatalf("nextRelayInterval() = %s, want 2s", got)
	}
	if got := nextRelayInterval(16*time.Second, 30*time.Second); got != 30*time.Second {
		t.Fatalf("nextRelayInterval() = %s, want 30s", got)
	}
	if got := jitteredRelayInterval(time.Second, func() float64 { return 0 }); got != 800*time.Millisecond {
		t.Fatalf("minimum jitter = %s, want 800ms", got)
	}
	if got := jitteredRelayInterval(time.Second, func() float64 { return 1 }); got != 1200*time.Millisecond {
		t.Fatalf("maximum jitter = %s, want 1.2s", got)
	}
}
func TestRelaySupervisorRetriesTransientConnectionFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	connected := make(chan struct{}, 1)
	supervisor, err := newRelaySupervisor(ctx, relaySupervisorOptions{
		Initial: 5 * time.Millisecond, Maximum: 10 * time.Millisecond, StableFor: time.Second,
		Random: func() float64 { return 0.5 },
		Connect: func(context.Context) (transport.Transport, error) {
			if attempts.Add(1) == 1 {
				return nil, errors.New("temporary relay failure")
			}
			_, endpoint, err := transport.NewStandalonePair(transport.StandaloneOptions{QueueCapacity: 1})
			if err == nil {
				select {
				case connected <- struct{}{}:
				default:
				}
			}
			return endpoint, err
		},
		Serve: func(ctx context.Context, endpoint transport.Transport) error {
			<-ctx.Done()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	supervisor.Start()
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not retry after a transient failure")
	}
	supervisor.Close()
	if got := attempts.Load(); got < 2 {
		t.Fatalf("connection attempts = %d, want at least 2", got)
	}
}
