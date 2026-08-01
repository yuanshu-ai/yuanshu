package fake

import (
	"context"
	"sync"

	platformpkg "github.com/yuanshu-ai/yuanshu/internal/platform"
)

type WorkspaceInspector struct {
	failure injectedFailure
	mu      sync.RWMutex
	facts   map[string]platformpkg.WorkspaceFacts
}

var _ platformpkg.WorkspaceInspector = (*WorkspaceInspector)(nil)

func NewWorkspaceInspector() *WorkspaceInspector {
	return &WorkspaceInspector{facts: make(map[string]platformpkg.WorkspaceFacts)}
}

func (*WorkspaceInspector) Available() bool { return true }

func (w *WorkspaceInspector) SetError(err error) { w.failure.set(err) }

func (w *WorkspaceInspector) Register(path string, facts platformpkg.WorkspaceFacts) error {
	if path == "" {
		return platformpkg.ErrInvalidArgument
	}
	w.mu.Lock()
	w.facts[path] = facts
	w.mu.Unlock()
	return nil
}

func (w *WorkspaceInspector) Inspect(ctx context.Context, path string) (platformpkg.WorkspaceFacts, error) {
	if err := w.failure.get(ctx); err != nil {
		return platformpkg.WorkspaceFacts{}, err
	}
	if path == "" {
		return platformpkg.WorkspaceFacts{}, platformpkg.ErrInvalidArgument
	}
	w.mu.RLock()
	facts, ok := w.facts[path]
	w.mu.RUnlock()
	if !ok {
		return platformpkg.WorkspaceFacts{}, platformpkg.ErrNotFound
	}
	return facts, nil
}
