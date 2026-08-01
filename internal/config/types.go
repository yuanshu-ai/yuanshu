// Package config defines Yuanshu's versioned, local Node configuration.
package config

import platformpkg "github.com/yuanshu-ai/yuanshu/internal/platform"

const (
	CurrentVersion = 1
	MaxFileBytes   = 1 << 20
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
	ConfigVersion int               `toml:"config_version" json:"config_version"`
	Host          HostConfig        `toml:"host" json:"host"`
	Transport     TransportConfig   `toml:"transport" json:"transport"`
	Relay         RelayConfig       `toml:"relay" json:"relay"`
	Identity      IdentityConfig    `toml:"identity" json:"identity"`
	Adapters      AdaptersConfig    `toml:"adapters" json:"adapters"`
	Events        EventsConfig      `toml:"events" json:"events"`
	Workspaces    []WorkspaceConfig `toml:"workspaces" json:"workspaces"`
}

type HostConfig struct {
	Name string `toml:"name" json:"name"`
}

type TransportConfig struct {
	Mode TransportMode `toml:"mode" json:"mode"`
}

type RelayConfig struct {
	URL                   string                `toml:"url" json:"url"`
	ProxyURL              string                `toml:"proxy_url" json:"proxy_url"`
	ConnectTimeoutSeconds int                   `toml:"connect_timeout_seconds" json:"connect_timeout_seconds"`
	CredentialRef         platformpkg.SecretRef `toml:"credential_ref" json:"credential_ref"`
	ProxyCredentialRef    platformpkg.SecretRef `toml:"proxy_credential_ref" json:"proxy_credential_ref"`
}

type IdentityConfig struct {
	PrivateKeyRef platformpkg.SecretRef `toml:"private_key_ref" json:"private_key_ref"`
}

type AdaptersConfig struct {
	Codex CodexAdapterConfig `toml:"codex" json:"codex"`
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
	ID                string            `toml:"id" json:"id"`
	DisplayName       string            `toml:"display_name" json:"display_name"`
	Path              string            `toml:"path" json:"path"`
	AllowedAdapters   []string          `toml:"allowed_adapters" json:"allowed_adapters"`
	DefaultAdapter    string            `toml:"default_adapter" json:"default_adapter"`
	PermissionProfile PermissionProfile `toml:"permission_profile" json:"permission_profile"`
	AllowNetwork      bool              `toml:"allow_network" json:"allow_network"`
}
