package adapter

import (
	"context"
	"errors"
	"sort"
	"strings"
)

type AgentDescriptor struct {
	Type        string
	DisplayName string
}

type InstanceDescriptor struct {
	ID          string
	AgentType   string
	DisplayName string
}

type RuntimeDescriptor struct {
	Agent        AgentDescriptor
	Instance     InstanceDescriptor
	Installation Installation
	Capabilities CapabilitySet
}

type Factory func() (AgentAdapter, error)

type Registration struct {
	Agent    AgentDescriptor
	Instance InstanceDescriptor
	Default  bool
	Factory  Factory
}

type Registry struct {
	agents          map[string]AgentDescriptor
	registrations   map[string]Registration
	defaultInstance string
}

type Handle struct {
	Agent    AgentDescriptor
	Instance InstanceDescriptor
	Adapter  AgentAdapter
}

func NewRegistry(registrations ...Registration) (*Registry, error) {
	registry := &Registry{
		agents:        make(map[string]AgentDescriptor),
		registrations: make(map[string]Registration),
	}
	for _, registration := range registrations {
		if !validIdentifier(registration.Agent.Type) ||
			!validIdentifier(registration.Instance.ID) ||
			registration.Instance.AgentType != registration.Agent.Type ||
			!validDisplayName(registration.Agent.DisplayName) ||
			!validDisplayName(registration.Instance.DisplayName) ||
			registration.Factory == nil {
			return nil, ErrInvalid
		}
		if existing, ok := registry.agents[registration.Agent.Type]; ok {
			if existing != registration.Agent {
				return nil, ErrConflict
			}
		} else {
			registry.agents[registration.Agent.Type] = registration.Agent
		}
		if _, ok := registry.registrations[registration.Instance.ID]; ok {
			return nil, ErrConflict
		}
		if registration.Default {
			if registry.defaultInstance != "" {
				return nil, ErrConflict
			}
			registry.defaultInstance = registration.Instance.ID
		}
		registry.registrations[registration.Instance.ID] = registration
	}
	return registry, nil
}

func (r *Registry) Agents() []AgentDescriptor {
	if r == nil {
		return nil
	}
	result := make([]AgentDescriptor, 0, len(r.agents))
	for _, descriptor := range r.agents {
		result = append(result, descriptor)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Type < result[j].Type })
	return result
}

func (r *Registry) Instances() []InstanceDescriptor {
	if r == nil {
		return nil
	}
	result := make([]InstanceDescriptor, 0, len(r.registrations))
	for _, registration := range r.registrations {
		result = append(result, registration.Instance)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (r *Registry) Create(instanceID string) (Handle, error) {
	if r == nil {
		return Handle{}, ErrInvalid
	}
	registration, ok := r.registrations[instanceID]
	if !ok {
		return Handle{}, ErrNotFound
	}
	created, err := registration.Factory()
	if err != nil {
		return Handle{}, normalizeFactoryError(err)
	}
	if created == nil || created.ID() != registration.Agent.Type {
		return Handle{}, ErrInvalid
	}
	return Handle{Agent: registration.Agent, Instance: registration.Instance, Adapter: created}, nil
}

func (r *Registry) CreateDefault() (Handle, error) {
	if r == nil {
		return Handle{}, ErrInvalid
	}
	if r.defaultInstance == "" {
		return Handle{}, ErrNotFound
	}
	return r.Create(r.defaultInstance)
}

func (h Handle) Detect(ctx context.Context) (RuntimeDescriptor, error) {
	if h.Adapter == nil || h.Adapter.ID() != h.Agent.Type || h.Instance.AgentType != h.Agent.Type {
		return RuntimeDescriptor{}, ErrInvalid
	}
	if ctx == nil {
		return RuntimeDescriptor{}, context.Canceled
	}
	installation, err := h.Adapter.Detect(ctx)
	if err != nil {
		return RuntimeDescriptor{}, err
	}
	return RuntimeDescriptor{
		Agent: h.Agent, Instance: h.Instance, Installation: installation,
		Capabilities: h.Adapter.Capabilities(),
	}, nil
}

func validIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			continue
		}
		if character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validDisplayName(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 128 &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func normalizeFactoryError(err error) error {
	for _, candidate := range []error{
		ErrInvalid, ErrUnavailable, ErrUnsupported, ErrNotFound, ErrConflict,
		ErrForbidden, ErrReconciliationNeeded, ErrAmbiguous, ErrClosed,
	} {
		if errors.Is(err, candidate) {
			return candidate
		}
	}
	return ErrUnavailable
}
