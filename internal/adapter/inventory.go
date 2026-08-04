package adapter

import (
	"context"
	"sort"
	"sync"
	"time"
)

type InstallationDescriptor struct {
	ID           string
	Agent        AgentDescriptor
	Installation Installation
	State        InstallationState
	Process      ProcessState
	Configured   bool
}

type Detector interface {
	Agent() AgentDescriptor
	Detect(context.Context) ([]InstallationDescriptor, error)
}

type InventorySnapshot struct {
	Generation    uint64
	ObservedAt    time.Time
	Installations []InstallationDescriptor
}

type Inventory struct {
	detectors []Detector
	mu        sync.Mutex
	snapshot  InventorySnapshot
	refresh   chan struct{}
}

func NewInventory(detectors ...Detector) (*Inventory, error) {
	seen := make(map[string]AgentDescriptor, len(detectors))
	copyDetectors := append([]Detector(nil), detectors...)
	for _, detector := range copyDetectors {
		if detector == nil {
			return nil, ErrInvalid
		}
		agent := detector.Agent()
		if !validIdentifier(agent.Type) || !validDisplayName(agent.DisplayName) {
			return nil, ErrInvalid
		}
		if existing, ok := seen[agent.Type]; ok && existing != agent {
			return nil, ErrConflict
		}
		if _, ok := seen[agent.Type]; ok {
			return nil, ErrConflict
		}
		seen[agent.Type] = agent
	}
	sort.Slice(copyDetectors, func(i, j int) bool { return copyDetectors[i].Agent().Type < copyDetectors[j].Agent().Type })
	return &Inventory{detectors: copyDetectors}, nil
}

func (i *Inventory) Refresh(ctx context.Context) (InventorySnapshot, error) {
	if i == nil {
		return InventorySnapshot{}, ErrInvalid
	}
	if ctx == nil {
		return InventorySnapshot{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return InventorySnapshot{}, err
	}
	i.mu.Lock()
	if current := i.refresh; current != nil {
		i.mu.Unlock()
		select {
		case <-current:
			return i.Snapshot(), nil
		case <-ctx.Done():
			return InventorySnapshot{}, ctx.Err()
		}
	}
	i.refresh = make(chan struct{})
	current := i.refresh
	i.mu.Unlock()

	items := make([]InstallationDescriptor, 0, len(i.detectors))
	for _, detector := range i.detectors {
		if err := ctx.Err(); err != nil {
			i.finishRefresh(current, InventorySnapshot{}, false)
			return InventorySnapshot{}, err
		}
		detected, err := detector.Detect(ctx)
		if err != nil || !validDetectedInstallations(detector.Agent(), detected) {
			detected = []InstallationDescriptor{{
				ID: detector.Agent().Type + "-unavailable", Agent: detector.Agent(),
				State: InstallationUnavailable, Process: ProcessUnknown,
			}}
		}
		items = append(items, detected...)
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].Agent.Type == items[right].Agent.Type {
			return items[left].ID < items[right].ID
		}
		return items[left].Agent.Type < items[right].Agent.Type
	})
	i.mu.Lock()
	generation := i.snapshot.Generation + 1
	i.mu.Unlock()
	result := InventorySnapshot{Generation: generation, ObservedAt: time.Now().UTC(), Installations: items}
	i.finishRefresh(current, result, true)
	return cloneInventorySnapshot(result), nil
}

func (i *Inventory) Snapshot() InventorySnapshot {
	if i == nil {
		return InventorySnapshot{}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return cloneInventorySnapshot(i.snapshot)
}

func (i *Inventory) finishRefresh(current chan struct{}, value InventorySnapshot, publish bool) {
	i.mu.Lock()
	if publish {
		i.snapshot = cloneInventorySnapshot(value)
	}
	if i.refresh == current {
		i.refresh = nil
		close(current)
	}
	i.mu.Unlock()
}

func cloneInventorySnapshot(value InventorySnapshot) InventorySnapshot {
	value.Installations = append([]InstallationDescriptor(nil), value.Installations...)
	return value
}

func validDetectedInstallations(agent AgentDescriptor, items []InstallationDescriptor) bool {
	if len(items) == 0 || len(items) > 64 {
		return false
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.Agent != agent || !validIdentifier(item.ID) {
			return false
		}
		if _, ok := seen[item.ID]; ok {
			return false
		}
		seen[item.ID] = struct{}{}
		switch item.State {
		case InstallationInstalled, InstallationNotInstalled, InstallationIncompatible, InstallationUnavailable:
		default:
			return false
		}
		switch item.Process {
		case ProcessRunning, ProcessStopped, ProcessUnknown:
		default:
			return false
		}
	}
	return true
}
