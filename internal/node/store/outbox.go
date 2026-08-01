package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"
)

const MaxOutboxFrameBytes = 1 << 20

type OutboxRecord struct {
	MessageID      string
	StreamID       string
	Sequence       int64
	Frame          []byte
	CreatedAt      time.Time
	AcknowledgedAt time.Time
}

func (s *Store) Enqueue(ctx context.Context, record OutboxRecord) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validOutbox(record) {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	created := record.CreatedAt
	if created.IsZero() {
		created = s.clock()
	}
	_, err = db.ExecContext(ctx, `INSERT INTO outbox(message_id, stream_id, sequence, frame, created_at, acknowledged_at)
		VALUES (?, ?, ?, ?, ?, NULL) ON CONFLICT(message_id) DO NOTHING`, record.MessageID, record.StreamID, record.Sequence, append([]byte(nil), record.Frame...), timestamp(created))
	if err != nil {
		return ErrConflict
	}
	var existing OutboxRecord
	var createdText string
	err = db.QueryRowContext(ctx, `SELECT message_id, stream_id, sequence, frame, created_at FROM outbox WHERE message_id = ?`, record.MessageID).
		Scan(&existing.MessageID, &existing.StreamID, &existing.Sequence, &existing.Frame, &createdText)
	if err != nil {
		return internal("outbox read")
	}
	if existing.StreamID != record.StreamID || existing.Sequence != record.Sequence || !bytes.Equal(existing.Frame, record.Frame) {
		return ErrConflict
	}
	return nil
}

func (s *Store) Pending(ctx context.Context, limit int) ([]OutboxRecord, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT message_id, stream_id, sequence, frame, created_at
		FROM outbox WHERE acknowledged_at IS NULL ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, internal("outbox list")
	}
	defer rows.Close()
	result := make([]OutboxRecord, 0)
	for rows.Next() {
		var record OutboxRecord
		var created string
		if err := rows.Scan(&record.MessageID, &record.StreamID, &record.Sequence, &record.Frame, &created); err != nil {
			return nil, internal("outbox list")
		}
		record.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, ErrCorrupt
		}
		record.Frame = append([]byte(nil), record.Frame...)
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, internal("outbox list")
	}
	return result, nil
}

func (s *Store) Acknowledge(ctx context.Context, messageID string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if messageID == "" || len(messageID) > 128 {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE outbox SET acknowledged_at = COALESCE(acknowledged_at, ?) WHERE message_id = ?`, timestamp(s.clock()), messageID)
	if err != nil {
		return internal("outbox acknowledge")
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return internal("outbox acknowledge")
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) OutboxRecord(ctx context.Context, messageID string) (OutboxRecord, error) {
	if err := requireContext(ctx); err != nil {
		return OutboxRecord{}, err
	}
	if messageID == "" || len(messageID) > 128 {
		return OutboxRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return OutboxRecord{}, err
	}
	var record OutboxRecord
	var created string
	var acknowledged sql.NullString
	err = db.QueryRowContext(ctx, `SELECT message_id, stream_id, sequence, frame, created_at, acknowledged_at FROM outbox WHERE message_id = ?`, messageID).
		Scan(&record.MessageID, &record.StreamID, &record.Sequence, &record.Frame, &created, &acknowledged)
	if errors.Is(err, sql.ErrNoRows) {
		return OutboxRecord{}, ErrNotFound
	}
	if err != nil {
		return OutboxRecord{}, internal("outbox read")
	}
	record.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return OutboxRecord{}, ErrCorrupt
	}
	if acknowledged.Valid {
		record.AcknowledgedAt, err = time.Parse(time.RFC3339Nano, acknowledged.String)
		if err != nil {
			return OutboxRecord{}, ErrCorrupt
		}
	}
	record.Frame = append([]byte(nil), record.Frame...)
	return record, nil
}

func validOutbox(record OutboxRecord) bool {
	return record.MessageID != "" && len(record.MessageID) <= 128 && record.StreamID != "" && len(record.StreamID) <= 128 && record.Sequence >= 0 && record.Sequence <= 9007199254740991 && len(record.Frame) <= MaxOutboxFrameBytes
}
