// Package config defines Yuanshu's versioned, local Node configuration.
package config

import platformpkg "github.com/yuanshu-ai/yuanshu/internal/platform"

const (
	CurrentVersion         = 2
	MaxFileBytes           = 1 << 20
	DefaultCodexInstanceID = "codex-default"
	DefaultIdentityKeyFile = "identity.key"
)

type TransportMode string

const (
	TransportRelay      TransportMode = "relay"
	TransportStandalone TransportMode = "standalone"
)

type PermissionProfile string

const (
	PermissionReadOnly       PermissionProfile = "read-only"
	PermissionWorkspaceWrite PermissionProfile = "workspace-write"
)

type Config struct {
	ConfigVersion  int                   `toml:"config_version" json:"config_version"`
	Host           HostConfig            `toml:"host" json:"host"`
	Transport      TransportConfig       `toml:"transport" json:"transport"`
	Relay          RelayConfig           `toml:"relay" json:"relay"`
	Identity       IdentityConfig        `toml:"identity" json:"identity"`
	AgentInstances []AgentInstanceConfig `toml:"agent_instances" json:"agent_instances"`
	Events         EventsConfig          `toml:"events" json:"events"`
	Workspaces     []WorkspaceConfig     `toml:"workspaces" json:"workspaces"`
}

type HostConfig struct {
	Name   string `toml:"name" json:"name"`
	Locale string `toml:"locale,omitempty" json:"locale,omitempty"`
}

type TransportConfig struct {
	Mode TransportMode `toml:"mode" json:"mode"`
}

type RelayConfig struct {
	URL                   string                `toml:"url" json:"url"`
	ProxyURL              string                `toml:"proxy_url" json:"proxy_url"`
	CABundleFile          string                `toml:"ca_bundle_file,omitempty" json:"ca_bundle_file,omitempty"`
	ConnectTimeoutSeconds int                   `toml:"connect_timeout_seconds" json:"connect_timeout_seconds"`
	CredentialRef         platformpkg.SecretRef `toml:"credential_ref" json:"credential_ref"`
	ProxyCredentialRef    platformpkg.SecretRef `toml:"proxy_credential_ref" json:"proxy_credential_ref"`
}

type IdentityConfig struct {
	// KeyFile is the local Node device key. It is deliberately fixed to a
	// basename so a config file cannot redirect identity material elsewhere.
	KeyFile string `toml:"key_file,omitempty" json:"key_file,omitempty"`
	// PrivateKeyRef is retained only so pre-file-identity configurations can be
	// decoded and reported as requiring repair. New code must not read it.
	PrivateKeyRef platformpkg.SecretRef `toml:"private_key_ref,omitempty" json:"private_key_ref,omitempty"`
}

type AgentRuntimeMode string

const (
	AgentRuntimeManaged      AgentRuntimeMode = "managed"
	AgentRuntimeDetectedOnly AgentRuntimeMode = "detected-only"
)

type AgentInstanceConfig struct {
	ID          string              `toml:"id" json:"id"`
	AdapterType string              `toml:"adapter_type" json:"adapter_type"`
	DisplayName string              `toml:"display_name" json:"display_name"`
	Enabled     bool                `toml:"enabled" json:"enabled"`
	IsDefault   bool                `toml:"is_default" json:"is_default"`
	RuntimeMode AgentRuntimeMode    `toml:"runtime_mode" json:"runtime_mode"`
	Codex       *CodexAdapterConfig `toml:"codex,omitempty" json:"codex,omitempty"`
}

type CodexAdapterConfig struct {
	Enabled     bool   `toml:"enabled" json:"enabled"`
	Binary      string `toml:"binary" json:"binary"`
	RuntimeMode string `toml:"runtime_mode" json:"runtime_mode"`
	Home        string `toml:"home" json:"home"`
}

type EventsConfig struct {
	MaxAgeHours int `toml:"max_age_hours" json:"max_age_hours"`
	MaxSizeMiB  int `toml:"max_size_mib" json:"max_size_mib"`
}

type WorkspaceConfig struct {
	ID                    string            `toml:"id" json:"id"`
	DisplayName           string            `toml:"display_name" json:"display_name"`
	Path                  string            `toml:"path" json:"path"`
	AllowedAgentInstances []string          `toml:"allowed_agent_instances" json:"allowed_agent_instances"`
	DefaultAgentInstance  string            `toml:"default_agent_instance" json:"default_agent_instance"`
	PermissionProfile     PermissionProfile `toml:"permission_profile" json:"permission_profile"`
	AllowNetwork          bool              `toml:"allow_network" json:"allow_network"`
}

func (c Config) AgentInstance(id string) (AgentInstanceConfig, bool) {
	for _, instance := range c.AgentInstances {
		if instance.ID == id {
			return instance, true
		}
	}
	return AgentInstanceConfig{}, false
}

func (c Config) DefaultAgent() (AgentInstanceConfig, bool) {
	for _, instance := range c.AgentInstances {
		if instance.IsDefault {
			return instance, true
		}
	}
	return AgentInstanceConfig{}, false
}

func (c Config) DefaultCodexConfig() (CodexAdapterConfig, bool) {
	instance, ok := c.DefaultAgent()
	if !ok || instance.AdapterType != "codex" || instance.Codex == nil {
		return CodexAdapterConfig{}, false
	}
	return *instance.Codex, true
}
