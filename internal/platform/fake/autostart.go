package fake

import (
	"context"
	"sync"

	platformpkg "github.com/yuanshu-ai/yuanshu/internal/platform"
)

type AutostartManager struct {
	failure injectedFailure
	mu      sync.RWMutex
	entries map[string]platformpkg.AutostartEntry
}

var _ platformpkg.AutostartManager = (*AutostartManager)(nil)

func NewAutostartManager() *AutostartManager {
	return &AutostartManager{entries: make(map[string]platformpkg.AutostartEntry)}
}

func (*AutostartManager) Available() bool { return true }

func (a *AutostartManager) SetError(err error) { a.failure.set(err) }

func (a *AutostartManager) Install(ctx context.Context, entry platformpkg.AutostartEntry) error {
	if err := a.failure.get(ctx); err != nil {
		return err
	}
	if entry.ID == "" || entry.Executable == "" {
		return platformpkg.ErrInvalidArgument
	}
	a.mu.Lock()
	a.entries[entry.ID] = cloneAutostartEntry(entry)
	a.mu.Unlock()
	return nil
}

func (a *AutostartManager) Remove(ctx context.Context, id string) error {
	if err := a.failure.get(ctx); err != nil {
		return err
	}
	if id == "" {
		return platformpkg.ErrInvalidArgument
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.entries[id]; !ok {
		return platformpkg.ErrNotFound
	}
	delete(a.entries, id)
	return nil
}

func (a *AutostartManager) Status(ctx context.Context, id string) (platformpkg.AutostartStatus, error) {
	if err := a.failure.get(ctx); err != nil {
		return platformpkg.AutostartStatus{}, err
	}
	if id == "" {
		return platformpkg.AutostartStatus{}, platformpkg.ErrInvalidArgument
	}
	a.mu.RLock()
	entry, ok := a.entries[id]
	a.mu.RUnlock()
	if !ok {
		return platformpkg.AutostartStatus{Installed: false}, nil
	}
	return platformpkg.AutostartStatus{Installed: true, Entry: cloneAutostartEntry(entry)}, nil
}

func cloneAutostartEntry(entry platformpkg.AutostartEntry) platformpkg.AutostartEntry {
	copyOfEntry := entry
	copyOfEntry.Args = append([]string(nil), entry.Args...)
	if entry.Env != nil {
		copyOfEntry.Env = append([]string{}, entry.Env...)
	}
	return copyOfEntry
}
