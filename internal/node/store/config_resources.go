package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"

	"github.com/yuanshu-ai/yuanshu/internal/config"
)

func (s *Store) ReconcileConfiguredAgents(ctx context.Context, value config.Config) error {
	instances := make([]AgentInstanceRecord, 0, len(value.AgentInstances))
	endpoints := make([]RuntimeEndpointRecord, 0, len(value.AgentInstances))
	for _, item := range value.AgentInstances {
		encoded, err := json.Marshal(item)
		if err != nil {
			return ErrInvalid
		}
		digest := sha256.Sum256(encoded)
		instances = append(instances, AgentInstanceRecord{
			InstanceID: item.ID, AdapterType: item.AdapterType, DisplayName: item.DisplayName,
			Enabled: item.Enabled, Default: item.IsDefault, RuntimeMode: string(item.RuntimeMode),
			ConfigRevision: base64.RawURLEncoding.EncodeToString(digest[:]),
		})
		ownership := EndpointOwnerExternal
		if item.RuntimeMode == config.AgentRuntimeManaged {
			ownership = EndpointOwnerNode
		}
		endpoints = append(endpoints, RuntimeEndpointRecord{
			EndpointID: item.ID + "-" + string(item.RuntimeMode), InstanceID: item.ID,
			Mode: string(item.RuntimeMode), Ownership: ownership,
		})
	}
	links := make([]WorkspaceAgentRecord, 0)
	for _, workspace := range value.Workspaces {
		for _, instanceID := range workspace.AllowedAgentInstances {
			links = append(links, WorkspaceAgentRecord{WorkspaceID: workspace.ID, InstanceID: instanceID, Default: instanceID == workspace.DefaultAgentInstance})
		}
	}
	return s.ReplaceAgentResources(ctx, instances, endpoints, links)
}
