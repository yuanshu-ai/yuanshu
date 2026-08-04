package node

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
)

type configController interface {
	Read(context.Context) (map[string]any, error)
	Update(context.Context, string, map[string]any) (configUpdateResult, error)
	Pending(context.Context) ([]store.ConfigChangeRecord, error)
	Approve(context.Context, string) (configUpdateResult, error)
	Reject(context.Context, string) error
}

type configUpdateResult struct {
	Payload map[string]any
	Reload  bool
}

type ConfigChangeSummary struct {
	ID               string                     `json:"id"`
	BaseRevision     string                     `json:"baseRevision"`
	State            string                     `json:"state"`
	CreatedAt        string                     `json:"createdAt"`
	ErrorCode        string                     `json:"errorCode,omitempty"`
	Fields           []string                   `json:"fields,omitempty"`
	Details          []ConfigChangeFieldSummary `json:"details,omitempty"`
	Risk             string                     `json:"risk"`
	RelayReconnect   bool                       `json:"relayReconnect"`
	PermissionChange string                     `json:"permissionChange"`
	ExpiresAt        string                     `json:"expiresAt"`
	Expired          bool                       `json:"expired"`
}

type ConfigChangeFieldSummary struct {
	Category string `json:"category"`
	Before   string `json:"before"`
	After    string `json:"after"`
	Risk     string `json:"risk"`
}

const configChangeTTL = 24 * time.Hour

func configChangeSummary(value store.ConfigChangeRecord, configurations ...config.Config) ConfigChangeSummary {
	var changes map[string]any
	_ = json.Unmarshal(value.Changes, &changes)
	fields := make([]string, 0, len(changes))
	for field := range changes {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	details, risk, reconnect, permission := summarizeConfigChanges(changes, configurations...)
	expires := value.CreatedAt.UTC().Add(configChangeTTL)
	return ConfigChangeSummary{ID: value.ID, BaseRevision: value.BaseRevision, State: value.State, CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), ErrorCode: value.ErrorCode, Fields: fields, Details: details, Risk: risk, RelayReconnect: reconnect, PermissionChange: permission, ExpiresAt: expires.Format(time.RFC3339Nano), Expired: !expires.After(time.Now().UTC())}
}

func summarizeConfigChanges(changes map[string]any, configurations ...config.Config) ([]ConfigChangeFieldSummary, string, bool, string) {
	current := config.Config{}
	if len(configurations) > 0 {
		current = configurations[0]
	}
	details := make([]ConfigChangeFieldSummary, 0)
	risk, reconnect, permission := "medium", false, "unchanged"
	if raw, ok := changes["relayUrl"].(string); ok {
		details = append(details, ConfigChangeFieldSummary{Category: "relay", Before: redactedEndpoint(current.Relay.URL), After: redactedEndpoint(raw), Risk: "high"})
		risk, reconnect = "high", true
	}
	if raw, ok := changes["proxyUrl"]; ok {
		after, _ := optionalStringChange(raw)
		details = append(details, ConfigChangeFieldSummary{Category: "relay_proxy", Before: redactedEndpoint(current.Relay.ProxyURL), After: redactedEndpoint(after), Risk: "high"})
		risk, reconnect = "high", true
	}
	if raw, ok := changes["workspaces"]; ok {
		items, _ := workspaceChanges(raw)
		for _, item := range items {
			before, after, direction := workspacePolicySummary(current, item)
			details = append(details, ConfigChangeFieldSummary{Category: "workspace_policy", Before: before, After: after, Risk: "high"})
			if direction != "unchanged" {
				permission = direction
			}
		}
		risk = "high"
	}
	return details, risk, reconnect, permission
}

func redactedEndpoint(raw string) string {
	if raw == "" {
		return "未配置"
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "已配置（无效）"
	}
	return parsed.Scheme + "://" + parsed.Host
}

func workspacePolicySummary(current config.Config, change workspaceChange) (string, string, string) {
	beforePermission, beforeNetwork := "unknown", false
	for _, item := range current.Workspaces {
		if item.ID == change.ID {
			beforePermission, beforeNetwork = string(item.PermissionProfile), item.AllowNetwork
			break
		}
	}
	afterPermission, afterNetwork := beforePermission, beforeNetwork
	if change.PermissionProfile != nil {
		afterPermission = string(*change.PermissionProfile)
	}
	if change.AllowNetwork != nil {
		afterNetwork = *change.AllowNetwork
	}
	direction := "unchanged"
	if (beforePermission == string(config.PermissionWorkspaceWrite) && afterPermission == string(config.PermissionReadOnly)) || (beforeNetwork && !afterNetwork) {
		direction = "reduced"
	} else if (beforePermission == string(config.PermissionReadOnly) && afterPermission == string(config.PermissionWorkspaceWrite)) || (!beforeNetwork && afterNetwork) {
		direction = "expanded"
	}
	return fmt.Sprintf("%s / network:%t", beforePermission, beforeNetwork), fmt.Sprintf("%s / network:%t", afterPermission, afterNetwork), direction
}

