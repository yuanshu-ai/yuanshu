// Package workspace owns the Node-local workspace trust and path policy.
package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

var (
	ErrInvalid     = errors.New("workspace request is invalid")
	ErrNotFound    = errors.New("workspace was not found")
	ErrDenied      = errors.New("workspace policy denied the request")
	ErrStale       = errors.New("workspace registration is stale")
	ErrUnavailable = errors.New("workspace inspection is unavailable")
	ErrInternal    = errors.New("workspace policy failed")
)

type PathIntent string

const (
	PathRead  PathIntent = "read"
	PathWrite PathIntent = "write"
)

type WorkspaceStore interface {
	ReplaceWorkspaces(ctx context.Context, records []store.WorkspaceRecord) error
	Workspace(ctx context.Context, id string) (store.WorkspaceRecord, error)
	Workspaces(ctx context.Context) ([]store.WorkspaceRecord, error)
}

type Manager struct {
	inspector platform.WorkspaceInspector
	store     WorkspaceStore
}

type Descriptor struct {
	ID                string
	DisplayName       string
	Adapter           string
	PermissionProfile config.PermissionProfile
	AllowNetwork      bool
}

type ResolvedWorkspace struct {
	Descriptor
	CanonicalPath  string
	FilesystemRoot string
	FileIdentity   string
}

type ResolvedPath struct {
	Workspace ResolvedWorkspace
	Path      string
	Exists    bool
	Directory bool
}

func NewManager(inspector platform.WorkspaceInspector, workspaceStore WorkspaceStore) (*Manager, error) {
	if inspector == nil || workspaceStore == nil {
		return nil, ErrInvalid
	}
	return &Manager{inspector: inspector, store: workspaceStore}, nil
}

func (m *Manager) Reconcile(ctx context.Context, configured []config.WorkspaceConfig) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if configured == nil {
		return ErrInvalid
	}
	if !m.inspector.Available() {
		return ErrUnavailable
	}
	records := make([]store.WorkspaceRecord, 0, len(configured))
	canonicalPaths := make(map[string]struct{}, len(configured))
	identities := make(map[string]struct{}, len(configured))
	ids := make(map[string]struct{}, len(configured))
	for _, item := range configured {
		if !validConfig(item) {
			return ErrInvalid
		}
		if _, exists := ids[item.ID]; exists {
			return ErrInvalid
		}
		ids[item.ID] = struct{}{}
		facts, err := ValidateRoot(ctx, m.inspector, item.Path)
		if err != nil {
			return err
		}
		pathKey := strings.ToLower(filepath.Clean(facts.CanonicalPath))
		if _, exists := canonicalPaths[pathKey]; exists {
			return ErrInvalid
		}
		canonicalPaths[pathKey] = struct{}{}
		if _, exists := identities[facts.FileIdentity]; exists {
			return ErrInvalid
		}
		identities[facts.FileIdentity] = struct{}{}
		records = append(records, store.WorkspaceRecord{
			ID:                item.ID,
			DisplayName:       item.DisplayName,
			CanonicalPath:     facts.CanonicalPath,
			FilesystemRoot:    facts.FilesystemRoot,
			FileIdentity:      facts.FileIdentity,
			Adapter:           "codex",
			PermissionProfile: string(item.PermissionProfile),
			AllowNetwork:      item.AllowNetwork,
		})
	}
	if err := m.store.ReplaceWorkspaces(ctx, records); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return ErrInternal
	}
	return nil
}

// ValidateRoot applies the complete Node-local workspace boundary without
// registering the path. Setup uses it after a native picker selection and
// Reconcile applies it again before persisting the workspace identity.
func ValidateRoot(ctx context.Context, inspector platform.WorkspaceInspector, path string) (platform.WorkspaceFacts, error) {
	if err := contextError(ctx); err != nil {
		return platform.WorkspaceFacts{}, err
	}
	if inspector == nil || !inspector.Available() || path == "" {
		return platform.WorkspaceFacts{}, ErrUnavailable
	}
	facts, err := inspector.Inspect(ctx, path)
	if err != nil {
		return platform.WorkspaceFacts{}, reconcileInspectionError(err)
	}
	if !safeWorkspaceRoot(facts) {
		return platform.WorkspaceFacts{}, ErrDenied
	}
	return facts, nil
}

func (m *Manager) List(ctx context.Context) ([]Descriptor, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	records, err := m.store.Workspaces(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, ErrInternal
	}
	descriptors := make([]Descriptor, len(records))
	for index, record := range records {
		descriptors[index] = descriptor(record)
	}
	return descriptors, nil
}

func (m *Manager) Resolve(ctx context.Context, id string) (ResolvedWorkspace, error) {
	if err := contextError(ctx); err != nil {
		return ResolvedWorkspace{}, err
	}
	if !validText(id, 128) {
		return ResolvedWorkspace{}, ErrInvalid
	}
	if !m.inspector.Available() {
		return ResolvedWorkspace{}, ErrUnavailable
	}
	record, err := m.store.Workspace(ctx, id)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			return ResolvedWorkspace{}, ErrNotFound
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return ResolvedWorkspace{}, err
		default:
			return ResolvedWorkspace{}, ErrInternal
		}
	}
	facts, err := m.inspector.Inspect(ctx, record.CanonicalPath)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ResolvedWorkspace{}, err
		}
		if errors.Is(err, platform.ErrUnavailable) {
			return ResolvedWorkspace{}, ErrUnavailable
		}
		return ResolvedWorkspace{}, ErrStale
	}
	if !safeWorkspaceRoot(facts) ||
		facts.FileIdentity != record.FileIdentity ||
		!samePath(facts.CanonicalPath, record.CanonicalPath) ||
		!samePath(facts.FilesystemRoot, record.FilesystemRoot) {
		return ResolvedWorkspace{}, ErrStale
	}
	return ResolvedWorkspace{
		Descriptor:     descriptor(record),
		CanonicalPath:  facts.CanonicalPath,
		FilesystemRoot: facts.FilesystemRoot,
		FileIdentity:   facts.FileIdentity,
	}, nil
}

