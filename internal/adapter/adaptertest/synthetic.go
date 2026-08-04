// Package adaptertest provides reusable synthetic Agent implementations for
// contract tests. Production composition must not register these adapters.
package adaptertest

import (
	"context"
	"sync"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
)

type Adapter struct {
	IDValue          string
	Installation     adapter.Installation
	CapabilityValues adapter.CapabilitySet
	Runtime          adapter.Runtime
	DetectError      error
	StartError       error
}

type Detector struct {
	AgentValue adapter.AgentDescriptor
	Items      []adapter.InstallationDescriptor
	Error      error
}

var _ adapter.Detector = (*Detector)(nil)

func NewDetector(agentType, displayName string, items ...adapter.InstallationDescriptor) *Detector {
	agent := adapter.AgentDescriptor{Type: agentType, DisplayName: displayName}
	copyItems := append([]adapter.InstallationDescriptor(nil), items...)
	for index := range copyItems {
		copyItems[index].Agent = agent
	}
	return &Detector{AgentValue: agent, Items: copyItems}
}

func (d *Detector) Agent() adapter.AgentDescriptor { return d.AgentValue }

func (d *Detector) Detect(context.Context) ([]adapter.InstallationDescriptor, error) {
	if d.Error != nil {
		return nil, d.Error
	}
	return append([]adapter.InstallationDescriptor(nil), d.Items...), nil
}

var _ adapter.AgentAdapter = (*Adapter)(nil)

func New(id string) *Adapter {
	return &Adapter{
		IDValue: id,
		Installation: adapter.Installation{
			Detected: true, Version: "synthetic-1", Protocol: "synthetic-v1",
			Compatibility: adapter.CompatibilityKnown,
		},
		CapabilityValues: adapter.CapabilitySet{ThreadList: true, ThreadRead: true},
		Runtime:          NewRuntime(),
	}
}

func (a *Adapter) ID() string { return a.IDValue }

func (a *Adapter) Detect(context.Context) (adapter.Installation, error) {
	if a.DetectError != nil {
		return adapter.Installation{}, a.DetectError
	}
	return a.Installation, nil
}

func (a *Adapter) Capabilities() adapter.CapabilitySet { return a.CapabilityValues }

func (a *Adapter) StartRuntime(context.Context) (adapter.Runtime, error) {
	if a.StartError != nil {
		return nil, a.StartError
	}
	return a.Runtime, nil
}

type Runtime struct {
	mu             sync.Mutex
	OperationError error
	HealthValue    adapter.HealthStatus
	events         chan adapter.AgentEvent
	closed         bool
}

var _ adapter.Runtime = (*Runtime)(nil)

func NewRuntime() *Runtime {
	return &Runtime{
		HealthValue: adapter.HealthStatus{State: "ready"},
		events:      make(chan adapter.AgentEvent),
	}
}

func (r *Runtime) ListThreads(context.Context, adapter.ListThreadsRequest) (adapter.ThreadPage, error) {
	return adapter.ThreadPage{}, r.OperationError
}

func (r *Runtime) ReadThread(context.Context, adapter.ReadThreadRequest) (adapter.ThreadSnapshot, error) {
	return adapter.ThreadSnapshot{}, r.OperationError
}

func (r *Runtime) StartThread(context.Context, adapter.StartThreadRequest) (adapter.Thread, error) {
	return adapter.Thread{}, r.OperationError
}

func (r *Runtime) ResumeThread(context.Context, adapter.ResumeThreadRequest) (adapter.Thread, error) {
	return adapter.Thread{}, r.OperationError
}

func (r *Runtime) StartTurn(context.Context, adapter.StartTurnRequest) (adapter.Turn, error) {
	return adapter.Turn{}, r.OperationError
}

func (r *Runtime) SteerTurn(context.Context, adapter.SteerTurnRequest) error {
	return r.OperationError
}

func (r *Runtime) InterruptTurn(context.Context, adapter.InterruptTurnRequest) error {
	return r.OperationError
}

func (r *Runtime) ResolveApproval(context.Context, adapter.ApprovalDecision) error {
	return r.OperationError
}

func (r *Runtime) Events() <-chan adapter.AgentEvent { return r.events }

func (r *Runtime) Health() adapter.HealthStatus { return r.HealthValue }

func (r *Runtime) Close(context.Context) error {
	r.mu.Lock()
	if !r.closed {
		close(r.events)
		r.closed = true
	}
	r.mu.Unlock()
	return nil
}
