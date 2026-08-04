package adapter_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/adapter/adaptertest"
)

func TestRegistryRejectsInvalidAndConflictingRegistrations(t *testing.T) {
	valid := registration("synthetic", "synthetic-default", true, func() (adapter.AgentAdapter, error) {
		return adaptertest.New("synthetic"), nil
	})
	tests := []struct {
		name          string
		registrations []adapter.Registration
		want          error
	}{
		{name: "invalid agent ID", registrations: []adapter.Registration{{
			Agent:    adapter.AgentDescriptor{Type: "Synthetic", DisplayName: "Synthetic"},
			Instance: valid.Instance, Factory: valid.Factory,
		}}, want: adapter.ErrInvalid},
		{name: "invalid instance ID", registrations: []adapter.Registration{{
			Agent:    valid.Agent,
			Instance: adapter.InstanceDescriptor{ID: "bad instance", AgentType: "synthetic", DisplayName: "Synthetic"},
			Factory:  valid.Factory,
		}}, want: adapter.ErrInvalid},
		{name: "agent mismatch", registrations: []adapter.Registration{{
			Agent:    valid.Agent,
			Instance: adapter.InstanceDescriptor{ID: "synthetic-default", AgentType: "other", DisplayName: "Synthetic"},
			Factory:  valid.Factory,
		}}, want: adapter.ErrInvalid},
		{name: "nil factory", registrations: []adapter.Registration{{
			Agent: valid.Agent, Instance: valid.Instance,
		}}, want: adapter.ErrInvalid},
		{name: "duplicate instance", registrations: []adapter.Registration{valid, valid}, want: adapter.ErrConflict},
		{name: "conflicting agent descriptor", registrations: []adapter.Registration{
			valid,
			registrationWithAgent(adapter.AgentDescriptor{Type: "synthetic", DisplayName: "Changed"}, "synthetic-other", false),
		}, want: adapter.ErrConflict},
		{name: "multiple defaults", registrations: []adapter.Registration{
			valid, registration("other", "other-default", true, func() (adapter.AgentAdapter, error) {
				return adaptertest.New("other"), nil
			}),
		}, want: adapter.ErrConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := adapter.NewRegistry(test.registrations...); !errors.Is(err, test.want) {
				t.Fatalf("NewRegistry() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRegistryListsStableCopiesAndCreatesDefault(t *testing.T) {
	registry, err := adapter.NewRegistry(
		registration("zeta", "zeta-default", false, func() (adapter.AgentAdapter, error) {
			return adaptertest.New("zeta"), nil
		}),
		registration("alpha", "alpha-default", true, func() (adapter.AgentAdapter, error) {
			return adaptertest.New("alpha"), nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	agents := registry.Agents()
	instances := registry.Instances()
	if len(agents) != 2 || agents[0].Type != "alpha" || agents[1].Type != "zeta" {
		t.Fatalf("Agents() = %+v", agents)
	}
	if len(instances) != 2 || instances[0].ID != "alpha-default" || instances[1].ID != "zeta-default" {
		t.Fatalf("Instances() = %+v", instances)
	}
	agents[0].Type = "mutated"
	instances[0].ID = "mutated"
	if registry.Agents()[0].Type != "alpha" || registry.Instances()[0].ID != "alpha-default" {
		t.Fatal("descriptor output mutated Registry state")
	}
	handle, err := registry.CreateDefault()
	if err != nil || handle.Agent.Type != "alpha" || handle.Instance.ID != "alpha-default" || handle.Adapter.ID() != "alpha" {
		t.Fatalf("CreateDefault() = %+v, %v", handle, err)
	}
	descriptor, err := handle.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Agent != handle.Agent || descriptor.Instance != handle.Instance ||
		descriptor.Installation.Version != "synthetic-1" || !descriptor.Capabilities.ThreadRead {
		t.Fatalf("Detect() = %+v", descriptor)
	}
}

func TestRegistryCreateErrorsAreStableAndRedacted(t *testing.T) {
	tests := []struct {
		name    string
		factory adapter.Factory
		want    error
	}{
		{name: "stable", factory: func() (adapter.AgentAdapter, error) {
			return nil, errors.Join(adapter.ErrForbidden, errors.New("credential-canary"))
		}, want: adapter.ErrForbidden},
		{name: "unknown", factory: func() (adapter.AgentAdapter, error) {
			return nil, errors.New("credential-canary")
		}, want: adapter.ErrUnavailable},
		{name: "nil adapter", factory: func() (adapter.AgentAdapter, error) { return nil, nil }, want: adapter.ErrInvalid},
		{name: "mismatched adapter", factory: func() (adapter.AgentAdapter, error) {
			return adaptertest.New("other"), nil
		}, want: adapter.ErrInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry, err := adapter.NewRegistry(registration("synthetic", "synthetic-default", true, test.factory))
			if err != nil {
				t.Fatal(err)
			}
			_, err = registry.CreateDefault()
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "credential-canary") {
				t.Fatalf("CreateDefault() error = %v, want redacted %v", err, test.want)
			}
		})
	}
	registry, err := adapter.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Create("missing"); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("Create(missing) error = %v", err)
	}
	if _, err := registry.CreateDefault(); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("CreateDefault() error = %v", err)
	}
}

func TestRegistryConcurrentCreateAndDetect(t *testing.T) {
	registry, err := adapter.NewRegistry(registration("synthetic", "synthetic-default", true, func() (adapter.AgentAdapter, error) {
		return adaptertest.New("synthetic"), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errorsFound := make(chan error, 64)
	for range 64 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			handle, createErr := registry.CreateDefault()
			if createErr != nil {
				errorsFound <- createErr
				return
			}
			if _, detectErr := handle.Detect(context.Background()); detectErr != nil {
				errorsFound <- detectErr
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}

func registration(agentType, instanceID string, isDefault bool, factory adapter.Factory) adapter.Registration {
	return adapter.Registration{
		Agent: adapter.AgentDescriptor{Type: agentType, DisplayName: agentType},
		Instance: adapter.InstanceDescriptor{
			ID: instanceID, AgentType: agentType, DisplayName: agentType,
		},
		Default: isDefault, Factory: factory,
	}
}

func registrationWithAgent(agent adapter.AgentDescriptor, instanceID string, isDefault bool) adapter.Registration {
	return adapter.Registration{
		Agent: agent,
		Instance: adapter.InstanceDescriptor{
			ID: instanceID, AgentType: agent.Type, DisplayName: agent.DisplayName,
		},
		Default: isDefault,
		Factory: func() (adapter.AgentAdapter, error) { return adaptertest.New(agent.Type), nil },
	}
}
