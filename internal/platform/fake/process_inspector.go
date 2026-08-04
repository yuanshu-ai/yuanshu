package fake

import (
	"context"
	"strings"
	"sync"

	platformpkg "github.com/yuanshu-ai/yuanshu/internal/platform"
)

type ProcessInspector struct {
	failure injectedFailure
	mu      sync.RWMutex
	names   map[string]int
}

func NewProcessInspector() *ProcessInspector {
	return &ProcessInspector{names: make(map[string]int)}
}

func (*ProcessInspector) Available() bool { return true }

func (p *ProcessInspector) Inspect(ctx context.Context, query platformpkg.ProcessQuery) (platformpkg.ProcessSummary, error) {
	if err := p.failure.get(ctx); err != nil {
		return platformpkg.ProcessSummary{State: platformpkg.ProcessUnknown}, err
	}
	if len(query.ExecutableNames) == 0 {
		return platformpkg.ProcessSummary{State: platformpkg.ProcessUnknown}, platformpkg.ErrInvalidArgument
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	matches := 0
	for _, name := range query.ExecutableNames {
		matches += p.names[strings.ToLower(name)]
	}
	if matches > 0 {
		return platformpkg.ProcessSummary{State: platformpkg.ProcessRunning, Matches: matches}, nil
	}
	return platformpkg.ProcessSummary{State: platformpkg.ProcessStopped}, nil
}

func (p *ProcessInspector) Set(name string, count int) {
	p.mu.Lock()
	if count <= 0 {
		delete(p.names, strings.ToLower(name))
	} else {
		p.names[strings.ToLower(name)] = count
	}
	p.mu.Unlock()
}

func (p *ProcessInspector) SetError(err error) { p.failure.set(err) }
