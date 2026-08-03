package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Notification struct {
	ID             string
	OwnerID        string
	NodeID         string
	WorkspaceID    string
	ThreadID       string
	TurnID         string
	Type           string
	Summary        string
	SourceSequence int64
	DedupKey       string
	CreatedAt      time.Time
	ReadAt         *time.Time
}

func (s *Store) SaveNotification(ctx context.Context, notification Notification) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validNotification(notification) {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO notifications(id,owner_id,node_id,workspace_id,thread_id,turn_id,type,summary,source_sequence,dedup_key,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(owner_id,dedup_key) DO NOTHING`,
		notification.ID, notification.OwnerID, notification.NodeID, nullNotificationText(notification.WorkspaceID), nullNotificationText(notification.ThreadID), nullNotificationText(notification.TurnID), notification.Type, notification.Summary, notification.SourceSequence, notification.DedupKey, timestamp(notification.CreatedAt))
	if err != nil {
		return internal("notification save")
	}
	return nil
}

func (s *Store) ListNotifications(ctx context.Context, ownerID string, limit int) ([]Notification, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if !validNotificationText(ownerID, 128) || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id,owner_id,node_id,COALESCE(workspace_id,''),COALESCE(thread_id,''),COALESCE(turn_id,''),type,summary,source_sequence,dedup_key,created_at,COALESCE(read_at,'') FROM notifications WHERE owner_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, internal("notification list")
	}
	defer rows.Close()
	result := make([]Notification, 0, limit)
	for rows.Next() {
		item, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, internal("notification list")
	}
	return result, nil
}

func (s *Store) MarkNotificationRead(ctx context.Context, ownerID, notificationID string, now time.Time) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validNotificationText(ownerID, 128) || !validNotificationText(notificationID, 128) || now.IsZero() {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE notifications SET read_at=COALESCE(read_at,?) WHERE owner_id=? AND id=?`, timestamp(now.UTC()), ownerID, notificationID)
	if err != nil {
		return internal("notification read")
	}
	count, err := result.RowsAffected()
	if err != nil {
		return internal("notification read")
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

type notificationScanner interface{ Scan(...any) error }

func scanNotification(scanner notificationScanner) (Notification, error) {
	var item Notification
	var created, read string
	if err := scanner.Scan(&item.ID, &item.OwnerID, &item.NodeID, &item.WorkspaceID, &item.ThreadID, &item.TurnID, &item.Type, &item.Summary, &item.SourceSequence, &item.DedupKey, &created, &read); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Notification{}, ErrNotFound
		}
		return Notification{}, internal("notification read")
	}
	var err error
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Notification{}, ErrCorrupt
	}
	if read != "" {
		value, err := time.Parse(time.RFC3339Nano, read)
		if err != nil {
			return Notification{}, ErrCorrupt
		}
		item.ReadAt = &value
	}
	return item, nil
}

func validNotification(notification Notification) bool {
	return validNotificationText(notification.ID, 128) && validNotificationText(notification.OwnerID, 128) && validNotificationText(notification.NodeID, 128) && validNotificationText(notification.Type, 64) && validNotificationText(notification.Summary, 512) && validNotificationText(notification.DedupKey, 256) && notification.SourceSequence >= 0 && !notification.CreatedAt.IsZero() && (notification.Type == "task.completed" || notification.Type == "task.failed" || notification.Type == "approval.required" || notification.Type == "node.offline" || notification.Type == "node.online")
}

func validNotificationText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && !containsNUL(value)
}

func nullNotificationText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func containsNUL(value string) bool {
	for _, character := range value {
		if character == 0 {
			return true
		}
	}
	return false
}
