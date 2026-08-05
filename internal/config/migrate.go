package config

import (
	"bytes"

	"github.com/pelletier/go-toml/v2"
)

type legacyConfigV1 struct {
	ConfigVersion int                     `toml:"config_version"`
	Host          HostConfig              `toml:"host"`
	Transport     TransportConfig         `toml:"transport"`
	Relay         RelayConfig             `toml:"relay"`
	Identity      IdentityConfig          `toml:"identity"`
	Adapters      legacyAdaptersConfigV1  `toml:"adapters"`
	Events        EventsConfig            `toml:"events"`
	Workspaces    []legacyWorkspaceConfig `toml:"workspaces"`
}

type legacyAdaptersConfigV1 struct {
	Codex CodexAdapterConfig `toml:"codex"`
}

type legacyWorkspaceConfig struct {
	ID                string            `toml:"id"`
	DisplayName       string            `toml:"display_name"`
	Path              string            `toml:"path"`
	AllowedAdapters   []string          `toml:"allowed_adapters"`
	DefaultAdapter    string            `toml:"default_adapter"`
	PermissionProfile PermissionProfile `toml:"permission_profile"`
	AllowNetwork      bool              `toml:"allow_network"`
}

func migrateV1(raw []byte) ([]byte, error) {
	var legacy legacyConfigV1
	decoder := toml.NewDecoder(bytes.NewReader(raw)).DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return nil, err
	}
	workspaces := make([]WorkspaceConfig, 0, len(legacy.Workspaces))
	for _, workspace := range legacy.Workspaces {
		if len(workspace.AllowedAdapters) != 1 || workspace.AllowedAdapters[0] != "codex" || workspace.DefaultAdapter != "codex" {
			return nil, ErrInvalid
		}
		workspaces = append(workspaces, WorkspaceConfig{
			ID: workspace.ID, DisplayName: workspace.DisplayName, Path: workspace.Path,
			AllowedAgentInstances: []string{DefaultCodexInstanceID}, DefaultAgentInstance: DefaultCodexInstanceID,
			PermissionProfile: workspace.PermissionProfile, AllowNetwork: workspace.AllowNetwork,
		})
	}
	codex := legacy.Adapters.Codex
	normalized := Config{
		ConfigVersion: CurrentVersion,
		Host:          legacy.Host, Transport: legacy.Transport, Relay: legacy.Relay, Identity: legacy.Identity,
		AgentInstances: []AgentInstanceConfig{{
			ID: DefaultCodexInstanceID, AdapterType: "codex", DisplayName: "Codex",
			Enabled: codex.Enabled, IsDefault: true, RuntimeMode: AgentRuntimeManaged, Codex: &codex,
		}},
		Events: legacy.Events, Workspaces: workspaces,
	}
	if err := Validate(normalized); err != nil {
		return nil, err
	}
	return toml.Marshal(normalized)
}