type nodeConfigController struct {
	path  string
	local *store.Store
	clock func() time.Time
}

func newNodeConfigController(path string, local *store.Store, clock func() time.Time) (*nodeConfigController, error) {
	if path == "" || local == nil {
		return nil, errors.New("node configuration controller is unavailable")
	}
	if clock == nil {
		clock = time.Now
	}
	return &nodeConfigController{path: path, local: local, clock: clock}, nil
}

func (c *nodeConfigController) Read(ctx context.Context) (map[string]any, error) {
	loaded, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	pending, err := c.local.ConfigChanges(ctx, store.ConfigChangePending)
	if err != nil {
		return nil, err
	}
	view := configView(loaded.Config, configRevision(loaded.Config), len(pending))
	view["pendingChangeSummaries"] = configChangeSummaries(pending, loaded.Config)
	return view, nil
}

func (c *nodeConfigController) Update(ctx context.Context, baseRevision string, changes map[string]any) (configUpdateResult, error) {
	loaded, err := c.load(ctx)
	if err != nil {
		return configUpdateResult{}, err
	}
	currentRevision := configRevision(loaded.Config)
	if baseRevision == "" || baseRevision != currentRevision {
		return configUpdateResult{}, store.ErrConflict
	}
	next, sensitive, err := applyRemoteChanges(loaded.Config, changes, false)
	if err != nil {
		return configUpdateResult{}, err
	}
	encoded, err := json.Marshal(changes)
	if err != nil || len(encoded) > 262144 {
		return configUpdateResult{}, store.ErrInvalid
	}
	if sensitive {
		id, idErr := configChangeID()
		if idErr != nil {
			return configUpdateResult{}, idErr
		}
		created, err := c.local.CreateConfigChange(ctx, store.ConfigChangeRecord{ID: id, BaseRevision: baseRevision, Changes: encoded, State: store.ConfigChangePending, CreatedAt: c.clock().UTC()})
		if err != nil {
			return configUpdateResult{}, err
		}
		view, viewErr := configViewWithPending(ctx, c.local, loaded.Config, currentRevision)
		if viewErr != nil {
			return configUpdateResult{}, viewErr
		}
		return configUpdateResult{Payload: map[string]any{"config": view, "pending": true, "requiresLocalConfirmation": true, "changeId": id, "change": configChangeSummary(created, loaded.Config), "revision": currentRevision}}, nil
	}
	if err := c.save(ctx, next); err != nil {
		return configUpdateResult{}, err
	}
	view, err := configViewWithPending(ctx, c.local, next, configRevision(next))
	if err != nil {
		return configUpdateResult{}, err
	}
	return configUpdateResult{Payload: map[string]any{"config": view, "applied": true, "revision": configRevision(next)}, Reload: true}, nil
}

func (c *nodeConfigController) Pending(ctx context.Context) ([]store.ConfigChangeRecord, error) {
	return c.local.ConfigChanges(ctx, store.ConfigChangePending)
}

func (c *nodeConfigController) Approve(ctx context.Context, id string) (configUpdateResult, error) {
	change, err := c.local.ConfigChange(ctx, id)
	if err != nil {
		return configUpdateResult{}, err
	}
	if !change.CreatedAt.Add(configChangeTTL).After(c.clock().UTC()) {
		_, _ = c.local.TransitionConfigChange(ctx, id, store.ConfigChangeRejected, "config_change_expired")
		return configUpdateResult{}, store.ErrConflict
	}
	if change.State != store.ConfigChangePending {
		return configUpdateResult{}, store.ErrConflict
	}
	loaded, err := c.load(ctx)
	if err != nil {
		return configUpdateResult{}, err
	}
	if configRevision(loaded.Config) != change.BaseRevision {
		_, _ = c.local.TransitionConfigChange(ctx, id, store.ConfigChangeRejected, "stale_revision")
		return configUpdateResult{}, store.ErrConflict
	}
	var changes map[string]any
	if err := json.Unmarshal(change.Changes, &changes); err != nil {
		return configUpdateResult{}, store.ErrCorrupt
	}
	next, _, err := applyRemoteChanges(loaded.Config, changes, true)
	if err != nil {
		_, _ = c.local.TransitionConfigChange(ctx, id, store.ConfigChangeRejected, errorCode(err))
		return configUpdateResult{}, err
	}
	if err := c.save(ctx, next); err != nil {
		return configUpdateResult{}, err
	}
	if _, err := c.local.TransitionConfigChange(ctx, id, store.ConfigChangeApproved, ""); err != nil {
		return configUpdateResult{}, err
	}
	view, err := configViewWithPending(ctx, c.local, next, configRevision(next))
	if err != nil {
		return configUpdateResult{}, err
	}
	return configUpdateResult{Payload: map[string]any{"config": view, "applied": true, "changeId": id, "revision": configRevision(next)}, Reload: true}, nil
}

