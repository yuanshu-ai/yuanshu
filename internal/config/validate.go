package config

import (
	"net/url"
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
	switch value.Transport.Mode {
	case TransportRelay, TransportStandalone:
	default:
		return configError("validation", ErrInvalid)
	}
	if value.Transport.Mode == TransportRelay && value.Relay.URL == "" {
		return configError("validation", ErrInvalid)
	}
	if value.Relay.URL != "" && !validURL(value.Relay.URL, map[string]bool{"wss": true}) {
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
	if value.Adapters.Codex.RuntimeMode != "stdio" {
		return configError("validation", ErrInvalid)
	}
	if value.Adapters.Codex.Enabled && !validText(value.Adapters.Codex.Binary, true, maxPathBytes) {
		return configError("validation", ErrInvalid)
	}
	if value.Adapters.Codex.Binary != "" && !validText(value.Adapters.Codex.Binary, true, maxPathBytes) {
		return configError("validation", ErrInvalid)
	}
	if value.Adapters.Codex.Home != "" && !validText(value.Adapters.Codex.Home, false, maxPathBytes) {
		return configError("validation", ErrInvalid)
	}
	if value.Events.MaxAgeHours < 1 || value.Events.MaxAgeHours > 720 ||
		value.Events.MaxSizeMiB < 16 || value.Events.MaxSizeMiB > 4096 {
		return configError("validation", ErrInvalid)
	}
	if value.Workspaces == nil {
		return configError("validation", ErrInvalid)
	}
	if !validWorkspaces(value.Workspaces) {
		return configError("validation", ErrInvalid)
	}
	return nil
}

func validWorkspaces(workspaces []WorkspaceConfig) bool {
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
		if len(workspace.AllowedAdapters) != 1 || workspace.AllowedAdapters[0] != "codex" ||
			workspace.DefaultAdapter != "codex" {
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
