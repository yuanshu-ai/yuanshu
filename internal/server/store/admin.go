package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type SecuritySettings struct {
	OwnerID               string    `json:"-"`
	ControlPairingEnabled bool      `json:"controlPairingEnabled"`
	NodeEnrollmentEnabled bool      `json:"nodeEnrollmentEnabled"`
	Revision              int64     `json:"revision"`
	UpdatedAt             time.Time `json:"updatedAt"`
	UpdatedBy             string    `json:"updatedBy"`
}

type AdminAccessRequest struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	NodeID    string    `json:"nodeId"`
	Name      string    `json:"name,omitempty"`
	OS        string    `json:"os,omitempty"`
	Version   string    `json:"version,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type AdminLease struct {
	Scope          LeaseScope `json:"scope"`
	LeaseID        string     `json:"leaseId"`
	HolderClientID string     `json:"holderClientId"`
	Epoch          int64      `json:"epoch"`
	AcquiredAt     time.Time  `json:"acquiredAt"`
	ExpiresAt      time.Time  `json:"expiresAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type AdminAudit struct {
	ID            string    `json:"id"`
	OwnerID       string    `json:"-"`
	ActorClientID string    `json:"actorClientId"`
	Action        string    `json:"action"`
	ResourceType  string    `json:"resourceType"`
	ResourceRef   string    `json:"resourceRef"`
	Result        string    `json:"result"`
	ErrorCode     string    `json:"errorCode,omitempty"`
	CorrelationID string    `json:"correlationId"`
	CreatedAt     time.Time `json:"createdAt"`
}

type AdminCounts struct {
	ActiveNodes          int `json:"activeNodes"`
	ActiveControlClients int `json:"activeControlClients"`
	PendingPairings      int `json:"pendingPairings"`
	PendingEnrollments   int `json:"pendingEnrollments"`
	ActiveLeases         int `json:"activeLeases"`
	UnreadNotifications  int `json:"unreadNotifications"`
	RecentFailures       int `json:"recentFailures"`
}

