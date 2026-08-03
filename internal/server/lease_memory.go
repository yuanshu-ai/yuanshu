package server

import (
	"context"
	"sync"
	"time"

	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
)

type hubLeaseStore interface {
	AcquireLease(context.Context, serverstore.LeaseAcquireRequest) (serverstore.LeaseRecord, error)
	RenewLease(context.Context, serverstore.LeaseMutationRequest) (serverstore.LeaseRecord, error)
	ReleaseLease(context.Context, serverstore.LeaseMutationRequest) (serverstore.LeaseRecord, error)
	Lease(context.Context, serverstore.LeaseScope, time.Time) (serverstore.LeaseRecord, error)
}

type hubNotificationStore interface {
	SaveNotification(context.Context, serverstore.Notification) error
	ListNotifications(context.Context, string, int) ([]serverstore.Notification, error)
	MarkNotificationRead(context.Context, string, string, time.Time) error
}

type memoryNotificationStore struct {
	mu    sync.Mutex
	clock func() time.Time
	items []serverstore.Notification
	seen  map[string]struct{}
}

func newMemoryNotificationStore(clock func() time.Time) *memoryNotificationStore {
	return &memoryNotificationStore{clock: clock, seen: make(map[string]struct{})}
}

func (s *memoryNotificationStore) SaveNotification(_ context.Context, item serverstore.Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := item.OwnerID + "\x00" + item.DedupKey
	if _, exists := s.seen[key]; exists {
		return nil
	}
	s.seen[key] = struct{}{}
	s.items = append(s.items, item)
	return nil
}

func (s *memoryNotificationStore) ListNotifications(_ context.Context, ownerID string, limit int) ([]serverstore.Notification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]serverstore.Notification, 0, limit)
	for index := len(s.items) - 1; index >= 0 && len(result) < limit; index-- {
		if s.items[index].OwnerID == ownerID {
			result = append(result, s.items[index])
		}
	}
	return result, nil
}

func (s *memoryNotificationStore) MarkNotificationRead(_ context.Context, ownerID, notificationID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.items {
		if s.items[index].OwnerID == ownerID && s.items[index].ID == notificationID {
			s.items[index].ReadAt = &now
			return nil
		}
	}
	return serverstore.ErrNotFound
}

type memoryLeaseStore struct {
	mu      sync.Mutex
	clock   func() time.Time
	records map[string]serverstore.LeaseRecord
}

func newMemoryLeaseStore(clock func() time.Time) *memoryLeaseStore {
	return &memoryLeaseStore{clock: clock, records: make(map[string]serverstore.LeaseRecord)}
}

func (s *memoryLeaseStore) AcquireLease(_ context.Context, request serverstore.LeaseAcquireRequest) (serverstore.LeaseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := leaseKey(request.Scope)
	current, ok := s.records[key]
	if !ok {
		current = serverstore.LeaseRecord{Scope: request.Scope, State: "none"}
	}
	if current.State == "held" && !current.ExpiresAt.After(request.Now) {
		current.State = "expired"
	}
	if request.ExpectedEpoch != nil && *request.ExpectedEpoch != current.Epoch {
		return current, serverstore.ErrConflict
	}
	if current.State == "held" && current.HolderClientID != request.ClientID && !request.Force {
		return current, serverstore.ErrConflict
	}
	if current.State == "held" && current.HolderClientID == request.ClientID && !request.Force {
		return current, nil
	}
	next := serverstore.LeaseRecord{Scope: request.Scope, LeaseID: request.LeaseID, HolderClientID: request.ClientID, Epoch: current.Epoch + 1, AcquiredAt: request.Now.UTC(), ExpiresAt: request.Now.UTC().Add(request.TTL), UpdatedAt: request.Now.UTC(), State: "held"}
	s.records[key] = next
	return next, nil
}

func (s *memoryLeaseStore) RenewLease(_ context.Context, request serverstore.LeaseMutationRequest) (serverstore.LeaseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.records[leaseKey(request.Scope)]
	if current.State != "held" || current.LeaseID != request.LeaseID || current.HolderClientID != request.ClientID || current.Epoch != request.Epoch {
		return current, serverstore.ErrConflict
	}
	if !current.ExpiresAt.After(request.Now) {
		current.State = "expired"
		return current, serverstore.ErrExpired
	}
	current.ExpiresAt = request.Now.UTC().Add(request.TTL)
	current.UpdatedAt = request.Now.UTC()
	s.records[leaseKey(request.Scope)] = current
	return current, nil
}

func (s *memoryLeaseStore) ReleaseLease(_ context.Context, request serverstore.LeaseMutationRequest) (serverstore.LeaseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.records[leaseKey(request.Scope)]
	if current.State != "held" || current.LeaseID != request.LeaseID || current.HolderClientID != request.ClientID || current.Epoch != request.Epoch {
		return current, serverstore.ErrConflict
	}
	current.LeaseID, current.HolderClientID = "", ""
	current.AcquiredAt, current.ExpiresAt = time.Time{}, time.Time{}
	current.Epoch++
	current.UpdatedAt = request.Now.UTC()
	current.State = "released"
	s.records[leaseKey(request.Scope)] = current
	return current, nil
}

func (s *memoryLeaseStore) Lease(_ context.Context, scope serverstore.LeaseScope, now time.Time) (serverstore.LeaseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[leaseKey(scope)]
	if !ok {
		return serverstore.LeaseRecord{Scope: scope, State: "none"}, nil
	}
	if record.State == "held" && !record.ExpiresAt.After(now) {
		record.State = "expired"
	}
	return record, nil
}

func leaseKey(scope serverstore.LeaseScope) string {
	return scope.OwnerID + "\x00" + scope.NodeID + "\x00" + scope.WorkspaceID + "\x00" + scope.ThreadID
}