func (c *nodeConfigController) Reject(ctx context.Context, id string) error {
	_, err := c.local.TransitionConfigChange(ctx, id, store.ConfigChangeRejected, "rejected_by_local_user")
	return err
}

func (c *nodeConfigController) load(ctx context.Context) (config.LoadResult, error) {
	file, err := config.NewFileStore(c.path)
	if err != nil {
		return config.LoadResult{}, err
	}
	return file.Load(ctx)
}

func (c *nodeConfigController) save(ctx context.Context, value config.Config) error {
	file, err := config.NewFileStore(c.path)
	if err != nil {
		return err
	}
	return file.Save(ctx, value)
}

func configViewWithPending(ctx context.Context, local *store.Store, value config.Config, revision string) (map[string]any, error) {
	pending, err := local.ConfigChanges(ctx, store.ConfigChangePending)
	if err != nil {
		return nil, err
	}
	view := configView(value, revision, len(pending))
	view["pendingChangeSummaries"] = configChangeSummaries(pending, value)
	return view, nil
}

func configChangeSummaries(changes []store.ConfigChangeRecord, value config.Config) []ConfigChangeSummary {
	result := make([]ConfigChangeSummary, 0, len(changes))
	for _, change := range changes {
		result = append(result, configChangeSummary(change, value))
	}
	return result
}

func configView(value config.Config, revision string, pending int) map[string]any {
	workspaces := make([]any, 0, len(value.Workspaces))
	for _, workspace := range value.Workspaces {
		workspaces = append(workspaces, map[string]any{
			"id": workspace.ID, "name": workspace.DisplayName,
			"permissionProfile": string(workspace.PermissionProfile), "allowNetwork": workspace.AllowNetwork,
		})
	}
	return map[string]any{
		"revision":  revision,
		"host":      map[string]any{"name": value.Host.Name},
		"transport": map[string]any{"mode": string(value.Transport.Mode)},
		"relay": map[string]any{
			"url": value.Relay.URL, "proxyUrl": value.Relay.ProxyURL,
			"connectTimeoutSeconds": value.Relay.ConnectTimeoutSeconds,
			"credentialConfigured":  value.Relay.CredentialRef != "",
		},
		"adapter": map[string]any{
			"codexEnabled":     value.Adapters.Codex.Enabled,
			"binaryConfigured": value.Adapters.Codex.Binary != "",
			"homeConfigured":   value.Adapters.Codex.Home != "",
			"runtimeMode":      value.Adapters.Codex.RuntimeMode,
		},
		"events":         map[string]any{"maxAgeHours": value.Events.MaxAgeHours, "maxSizeMiB": value.Events.MaxSizeMiB},
		"workspaces":     workspaces,
		"pendingChanges": pending,
	}
}

