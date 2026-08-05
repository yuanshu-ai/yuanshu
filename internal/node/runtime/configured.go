package runtime

import (
	"context"
	"errors"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/adapter/builtin"
	"github.com/yuanshu-ai/yuanshu/internal/config"
)

// OpenConfiguredManaged starts every enabled managed Agent instance. A failure
// in a non-default instance is isolated; the default instance remains the
// availability boundary for the Node remote-control service.
func OpenConfiguredManaged(ctx context.Context, manager *Manager, registry *adapter.Registry, value config.Config) ([]Source, string, error) {
	if ctx == nil {
		return nil, "", context.Canceled
	}
	if manager == nil || registry == nil {
		return nil, "", adapter.ErrInvalid
	}
	defaultAgent, ok := value.DefaultAgent()
	if !ok {
		return nil, "", adapter.ErrNotFound
	}
	sources := make([]Source, 0, len(value.AgentInstances))
	for _, configured := range value.AgentInstances {
		if !configured.Enabled || configured.RuntimeMode != config.AgentRuntimeManaged {
			continue
		}
		handle, err := registry.Create(configured.ID)
		if err == nil {
			_, err = handle.Detect(ctx)
		}
		if err != nil {
			if configured.IsDefault {
				return nil, "", err
			}
			continue
		}
		key := RuntimeKey{InstanceID: configured.ID, EndpointID: builtin.ManagedEndpointID(configured.ID)}
		runtime, err := manager.Open(ctx, OpenRequest{Key: key, Mode: adapter.RuntimeManaged, Factory: handle.Adapter.StartRuntime})
		if err != nil {
			if configured.IsDefault {
				return nil, "", err
			}
			continue
		}
		sources = append(sources, Source{Key: key, Runtime: runtime})
	}
	for _, source := range sources {
		if source.Key.InstanceID == defaultAgent.ID {
			return sources, defaultAgent.ID, nil
		}
	}
	return nil, "", errors.Join(adapter.ErrUnavailable, errors.New("default Agent runtime did not start"))
}