func (m *Manager) ResolvePath(ctx context.Context, id, logicalPath string, intent PathIntent) (ResolvedPath, error) {
	if err := contextError(ctx); err != nil {
		return ResolvedPath{}, err
	}
	if intent != PathRead && intent != PathWrite || !validLogicalPath(logicalPath) {
		return ResolvedPath{}, ErrInvalid
	}
	resolved, err := m.Resolve(ctx, id)
	if err != nil {
		return ResolvedPath{}, err
	}
	if intent == PathWrite && resolved.PermissionProfile != config.PermissionWorkspaceWrite {
		return ResolvedPath{}, ErrDenied
	}
	target := filepath.Join(resolved.CanonicalPath, filepath.FromSlash(logicalPath))
	if !pathWithin(target, resolved.CanonicalPath) {
		return ResolvedPath{}, ErrDenied
	}
	facts, inspectErr := m.inspector.Inspect(ctx, target)
	if inspectErr == nil {
		if !safeWorkspaceTarget(facts, resolved.CanonicalPath) {
			return ResolvedPath{}, ErrDenied
		}
		return ResolvedPath{Workspace: resolved, Path: facts.CanonicalPath, Exists: true, Directory: facts.IsDirectory}, nil
	}
	if errors.Is(inspectErr, context.Canceled) || errors.Is(inspectErr, context.DeadlineExceeded) {
		return ResolvedPath{}, inspectErr
	}
	if errors.Is(inspectErr, platform.ErrUnavailable) {
		return ResolvedPath{}, ErrUnavailable
	}
	if !errors.Is(inspectErr, platform.ErrNotFound) {
		return ResolvedPath{}, ErrDenied
	}
	if intent == PathRead {
		return ResolvedPath{}, ErrNotFound
	}
	ancestor := filepath.Dir(target)
	for {
		facts, err = m.inspector.Inspect(ctx, ancestor)
		if err == nil {
			if !facts.IsDirectory || !safeWorkspaceTarget(facts, resolved.CanonicalPath) {
				return ResolvedPath{}, ErrDenied
			}
			remainder, err := filepath.Rel(ancestor, target)
			if err != nil || remainder == "." || remainder == ".." || strings.HasPrefix(remainder, ".."+string(filepath.Separator)) {
				return ResolvedPath{}, ErrDenied
			}
			path := filepath.Join(facts.CanonicalPath, remainder)
			if !pathWithin(path, resolved.CanonicalPath) {
				return ResolvedPath{}, ErrDenied
			}
			return ResolvedPath{Workspace: resolved, Path: path}, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ResolvedPath{}, err
		}
		if !errors.Is(err, platform.ErrNotFound) || samePath(ancestor, resolved.CanonicalPath) {
			return ResolvedPath{}, ErrDenied
		}
		next := filepath.Dir(ancestor)
		if next == ancestor || !pathWithin(next, resolved.CanonicalPath) {
			return ResolvedPath{}, ErrDenied
		}
		ancestor = next
	}
}

func descriptor(record store.WorkspaceRecord) Descriptor {
	return Descriptor{
		ID:                record.ID,
		DisplayName:       record.DisplayName,
		Adapter:           record.Adapter,
		PermissionProfile: config.PermissionProfile(record.PermissionProfile),
		AllowNetwork:      record.AllowNetwork,
	}
}

func safeWorkspaceRoot(facts platform.WorkspaceFacts) bool {
	return facts.IsDirectory && facts.CanonicalPath != "" && facts.FilesystemRoot != "" && facts.FileIdentity != "" &&
		!facts.IsFilesystemRoot && !facts.IsHome && !facts.IsSystem &&
		!facts.CrossesLinkBoundary && !facts.CrossesReparseBoundary
}

func safeWorkspaceTarget(facts platform.WorkspaceFacts, root string) bool {
	return facts.CanonicalPath != "" && facts.FileIdentity != "" &&
		!facts.CrossesLinkBoundary && !facts.CrossesReparseBoundary && pathWithin(facts.CanonicalPath, root)
}

func validConfig(item config.WorkspaceConfig) bool {
	return validText(item.ID, 128) && validText(item.DisplayName, 128) && validText(item.Path, 4096) &&
		len(item.AllowedAgentInstances) > 0 && item.DefaultAgentInstance != "" && contains(item.AllowedAgentInstances, item.DefaultAgentInstance) &&
		(item.PermissionProfile == config.PermissionReadOnly || item.PermissionProfile == config.PermissionWorkspaceWrite)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validLogicalPath(value string) bool {
	if !validText(value, 4096) || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.Contains(value, ":") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func validText(value string, maxBytes int) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || len(value) > maxBytes {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}

func samePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func reconcileInspectionError(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, platform.ErrUnavailable):
		return ErrUnavailable
	case errors.Is(err, platform.ErrNotFound), errors.Is(err, platform.ErrInvalidArgument):
		return ErrInvalid
	default:
		return ErrDenied
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}
