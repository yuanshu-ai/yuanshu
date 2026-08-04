package fake

import (
	"context"
	"sync"

	platformpkg "github.com/yuanshu-ai/yuanshu/internal/platform"
)

type DirectoryPicker struct {
	failure injectedFailure
	mu      sync.RWMutex
	value   platformpkg.DirectorySelection
}

var _ platformpkg.DirectoryPicker = (*DirectoryPicker)(nil)

func NewDirectoryPicker() *DirectoryPicker { return &DirectoryPicker{} }

func (*DirectoryPicker) Available() bool { return true }

func (p *DirectoryPicker) Set(selection platformpkg.DirectorySelection) {
	p.mu.Lock()
	p.value = selection
	p.mu.Unlock()
}

func (p *DirectoryPicker) SetError(err error) { p.failure.set(err) }

func (p *DirectoryPicker) PickDirectory(ctx context.Context) (platformpkg.DirectorySelection, error) {
	if err := p.failure.get(ctx); err != nil {
		return platformpkg.DirectorySelection{}, err
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.value.Path == "" {
		return platformpkg.DirectorySelection{}, context.Canceled
	}
	return p.value, nil
}