func applyRemoteChanges(current config.Config, changes map[string]any, localConfirmation bool) (config.Config, bool, error) {
	if len(changes) == 0 {
		return config.Config{}, false, store.ErrInvalid
	}
	next := current
	sensitive := false
	if value, ok := changes["hostName"]; ok {
		name, valid := stringChange(value)
		if !valid {
			return config.Config{}, false, store.ErrInvalid
		}
		next.Host.Name = name
	}
	if value, ok := changes["relayUrl"]; ok {
		url, valid := stringChange(value)
		if !valid {
			return config.Config{}, false, store.ErrInvalid
		}
		next.Relay.URL = url
		sensitive = true
	}
	if value, ok := changes["proxyUrl"]; ok {
		url, valid := optionalStringChange(value)
		if !valid {
			return config.Config{}, false, store.ErrInvalid
		}
		next.Relay.ProxyURL = url
		sensitive = true
	}
	if value, ok := changes["connectTimeoutSeconds"]; ok {
		seconds, valid := intChange(value)
		if !valid {
			return config.Config{}, false, store.ErrInvalid
		}
		next.Relay.ConnectTimeoutSeconds = seconds
	}
	if value, ok := changes["eventsMaxAgeHours"]; ok {
		hours, valid := intChange(value)
		if !valid {
			return config.Config{}, false, store.ErrInvalid
		}
		next.Events.MaxAgeHours = hours
	}
	if value, ok := changes["eventsMaxSizeMiB"]; ok {
		size, valid := intChange(value)
		if !valid {
			return config.Config{}, false, store.ErrInvalid
		}
		next.Events.MaxSizeMiB = size
	}
	if value, ok := changes["workspaces"]; ok {
		items, valid := workspaceChanges(value)
		if !valid {
			return config.Config{}, false, store.ErrInvalid
		}
		for _, item := range items {
			found := false
			for index := range next.Workspaces {
				if next.Workspaces[index].ID != item.ID {
					continue
				}
				found = true
				if item.DisplayName != nil {
					next.Workspaces[index].DisplayName = *item.DisplayName
				}
				if item.PermissionProfile != nil {
					if next.Workspaces[index].PermissionProfile == config.PermissionReadOnly && *item.PermissionProfile == config.PermissionWorkspaceWrite {
						return config.Config{}, false, errors.New("workspace permission escalation requires local configuration")
					}
					next.Workspaces[index].PermissionProfile = *item.PermissionProfile
				}
				if item.AllowNetwork != nil {
					if *item.AllowNetwork {
						return config.Config{}, false, errors.New("network permission escalation requires local configuration")
					}
					next.Workspaces[index].AllowNetwork = false
				}
			}
			if !found {
				return config.Config{}, false, store.ErrNotFound
			}
		}
		sensitive = true
	}
	if err := config.Validate(next); err != nil {
		return config.Config{}, false, err
	}
	if localConfirmation {
		return next, sensitive, nil
	}
	return next, sensitive, nil
}

type workspaceChange struct {
	ID                string
	DisplayName       *string
	PermissionProfile *config.PermissionProfile
	AllowNetwork      *bool
}

func workspaceChanges(value any) ([]workspaceChange, bool) {
	items, ok := value.([]any)
	if !ok {
		if typed, typedOK := value.([]map[string]any); typedOK {
			items = make([]any, len(typed))
			for index := range typed {
				items[index] = typed[index]
			}
		} else {
			return nil, false
		}
	}
	result := make([]workspaceChange, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, false
		}
		id, ok := stringChange(item["id"])
		if !ok {
			return nil, false
		}
		change := workspaceChange{ID: id}
		if value, exists := item["displayName"]; exists {
			text, valid := stringChange(value)
			if !valid {
				return nil, false
			}
			change.DisplayName = &text
		}
		if value, exists := item["permissionProfile"]; exists {
			text, valid := stringChange(value)
			profile := config.PermissionProfile(text)
			if !valid || (profile != config.PermissionReadOnly && profile != config.PermissionWorkspaceWrite) {
				return nil, false
			}
			change.PermissionProfile = &profile
		}
		if value, exists := item["allowNetwork"]; exists {
			allowed, valid := boolChange(value)
			if !valid {
				return nil, false
			}
			change.AllowNetwork = &allowed
		}
		if change.DisplayName == nil && change.PermissionProfile == nil && change.AllowNetwork == nil {
			return nil, false
		}
		result = append(result, change)
	}
	return result, len(result) > 0
}

func stringChange(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok && strings.TrimSpace(text) != "" && len(text) <= 4096
}

func optionalStringChange(value any) (string, bool) {
	if value == nil {
		return "", true
	}
	text, ok := value.(string)
	return text, ok && len(text) <= 4096
}

func intChange(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), int64(int(typed)) == typed
	case float64:
		return int(typed), typed == float64(int(typed))
	default:
		return 0, false
	}
}

func boolChange(value any) (bool, bool) {
	result, ok := value.(bool)
	return result, ok
}

func configRevision(value config.Config) string {
	encoded, _ := toml.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hexDigest(digest[:])
}

func configChangeID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("configuration change id unavailable")
	}
	return "cfg_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func hexDigest(value []byte) string {
	const hex = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, item := range value {
		result[index*2] = hex[item>>4]
		result[index*2+1] = hex[item&15]
	}
	return string(result)
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, store.ErrConflict):
		return "conflict"
	case errors.Is(err, store.ErrNotFound):
		return "not_found"
	case errors.Is(err, store.ErrInvalid), errors.Is(err, config.ErrInvalid):
		return "invalid_config"
	default:
		return fmt.Sprintf("%T", err)
	}
}