func (s *Store) SecuritySettings(ctx context.Context, ownerID string) (SecuritySettings, error) {
	if err := requireContext(ctx); err != nil {
		return SecuritySettings{}, err
	}
	if ownerID == "" {
		return SecuritySettings{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return SecuritySettings{}, err
	}
	now := s.clock().UTC()
	if _, err := db.ExecContext(ctx, `INSERT INTO server_security_settings(owner_id,control_pairing_enabled,node_enrollment_enabled,revision,updated_at,updated_by)
		VALUES(?,1,1,1,?,'system') ON CONFLICT(owner_id) DO NOTHING`, ownerID, timestamp(now)); err != nil {
		return SecuritySettings{}, internal("security settings initialize")
	}
	return readSecuritySettings(ctx, db, ownerID)
}

func (s *Store) UpdateSecuritySettings(ctx context.Context, ownerID, actor string, pairing, enrollment bool, baseRevision int64, now time.Time) (SecuritySettings, error) {
	if err := requireContext(ctx); err != nil {
		return SecuritySettings{}, err
	}
	if ownerID == "" || actor == "" || baseRevision < 1 || now.IsZero() {
		return SecuritySettings{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return SecuritySettings{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return SecuritySettings{}, internal("security settings update")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO server_security_settings(owner_id,control_pairing_enabled,node_enrollment_enabled,revision,updated_at,updated_by)
		VALUES(?,1,1,1,?,'system') ON CONFLICT(owner_id) DO NOTHING`, ownerID, timestamp(now)); err != nil {
		return SecuritySettings{}, internal("security settings update")
	}
	result, err := tx.ExecContext(ctx, `UPDATE server_security_settings SET control_pairing_enabled=?,node_enrollment_enabled=?,revision=revision+1,updated_at=?,updated_by=? WHERE owner_id=? AND revision=?`, boolInt(pairing), boolInt(enrollment), timestamp(now), actor, ownerID, baseRevision)
	if err != nil {
		return SecuritySettings{}, internal("security settings update")
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return SecuritySettings{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return SecuritySettings{}, internal("security settings update")
	}
	return readSecuritySettings(ctx, db, ownerID)
}

func readSecuritySettings(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ownerID string) (SecuritySettings, error) {
	var item SecuritySettings
	var pairing, enrollment int
	var updated string
	err := query.QueryRowContext(ctx, `SELECT owner_id,control_pairing_enabled,node_enrollment_enabled,revision,updated_at,updated_by FROM server_security_settings WHERE owner_id=?`, ownerID).Scan(&item.OwnerID, &pairing, &enrollment, &item.Revision, &updated, &item.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return SecuritySettings{}, ErrNotFound
	}
	if err != nil {
		return SecuritySettings{}, internal("security settings read")
	}
	item.ControlPairingEnabled, item.NodeEnrollmentEnabled = pairing == 1, enrollment == 1
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return SecuritySettings{}, ErrCorrupt
	}
	return item, nil
}

func (s *Store) TouchNode(ctx context.Context, ownerID, nodeID string, now time.Time) error {
	return s.touch(ctx, `UPDATE nodes SET last_seen_at=? WHERE owner_id=? AND id=? AND status='active'`, ownerID, nodeID, now)
}

func (s *Store) TouchControlClient(ctx context.Context, ownerID, clientID string, now time.Time) error {
	return s.touch(ctx, `UPDATE control_clients SET last_seen_at=? WHERE owner_id=? AND id=? AND status='active'`, ownerID, clientID, now)
}

func (s *Store) touch(ctx context.Context, statement, ownerID, subjectID string, now time.Time) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if ownerID == "" || subjectID == "" || now.IsZero() {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, statement, timestamp(now), ownerID, subjectID)
	if err != nil {
		return internal("last seen update")
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AdminAccessRequests(ctx context.Context, ownerID string, now time.Time) ([]AdminAccessRequest, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if ownerID == "" || now.IsZero() {
		return nil, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id,'control_client',node_id,COALESCE(client_name,''),'','',status,created_at,expires_at FROM pairings WHERE owner_id=? AND status IN ('pending','claimed')
		UNION ALL SELECT id,'node',issuer_node_id,COALESCE(name,''),COALESCE(os,''),COALESCE(version,''),status,created_at,expires_at FROM node_enrollments WHERE owner_id=? AND status IN ('pending','claimed') ORDER BY created_at DESC`, ownerID, ownerID)
	if err != nil {
		return nil, internal("admin access requests read")
	}
	defer rows.Close()
	var result []AdminAccessRequest
	for rows.Next() {
		var item AdminAccessRequest
		var created, expires string
		if err := rows.Scan(&item.ID, &item.Kind, &item.NodeID, &item.Name, &item.OS, &item.Version, &item.Status, &created, &expires); err != nil {
			return nil, internal("admin access requests read")
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, ErrCorrupt
		}
		item.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
		if err != nil {
			return nil, ErrCorrupt
		}
		if now.After(item.ExpiresAt) {
			continue
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) AdminLeases(ctx context.Context, ownerID string, now time.Time) ([]AdminLease, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if ownerID == "" || now.IsZero() {
		return nil, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT owner_id,node_id,workspace_id,thread_id,lease_id,holder_client_id,epoch,acquired_at,expires_at,updated_at FROM control_leases WHERE owner_id=? AND lease_id IS NOT NULL AND expires_at>? ORDER BY expires_at`, ownerID, timestamp(now))
	if err != nil {
		return nil, internal("admin leases read")
	}
	defer rows.Close()
	var result []AdminLease
	for rows.Next() {
		var item AdminLease
		var acquired, expires, updated string
		if err := rows.Scan(&item.Scope.OwnerID, &item.Scope.NodeID, &item.Scope.WorkspaceID, &item.Scope.ThreadID, &item.LeaseID, &item.HolderClientID, &item.Epoch, &acquired, &expires, &updated); err != nil {
			return nil, internal("admin leases read")
		}
		item.AcquiredAt, err = time.Parse(time.RFC3339Nano, acquired)
		if err != nil {
			return nil, ErrCorrupt
		}
		item.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
		if err != nil {
			return nil, ErrCorrupt
		}
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, ErrCorrupt
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) AdminReleaseLease(ctx context.Context, scope LeaseScope, expectedEpoch int64, now time.Time) (LeaseRecord, error) {
	if err := requireContext(ctx); err != nil {
		return LeaseRecord{}, err
	}
	if !validLeaseScope(scope) || expectedEpoch < 1 || now.IsZero() {
		return LeaseRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return LeaseRecord{}, err
	}
	result, err := db.ExecContext(ctx, `UPDATE control_leases SET lease_id=NULL,holder_client_id=NULL,acquired_at=NULL,expires_at=NULL,epoch=epoch+1,updated_at=? WHERE owner_id=? AND node_id=? AND workspace_id=? AND thread_id=? AND lease_id IS NOT NULL AND epoch=?`, timestamp(now), scope.OwnerID, scope.NodeID, scope.WorkspaceID, scope.ThreadID, expectedEpoch)
	if err != nil {
		return LeaseRecord{}, internal("admin lease release")
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return LeaseRecord{}, ErrConflict
	}
	return s.Lease(ctx, scope, now)
}

func (s *Store) CancelAdminAccessRequest(ctx context.Context, ownerID, kind, id string, now time.Time) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if ownerID == "" || id == "" || now.IsZero() {
		return ErrInvalid
	}
	table := "pairings"
	if kind == "node" {
		table = "node_enrollments"
	} else if kind != "control_client" {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE `+table+` SET status='expired',resolved_at=? WHERE owner_id=? AND id=? AND status IN ('pending','claimed')`, timestamp(now), ownerID, id)
	if err != nil {
		return internal("admin access request cancel")
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AdminRevokeNode(ctx context.Context, ownerID, nodeID string, now time.Time) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if ownerID == "" || nodeID == "" || now.IsZero() {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return internal("admin node revoke")
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE owner_id=? AND status='active'`, ownerID).Scan(&active); err != nil {
		return internal("admin node revoke")
	}
	if active <= 1 {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE nodes SET status='revoked' WHERE owner_id=? AND id=? AND status='active'`, ownerID, nodeID)
	if err != nil {
		return internal("admin node revoke")
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE node_credentials SET status='revoked',revoked_at=? WHERE node_id=?`, timestamp(now), nodeID); err != nil {
		return internal("admin node revoke")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE node_enrollments SET status='expired',resolved_at=? WHERE issuer_node_id=? AND status IN ('pending','claimed')`, timestamp(now), nodeID); err != nil {
		return internal("admin node revoke")
	}
	return tx.Commit()
}

func (s *Store) AdminRevokeControlClient(ctx context.Context, ownerID, clientID string, now time.Time) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if ownerID == "" || clientID == "" || now.IsZero() {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return internal("admin control client revoke")
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM control_clients WHERE owner_id=? AND status='active'`, ownerID).Scan(&active); err != nil {
		return internal("admin control client revoke")
	}
	if active <= 1 {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE control_clients SET status='revoked',revoked_at=? WHERE owner_id=? AND id=? AND status='active'`, timestamp(now), ownerID, clientID)
	if err != nil {
		return internal("admin control client revoke")
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE owners SET trust_revision=trust_revision+1 WHERE id=?`, ownerID); err != nil {
		return internal("admin control client revoke")
	}
	return tx.Commit()
}

func (s *Store) SaveAdminAudit(ctx context.Context, item AdminAudit) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if item.ID == "" || item.OwnerID == "" || item.ActorClientID == "" || item.Action == "" || item.ResourceType == "" || item.ResourceRef == "" || item.CorrelationID == "" || item.CreatedAt.IsZero() || (item.Result != "succeeded" && item.Result != "rejected" && item.Result != "failed") {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	var code any
	if item.ErrorCode != "" {
		code = item.ErrorCode
	}
	_, err = db.ExecContext(ctx, `INSERT INTO admin_audit_logs(id,owner_id,actor_client_id,action,resource_type,resource_ref,result,error_code,correlation_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, item.ID, item.OwnerID, item.ActorClientID, item.Action, item.ResourceType, item.ResourceRef, item.Result, code, item.CorrelationID, timestamp(item.CreatedAt))
	if err != nil {
		return internal("admin audit write")
	}
	return nil
}

func (s *Store) ListAdminAudit(ctx context.Context, ownerID string, limit int) ([]AdminAudit, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if ownerID == "" || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id,owner_id,actor_client_id,action,resource_type,resource_ref,result,COALESCE(error_code,''),correlation_id,created_at FROM admin_audit_logs WHERE owner_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, internal("admin audit read")
	}
	defer rows.Close()
	var result []AdminAudit
	for rows.Next() {
		var item AdminAudit
		var created string
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.ActorClientID, &item.Action, &item.ResourceType, &item.ResourceRef, &item.Result, &item.ErrorCode, &item.CorrelationID, &created); err != nil {
			return nil, internal("admin audit read")
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, ErrCorrupt
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) PurgeAdminAudit(ctx context.Context, before time.Time) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if before.IsZero() {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM admin_audit_logs WHERE created_at<?`, timestamp(before)); err != nil {
		return internal("admin audit purge")
	}
	return nil
}

func (s *Store) AdminCounts(ctx context.Context, ownerID string, now time.Time) (AdminCounts, error) {
	if err := requireContext(ctx); err != nil {
		return AdminCounts{}, err
	}
	if ownerID == "" || now.IsZero() {
		return AdminCounts{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return AdminCounts{}, err
	}
	var c AdminCounts
	queries := []struct {
		q    string
		out  *int
		args []any
	}{
		{`SELECT COUNT(*) FROM nodes WHERE owner_id=? AND status='active'`, &c.ActiveNodes, []any{ownerID}},
		{`SELECT COUNT(*) FROM control_clients WHERE owner_id=? AND status='active'`, &c.ActiveControlClients, []any{ownerID}},
		{`SELECT COUNT(*) FROM pairings WHERE owner_id=? AND status IN ('pending','claimed') AND expires_at>?`, &c.PendingPairings, []any{ownerID, timestamp(now)}},
		{`SELECT COUNT(*) FROM node_enrollments WHERE owner_id=? AND status IN ('pending','claimed') AND expires_at>?`, &c.PendingEnrollments, []any{ownerID, timestamp(now)}},
		{`SELECT COUNT(*) FROM control_leases WHERE owner_id=? AND lease_id IS NOT NULL AND expires_at>?`, &c.ActiveLeases, []any{ownerID, timestamp(now)}},
		{`SELECT COUNT(*) FROM notifications WHERE owner_id=? AND read_at IS NULL`, &c.UnreadNotifications, []any{ownerID}},
		{`SELECT COUNT(*) FROM admin_audit_logs WHERE owner_id=? AND result!='succeeded' AND created_at>?`, &c.RecentFailures, []any{ownerID, timestamp(now.Add(-24 * time.Hour))}},
	}
	for _, item := range queries {
		if err := db.QueryRowContext(ctx, item.q, item.args...).Scan(item.out); err != nil {
			return AdminCounts{}, internal("admin counts read")
		}
	}
	return c, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
