package node

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/transport"
)

const (
	relayStateConnecting   = "connecting"
	relayStateOnline       = "online"
	relayStateReconnecting = "reconnecting"
	relayStateRevoked      = "revoked"
	relayStateStopped      = "stopped"

	relayReconnectInitial = time.Second
	relayReconnectMax     = 30 * time.Second
	relayStableWindow     = 5 * time.Second
)

type relaySupervisorOptions struct {
	Connect   func(context.Context) (transport.Transport, error)
	Serve     func(context.Context, transport.Transport) error
	OnState   func(string)
	Random    func() float64
	Clock     func() time.Time
	Initial   time.Duration
	Maximum   time.Duration
	StableFor time.Duration
}

// relaySupervisor owns connection attempts, while ControlSession owns the
// Runtime event pump and the protocol state shared by every connection.
type relaySupervisor struct {
	connect func(context.Context) (transport.Transport, error)
	serve   func(context.Context, transport.Transport) error
	onState func(string)
	random  func() float64
	clock   func() time.Time
	initial time.Duration
	maximum time.Duration
	stable  time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{}

	mu      sync.Mutex
	active  *relayBinding
	started bool
	closed  bool
}

type relayBinding struct {
	value transport.Transport
}

func newRelaySupervisor(parent context.Context, options relaySupervisorOptions) (*relaySupervisor, error) {
	if parent == nil || options.Connect == nil || options.Serve == nil {
		return nil, errors.New("relay supervisor configuration is invalid")
	}
	initial := options.Initial
	if initial <= 0 {
		initial = relayReconnectInitial
	}
	maximum := options.Maximum
	if maximum <= 0 {
		maximum = relayReconnectMax
	}
	stable := options.StableFor
	if stable <= 0 {
		stable = relayStableWindow
	}
	random := options.Random
	if random == nil {
		source := rand.NewSource(time.Now().UnixNano())
		generator := rand.New(source)
		random = generator.Float64
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	ctx, cancel := context.WithCancel(parent)
	return &relaySupervisor{
		connect: options.Connect, serve: options.Serve, onState: options.OnState,
		random: random, clock: clock, initial: initial, maximum: maximum, stable: stable,
		ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1),
	}, nil
}

func (s *relaySupervisor) Start() {
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	go s.run()
}

func (s *relaySupervisor) run() {
	defer close(s.done)
	interval := s.initial
	firstAttempt := true
	for {
		if firstAttempt {
			s.state(relayStateConnecting)
			firstAttempt = false
		} else {
			s.state(relayStateReconnecting)
		}
		connection, err := s.connect(s.ctx)
		if s.ctx.Err() != nil {
			return
		}
		if err != nil || connection == nil {
			if errors.Is(err, errRelayRevoked) {
				s.state(relayStateRevoked)
				return
			}
			if !s.wait(interval) {
				return
			}
			interval = nextRelayInterval(interval, s.maximum)
			continue
		}

		binding := s.setActive(connection)
		s.state(relayStateOnline)
		connectedAt := s.clock()
		_ = s.serve(s.ctx, connection)
		s.clearActive(binding)
		_ = connection.Close()
		if s.ctx.Err() != nil {
			return
		}
		if s.clock().Sub(connectedAt) >= s.stable {
			interval = s.initial
		}
		if !s.wait(interval) {
			return
		}
		interval = nextRelayInterval(interval, s.maximum)
	}
}

func (s *relaySupervisor) state(value string) {
	if s.onState != nil {
		s.onState(value)
	}
}

func (s *relaySupervisor) setActive(value transport.Transport) *relayBinding {
	binding := &relayBinding{value: value}
	s.mu.Lock()
	s.active = binding
	s.mu.Unlock()
	return binding
}

func (s *relaySupervisor) clearActive(binding *relayBinding) {
	s.mu.Lock()
	if s.active == binding {
		s.active = nil
	}
	s.mu.Unlock()
}

func (s *relaySupervisor) closeActive() {
	s.mu.Lock()
	var active transport.Transport
	if s.active != nil {
		active = s.active.value
	}
	s.mu.Unlock()
	if active != nil {
		_ = active.Close()
	}
}

// Reconnect interrupts the current connection and wakes a pending backoff.
// The next Connect call reads the credential currently held by pairingManager.
func (s *relaySupervisor) Reconnect() {
	s.closeActive()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *relaySupervisor) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	started := s.started
	s.mu.Unlock()
	s.cancel()
	s.closeActive()
	if started {
		<-s.done
	}
	s.state(relayStateStopped)
}

func (s *relaySupervisor) wait(interval time.Duration) bool {
	delay := jitteredRelayInterval(interval, s.random)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return false
	case <-s.wake:
		return true
	case <-timer.C:
		return true
	}
}

func nextRelayInterval(current, maximum time.Duration) time.Duration {
	if current >= maximum/2 {
		return maximum
	}
	return current * 2
}

func jitteredRelayInterval(interval time.Duration, random func() float64) time.Duration {
	if interval <= 0 {
		return 0
	}
	value := random()
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	// Symmetric 20% jitter keeps retries from synchronizing while preserving
	// the configured interval as the center of the distribution.
	factor := 0.8 + value*0.4
	return time.Duration(float64(interval) * factor)
}
