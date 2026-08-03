package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const DefaultLeaseTTL = 60 * time.Second

type LeaseScope struct {
	OwnerID     string
	NodeID      string
	WorkspaceID string
	ThreadID    string
}

type LeaseRecord struct {
	Scope          LeaseScope
	LeaseID        string
	HolderClientID string
	Epoch          int64
	AcquiredAt     time.Time
	ExpiresAt      time.Time
	UpdatedAt      time.Time
	State          string
}

type LeaseAcquireRequest struct {
	Scope         LeaseScope
	ClientID      string
	LeaseID       string
	Force         bool
	ExpectedEpoch *int64
	Now           time.Time
	TTL           time.Duration
}

type LeaseMutationRequest struct {
	Scope    LeaseScope
	ClientID string
	LeaseID  string
	Epoch    int64
	Now      time.Time
	TTL      time.Duration
}

func (s *Store) AcquireLease(ctx context.Context, request LeaseAcquireRequest) (LeaseRecord, error) {
	if err := requireContext(ctx); err != nil {
		return LeaseRecord{}, err
	}
	if !validLeaseScope(request.Scope) || !validLeaseText(request.ClientID, 128) || !validLeaseText(request.LeaseID, 128) || request.Now.IsZero() || request.TTL <= 0 {
		return LeaseRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return LeaseRecord{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return LeaseRecord{}, internal("lease acquire")
	}
	defer tx.Rollback()
	current, exists, err := scanLeaseRow(tx.QueryRowContext(ctx, leaseSelectSQL, request.Scope.OwnerID, request.Scope.NodeID, request.Scope.WorkspaceID, request.Scope.ThreadID), request.Scope)
	if err != nil {
		return LeaseRecord{}, err
	}
	if !exists {
		current = LeaseRecord{Scope: request.Scope, Epoch: 0, State: "none"}
	}
	if current.State == "held" && !current.ExpiresAt.After(request.Now) {
		current.State = "expired"
	}
	if request.ExpectedEpoch != nil && *request.ExpectedEpoch != current.Epoch {
		return current, ErrConflict
	}
	if current.State == "held" && current.HolderClientID != request.ClientID && !request.Force {
		return current, ErrConflict
	}
	if current.State == "held" && current.HolderClientID == request.ClientID && current.LeaseID != "" && !request.Force {
		return current, nil
	}
	next := LeaseRecord{
		Scope:          request.Scope,
		LeaseID:        request.LeaseID,
		HolderClientID: request.ClientID,
		Epoch:          current.Epoch + 1,
		AcquiredAt:     request.Now.UTC(),
		ExpiresAt:      request.Now.UTC().Add(request.TTL),
		UpdatedAt:      request.Now.UTC(),
		State:          "held",
	}
	if exists {
		_, err = tx.ExecContext(ctx, `UPDATE control_leases SET lease_id=?,holder_client_id=?,epoch=?,acquired_at=?,expires_at=?,updated_at=? WHERE owner_id=? AND node_id=? AND workspace_id=? AND thread_id=?`,
			next.LeaseID, next.HolderClientID, next.Epoch, timestamp(next.AcquiredAt), timestamp(next.ExpiresAt), timestamp(next.UpdatedAt), request.Scope.OwnerID, request.Scope.NodeID, request.Scope.WorkspaceID, request.Scope.ThreadID)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO control_leases(owner_id,node_id,workspace_id,thread_id,lease_id,holder_client_id,epoch,acquired_at,expires_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			request.Scope.OwnerID, request.Scope.NodeID, request.Scope.WorkspaceID, request.Scope.ThreadID, next.LeaseID, next.HolderClientID, next.Epoch, timestamp(next.AcquiredAt), timestamp(next.ExpiresAt), timestamp(next.UpdatedAt))
	}
	if err != nil {
		return LeaseRecord{}, internal("lease acquire")
	}
	if err := tx.Commit(); err != nil {
		return LeaseRecord{}, internal("lease acquire")
	}
	return next, nil
}

func (s *Store) RenewLease(ctx context.Context, request LeaseMutationRequest) (LeaseRecord, error) {
	if err := requireContext(ctx); err != nil {
		return LeaseRecord{}, err
	}
	if !validLeaseScope(request.Scope) || !validLeaseText(request.ClientID, 128) || !validLeaseText(request.LeaseID, 128) || request.Epoch < 1 || request.Now.IsZero() || request.TTL <= 0 {
		return LeaseRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return LeaseRecord{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return LeaseRecord{}, internal("lease renew")
	}
	defer tx.Rollback()
	current, exists, err := scanLeaseRow(tx.QueryRowContext(ctx, leaseSelectSQL, request.Scope.OwnerID, request.Scope.NodeID, request.Scope.WorkspaceID, request.Scope.ThreadID), request.Scope)
	if err != nil {
		return LeaseRecord{}, err
	}
	if !exists || current.State != "held" || current.LeaseID != request.LeaseID || current.HolderClientID != request.ClientID || current.Epoch != request.Epoch {
		return current, ErrConflict
	}
	if !current.ExpiresAt.After(request.Now) {
		current.State = "expired"
		return current, ErrExpired
	}
	current.ExpiresAt = request.Now.UTC().Add(request.TTL)
	current.UpdatedAt = request.Now.UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE control_leases SET expires_at=?,updated_at=? WHERE owner_id=? AND node_id=? AND workspace_id=? AND thread_id=? AND lease_id=? AND holder_client_id=? AND epoch=?`,
		timestamp(current.ExpiresAt), timestamp(current.UpdatedAt), request.Scope.OwnerID, request.Scope.NodeID, request.Scope.WorkspaceID, request.Scope.ThreadID, request.LeaseID, request.ClientID, request.Epoch); err != nil {
		return LeaseRecord{}, internal("lease renew")
	}
	if err := tx.Commit(); err != nil {
		return LeaseRecord{}, internal("lease renew")
	}
	return current, nil
}

func (s *Store) ReleaseLease(ctx context.Context, request LeaseMutationRequest) (LeaseRecord, error) {
	if err := requireContext(ctx); err != nil {
		return LeaseRecord{}, err
	}
	if !validLeaseScope(request.Scope) || !validLeaseText(request.ClientID, 128) || !validLeaseText(request.LeaseID, 128) || request.Epoch < 1 || request.Now.IsZero() {
		return LeaseRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return LeaseRecord{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return LeaseRecord{}, internal("lease release")
	}
	defer tx.Rollback()
	current, exists, err := scanLeaseRow(tx.QueryRowContext(ctx, leaseSelectSQL, request.Scope.OwnerID, request.Scope.NodeID, request.Scope.WorkspaceID, request.Scope.ThreadID), request.Scope)
	if err != nil {
		return LeaseRecord{}, err
	}
	if !exists {
		return LeaseRecord{Scope: request.Scope, State: "released"}, nil
	}
	if current.State != "held" || current.LeaseID != request.LeaseID || current.HolderClientID != request.ClientID || current.Epoch != request.Epoch {
		return current, ErrConflict
	}
	current.LeaseID = ""
	current.HolderClientID = ""
	current.AcquiredAt = time.Time{}
	current.ExpiresAt = time.Time{}
	current.Epoch++
	current.UpdatedAt = request.Now.UTC()
	current.State = "released"
	if _, err := tx.ExecContext(ctx, `UPDATE control_leases SET lease_id=NULL,holder_client_id=NULL,epoch=?,acquired_at=NULL,expires_at=NULL,updated_at=? WHERE owner_id=? AND node_id=? AND workspace_id=? AND thread_id=? AND lease_id=? AND holder_client_id=? AND epoch=?`,
		current.Epoch, timestamp(current.UpdatedAt), request.Scope.OwnerID, request.Scope.NodeID, request.Scope.WorkspaceID, request.Scope.ThreadID, request.LeaseID, request.ClientID, request.Epoch); err != nil {
		return LeaseRecord{}, internal("lease release")
	}
	if err := tx.Commit(); err != nil {
		return LeaseRecord{}, internal("lease release")
	}
	return current, nil
}

func (s *Store) Lease(ctx context.Context, scope LeaseScope, now time.Time) (LeaseRecord, error) {
	if err := requireContext(ctx); err != nil {
		return LeaseRecord{}, err
	}
	if !validLeaseScope(scope) || now.IsZero() {
		return LeaseRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return LeaseRecord{}, err
	}
	record, exists, err := scanLeaseRow(db.QueryRowContext(ctx, leaseSelectSQL, scope.OwnerID, scope.NodeID, scope.WorkspaceID, scope.ThreadID), scope)
	if err != nil {
		return LeaseRecord{}, err
	}
	if !exists {
		return LeaseRecord{Scope: scope, State: "none"}, nil
	}
	if record.State == "held" && !record.ExpiresAt.After(now) {
		record.State = "expired"
	}
	return record, nil
}

const leaseSelectSQL = `SELECT owner_id,node_id,workspace_id,thread_id,lease_id,holder_client_id,epoch,acquired_at,expires_at,updated_at FROM control_leases WHERE owner_id=? AND node_id=? AND workspace_id=? AND thread_id=?`

type leaseScanner interface{ Scan(...any) error }

func scanLeaseRow(scanner leaseScanner, scope LeaseScope) (LeaseRecord, bool, error) {
	var record LeaseRecord
	var leaseID, holder, acquired, expires, updated sql.NullString
	err := scanner.Scan(&record.Scope.OwnerID, &record.Scope.NodeID, &record.Scope.WorkspaceID, &record.Scope.ThreadID, &leaseID, &holder, &record.Epoch, &acquired, &expires, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return LeaseRecord{}, false, nil
	}
	if err != nil {
		return LeaseRecord{}, false, internal("lease read")
	}
	if record.Scope != scope {
		return LeaseRecord{}, false, ErrCorrupt
	}
	record.LeaseID, record.HolderClientID = leaseID.String, holder.String
	if acquired.Valid {
		record.AcquiredAt, err = time.Parse(time.RFC3339Nano, acquired.String)
		if err != nil {
			return LeaseRecord{}, false, ErrCorrupt
		}
	}
	if expires.Valid {
		record.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires.String)
		if err != nil {
			return LeaseRecord{}, false, ErrCorrupt
		}
	}
	if !updated.Valid {
		return LeaseRecord{}, false, ErrCorrupt
	}
	record.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated.String)
	if err != nil {
		return LeaseRecord{}, false, ErrCorrupt
	}
	record.State = "released"
	if record.LeaseID != "" && record.HolderClientID != "" {
		record.State = "held"
	}
	return record, true, nil
}

func validLeaseScope(scope LeaseScope) bool {
	return validLeaseText(scope.OwnerID, 128) && validLeaseText(scope.NodeID, 128) && validLeaseText(scope.WorkspaceID, 128) && validLeaseText(scope.ThreadID, 128)
}

func validLeaseText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && !strings.ContainsRune(value, '\x00')
}
