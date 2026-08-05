package config

import (
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxHostNameBytes  = 128
	maxIDBytes        = 128
	maxDisplayBytes   = 128
	maxPathBytes      = 4096
	maxSecretRefBytes = 256
)

func Validate(value Config) error {
	if value.ConfigVersion != CurrentVersion {
		if value.ConfigVersion > CurrentVersion {
			return configError("validation", ErrUnsupportedVersion)
		}
		return configError("validation", ErrInvalid)
	}
	if err := validateSchema(value); err != nil {
		return err
	}
	if !validText(value.Host.Name, true, maxHostNameBytes) {
		return configError("validation", ErrInvalid)
	}
	if value.Host.Locale != "" && value.Host.Locale != "zh-CN" && value.Host.Locale != "en-US" {
		return configError("validation", ErrInvalid)
	}
	switch value.Transport.Mode {
	case TransportRelay, TransportStandalone:
	default:
		return configError("validation", ErrInvalid)
	}
	if value.Transport.Mode == TransportRelay && value.Relay.URL == "" {
		return configError("validation", ErrInvalid)
	}
	if value.Relay.URL != "" && !validRelayURL(value.Relay.URL) {
		return configError("validation", ErrInvalid)
	}
	if value.Relay.CABundleFile != "" && (!filepath.IsAbs(value.Relay.CABundleFile) || !validText(value.Relay.CABundleFile, true, maxPathBytes)) {
		return configError("validation", ErrInvalid)
	}
	if value.Relay.ProxyURL != "" && !validURL(value.Relay.ProxyURL, map[string]bool{
		"http": true, "https": true, "socks5": true,
	}) {
		return configError("validation", ErrInvalid)
	}
	if value.Relay.ConnectTimeoutSeconds < 1 || value.Relay.ConnectTimeoutSeconds > 120 {
		return configError("validation", ErrInvalid)
	}
	if !validSecretRef(value.Relay.CredentialRef) ||
		!validSecretRef(value.Relay.ProxyCredentialRef) ||
		!validSecretRef(value.Identity.PrivateKeyRef) {
		return configError("validation", ErrInvalid)
	}
	instances, ok := validAgentInstances(value.AgentInstances)
	if !ok {
		return configError("validation", ErrInvalid)
	}
	if value.Events.MaxAgeHours < 1 || value.Events.MaxAgeHours > 720 ||
		value.Events.MaxSizeMiB < 16 || value.Events.MaxSizeMiB > 4096 {
		return configError("validation", ErrInvalid)
	}
	if value.Workspaces == nil {
		return configError("validation", ErrInvalid)
	}
	if !validWorkspaces(value.Workspaces, instances) {
		return configError("validation", ErrInvalid)
	}
	return nil
}

func validRelayURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme == "wss" {
		return true
	}
	host := parsed.Hostname()
	return parsed.Scheme == "ws" && (host == "127.0.0.1" || host == "::1")
}

func validAgentInstances(instances []AgentInstanceConfig) (map[string]AgentInstanceConfig, bool) {
	if len(instances) == 0 {
		return nil, false
	}
	result := make(map[string]AgentInstanceConfig, len(instances))
	defaults := 0
	for _, instance := range instances {
		if !validText(instance.ID, true, maxIDBytes) || !validText(instance.DisplayName, true, maxDisplayBytes) {
			return nil, false
		}
		if _, exists := result[instance.ID]; exists {
			return nil, false
		}
		if instance.IsDefault {
			defaults++
			if !instance.Enabled || instance.RuntimeMode != AgentRuntimeManaged {
				return nil, false
			}
		}
		switch instance.AdapterType {
		case "codex":
			if instance.RuntimeMode != AgentRuntimeManaged || instance.Codex == nil || instance.Codex.Enabled != instance.Enabled || !validCodexConfig(*instance.Codex) {
				return nil, false
			}
		case "claude-code", "opencode":
			if instance.RuntimeMode != AgentRuntimeDetectedOnly || instance.Codex != nil || instance.IsDefault {
				return nil, false
			}
		default:
			return nil, false
		}
		result[instance.ID] = instance
	}
	return result, defaults == 1
}

func validCodexConfig(value CodexAdapterConfig) bool {
	if value.RuntimeMode != "stdio" {
		return false
	}
	if value.Enabled && !validText(value.Binary, true, maxPathBytes) {
		return false
	}
	if value.Binary != "" && !validText(value.Binary, true, maxPathBytes) {
		return false
	}
	return value.Home == "" || validText(value.Home, false, maxPathBytes)
}

func validWorkspaces(workspaces []WorkspaceConfig, instances map[string]AgentInstanceConfig) bool {
	ids := make(map[string]struct{}, len(workspaces))
	paths := make(map[string]struct{}, len(workspaces))
	for _, workspace := range workspaces {
		if !validText(workspace.ID, true, maxIDBytes) ||
			!validText(workspace.DisplayName, true, maxDisplayBytes) ||
			!validText(workspace.Path, true, maxPathBytes) {
			return false
		}
		if _, ok := ids[workspace.ID]; ok {
			return false
		}
		ids[workspace.ID] = struct{}{}
		if _, ok := paths[workspace.Path]; ok {
			return false
		}
		paths[workspace.Path] = struct{}{}
		if len(workspace.AllowedAgentInstances) == 0 {
			return false
		}
		allowed := make(map[string]struct{}, len(workspace.AllowedAgentInstances))
		for _, instanceID := range workspace.AllowedAgentInstances {
			instance, exists := instances[instanceID]
			if !exists || !instance.Enabled || instance.RuntimeMode != AgentRuntimeManaged {
				return false
			}
			if _, duplicate := allowed[instanceID]; duplicate {
				return false
			}
			allowed[instanceID] = struct{}{}
		}
		if _, exists := allowed[workspace.DefaultAgentInstance]; !exists {
			return false
		}
		switch workspace.PermissionProfile {
		case PermissionReadOnly, PermissionWorkspaceWrite:
		default:
			return false
		}
	}
	return true
}

func validURL(raw string, schemes map[string]bool) bool {
	parsed, err := url.Parse(raw)
	if err != nil || !schemes[strings.ToLower(parsed.Scheme)] || parsed.Host == "" {
		return false
	}
	return parsed.User == nil && parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == ""
}

func validSecretRef[T ~string](ref T) bool {
	return validText(string(ref), false, maxSecretRefBytes)
}

func validText(value string, required bool, maxBytes int) bool {
	if !utf8.ValidString(value) || len(value) > maxBytes {
		return false
	}
	if required && strings.TrimSpace(value) == "" {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
