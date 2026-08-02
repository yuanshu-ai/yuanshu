package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	MaxEventFrameBytes    = 1 << 20
	MaxSnapshotBytes      = 768 << 10
	MaxReplayRecords      = 1000
	MaxJavaScriptSequence = int64(9007199254740991)
)

type EventBinding struct {
	OwnerID  string
	NodeID   string
	StreamID string
}

type EventTarget struct {
	WorkspaceID string
	ThreadID    string
	TurnID      string
	ItemID      string
}

type EventRetention struct {
	MaxAge   time.Duration
	MaxBytes int64
}

type EventRecord struct {
	EventBinding
	EventTarget
	Sequence  int64
	MessageID string
	Type      string
	Frame     []byte
	CreatedAt time.Time
}

type EventHead struct {
	EarliestSequence int64
	LatestSequence   int64
	TotalBytes       int64
}

type EventFrameBuilder func(sequence int64) (messageID string, frame []byte, err error)

type ControlEventMutation struct {
	MessageID string
	State     string
	ErrorCode string
}

// AppendEvent allocates the next sequence and atomically persists the event and
// its outbound frame. The builder is called inside the transaction so the
// sequence encoded on the wire is the sequence that is committed.
func (s *Store) AppendEvent(ctx context.Context, binding EventBinding, target EventTarget, eventType string, createdAt time.Time, retention EventRetention, build EventFrameBuilder) (EventRecord, error) {
	return s.appendEvent(ctx, binding, target, eventType, createdAt, retention, nil, build)
}

// AppendControlEvent commits a terminal control transition with its event log
// and outbox frame in the same SQLite transaction.
func (s *Store) AppendControlEvent(ctx context.Context, binding EventBinding, target EventTarget, eventType string, createdAt time.Time, retention EventRetention, mutation ControlEventMutation, build EventFrameBuilder) (EventRecord, error) {
	if !validWorkspaceText(mutation.MessageID, 128) || !isTerminalControlState(mutation.State) || len(mutation.ErrorCode) > 128 {
		return EventRecord{}, ErrInvalid
	}
	return s.appendEvent(ctx, binding, target, eventType, createdAt, retention, &mutation, build)
}

func (s *Store) appendEvent(ctx context.Context, binding EventBinding, target EventTarget, eventType string, createdAt time.Time, retention EventRetention, mutation *ControlEventMutation, build EventFrameBuilder) (EventRecord, error) {
	if err := requireContext(ctx); err != nil {
		return EventRecord{}, err
	}
	if !validEventBinding(binding) || !validOptionalEventTarget(target) || !validWorkspaceText(eventType, 128) || build == nil || retention.MaxAge <= 0 || retention.MaxBytes < MaxEventFrameBytes {
		return EventRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return EventRecord{}, err
	}
	if createdAt.IsZero() {
		createdAt = s.clock()
	}
	createdAt = createdAt.UTC()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return EventRecord{}, internal("event append")
	}
	defer tx.Rollback()
	nowText := timestamp(createdAt)
	if _, err := tx.ExecContext(ctx, `INSERT INTO event_streams(owner_id,node_id,stream_id,latest_sequence,total_bytes,updated_at)
		VALUES(?,?,?,0,0,?) ON CONFLICT(owner_id,node_id,stream_id) DO NOTHING`, binding.OwnerID, binding.NodeID, binding.StreamID, nowText); err != nil {
		return EventRecord{}, internal("event append")
	}
	var latest, total int64
	if err := tx.QueryRowContext(ctx, `SELECT latest_sequence,total_bytes FROM event_streams WHERE owner_id=? AND node_id=? AND stream_id=?`, binding.OwnerID, binding.NodeID, binding.StreamID).Scan(&latest, &total); err != nil {
		return EventRecord{}, internal("event append")
	}
	if latest >= MaxJavaScriptSequence {
		return EventRecord{}, ErrConflict
	}
	sequence := latest + 1
	messageID, frame, err := build(sequence)
	if err != nil || !validWorkspaceText(messageID, 128) || len(frame) > MaxEventFrameBytes {
		return EventRecord{}, ErrInvalid
	}
	frame = append([]byte(nil), frame...)
	values := []any{binding.OwnerID, binding.NodeID, binding.StreamID, sequence, messageID, eventType,
		nullText(target.WorkspaceID), nullText(target.ThreadID), nullText(target.TurnID), nullText(target.ItemID), frame, len(frame), nowText}
	if _, err := tx.ExecContext(ctx, `INSERT INTO event_log(owner_id,node_id,stream_id,sequence,message_id,event_type,workspace_id,thread_id,turn_id,item_id,frame,frame_bytes,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, values...); err != nil {
		return EventRecord{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox(message_id,stream_id,sequence,frame,created_at,acknowledged_at) VALUES(?,?,?,?,?,NULL)`, messageID, binding.StreamID, sequence, frame, nowText); err != nil {
		return EventRecord{}, ErrConflict
	}
	total += int64(len(frame))
	if _, err := tx.ExecContext(ctx, `UPDATE event_streams SET latest_sequence=?,total_bytes=?,updated_at=? WHERE owner_id=? AND node_id=? AND stream_id=?`, sequence, total, nowText, binding.OwnerID, binding.NodeID, binding.StreamID); err != nil {
		return EventRecord{}, internal("event append")
	}
	if mutation != nil {
		result, err := tx.ExecContext(ctx, `UPDATE control_requests SET state=?,error_code=?,result_stream_id=?,result_sequence=?,updated_at=?
			WHERE message_id=? AND state='dispatching'`, mutation.State, nullText(mutation.ErrorCode), binding.StreamID, sequence, nowText, mutation.MessageID)
		if err != nil {
			return EventRecord{}, internal("control result append")
		}
		rows, err := result.RowsAffected()
		if err != nil || rows != 1 {
			return EventRecord{}, ErrConflict
		}
	}
	if err := pruneEvents(ctx, tx, binding, createdAt.Add(-retention.MaxAge), retention.MaxBytes, &total); err != nil {
		return EventRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE event_streams SET total_bytes=? WHERE owner_id=? AND node_id=? AND stream_id=?`, total, binding.OwnerID, binding.NodeID, binding.StreamID); err != nil {
		return EventRecord{}, internal("event append")
	}
	if err := tx.Commit(); err != nil {
		return EventRecord{}, internal("event append")
	}
	return EventRecord{EventBinding: binding, EventTarget: target, Sequence: sequence, MessageID: messageID, Type: eventType, Frame: frame, CreatedAt: createdAt}, nil
}

func pruneEvents(ctx context.Context, tx *sql.Tx, binding EventBinding, cutoff time.Time, maxBytes int64, total *int64) error {
	for {
		var sequence, size int64
		var messageID, createdText string
		err := tx.QueryRowContext(ctx, `SELECT sequence,message_id,frame_bytes,created_at FROM event_log
			WHERE owner_id=? AND node_id=? AND stream_id=? ORDER BY sequence LIMIT 1`, binding.OwnerID, binding.NodeID, binding.StreamID).Scan(&sequence, &messageID, &size, &createdText)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return internal("event retention")
		}
		created, err := time.Parse(time.RFC3339Nano, createdText)
		if err != nil {
			return ErrCorrupt
		}
		if !created.Before(cutoff) && *total <= maxBytes {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE message_id=?`, messageID); err != nil {
			return internal("event retention")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM event_log WHERE owner_id=? AND node_id=? AND stream_id=? AND sequence=?`, binding.OwnerID, binding.NodeID, binding.StreamID, sequence); err != nil {
			return internal("event retention")
		}
		*total -= size
	}
}

func (s *Store) ReplayEvents(ctx context.Context, binding EventBinding, afterSequence int64, limit int) ([]EventRecord, EventHead, error) {
	if err := requireContext(ctx); err != nil {
		return nil, EventHead{}, err
	}
	if !validEventBinding(binding) || afterSequence < 0 || afterSequence > MaxJavaScriptSequence || limit < 1 || limit > MaxReplayRecords {
		return nil, EventHead{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return nil, EventHead{}, err
	}
	head, err := readEventHead(ctx, db, binding)
	if err != nil {
		return nil, EventHead{}, err
	}
	if afterSequence > head.LatestSequence {
		return nil, EventHead{}, ErrConflict
	}
	rows, err := db.QueryContext(ctx, `SELECT sequence,message_id,event_type,workspace_id,thread_id,turn_id,item_id,frame,created_at
		FROM event_log WHERE owner_id=? AND node_id=? AND stream_id=? AND sequence>? ORDER BY sequence LIMIT ?`, binding.OwnerID, binding.NodeID, binding.StreamID, afterSequence, limit)
	if err != nil {
		return nil, EventHead{}, internal("event replay")
	}
	defer rows.Close()
	records := make([]EventRecord, 0)
	for rows.Next() {
		var record EventRecord
		var workspaceID, threadID, turnID, itemID sql.NullString
		var created string
		if err := rows.Scan(&record.Sequence, &record.MessageID, &record.Type, &workspaceID, &threadID, &turnID, &itemID, &record.Frame, &created); err != nil {
			return nil, EventHead{}, internal("event replay")
		}
		record.EventBinding = binding
		record.WorkspaceID, record.ThreadID, record.TurnID, record.ItemID = workspaceID.String, threadID.String, turnID.String, itemID.String
		record.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, EventHead{}, ErrCorrupt
		}
		record.Frame = append([]byte(nil), record.Frame...)
		records = append(records, record)
	}
	if rows.Err() != nil {
		return nil, EventHead{}, internal("event replay")
	}
	return records, head, nil
}

func (s *Store) EventHead(ctx context.Context, binding EventBinding) (EventHead, error) {
	if err := requireContext(ctx); err != nil {
		return EventHead{}, err
	}
	if !validEventBinding(binding) {
		return EventHead{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return EventHead{}, err
	}
	return readEventHead(ctx, db, binding)
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readEventHead(ctx context.Context, query rowQueryer, binding EventBinding) (EventHead, error) {
	var head EventHead
	err := query.QueryRowContext(ctx, `SELECT latest_sequence,total_bytes,
		COALESCE((SELECT MIN(sequence) FROM event_log WHERE owner_id=? AND node_id=? AND stream_id=?),0)
		FROM event_streams WHERE owner_id=? AND node_id=? AND stream_id=?`, binding.OwnerID, binding.NodeID, binding.StreamID, binding.OwnerID, binding.NodeID, binding.StreamID).
		Scan(&head.LatestSequence, &head.TotalBytes, &head.EarliestSequence)
	if errors.Is(err, sql.ErrNoRows) {
		return EventHead{}, nil
	}
	if err != nil {
		return EventHead{}, internal("event head")
	}
	return head, nil
}

func (s *Store) AcknowledgeEvents(ctx context.Context, binding EventBinding, sequence int64) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validEventBinding(binding) || sequence < 0 || sequence > MaxJavaScriptSequence {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return internal("event acknowledge")
	}
	defer tx.Rollback()
	head, err := readEventHead(ctx, tx, binding)
	if err != nil || sequence > head.LatestSequence {
		if err != nil {
			return err
		}
		return ErrConflict
	}
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT acknowledged_sequence FROM event_cursors WHERE owner_id=? AND node_id=? AND stream_id=?`, binding.OwnerID, binding.NodeID, binding.StreamID).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return internal("event acknowledge")
	}
	if sequence < current {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO event_cursors(owner_id,node_id,stream_id,acknowledged_sequence,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(owner_id,node_id,stream_id) DO UPDATE SET acknowledged_sequence=excluded.acknowledged_sequence,updated_at=excluded.updated_at`, binding.OwnerID, binding.NodeID, binding.StreamID, sequence, timestamp(s.clock())); err != nil {
		return internal("event acknowledge")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE outbox SET acknowledged_at=COALESCE(acknowledged_at,?) WHERE stream_id=? AND sequence<=?`, timestamp(s.clock()), binding.StreamID, sequence); err != nil {
		return internal("event acknowledge")
	}
	if err := tx.Commit(); err != nil {
		return internal("event acknowledge")
	}
	return nil
}

type SnapshotRecord struct {
	WorkspaceID    string
	ThreadID       string
	Status         string
	LatestSequence int64
	Payload        []byte
	UpdatedAt      time.Time
}

func (s *Store) SaveSnapshot(ctx context.Context, record SnapshotRecord) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validWorkspaceText(record.WorkspaceID, 128) || !validWorkspaceText(record.ThreadID, 128) || !validWorkspaceText(record.Status, 64) || record.LatestSequence < 0 || record.LatestSequence > MaxJavaScriptSequence || len(record.Payload) == 0 || len(record.Payload) > MaxSnapshotBytes {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	when := record.UpdatedAt
	if when.IsZero() {
		when = s.clock()
	}
	_, err = db.ExecContext(ctx, `INSERT INTO thread_snapshots(thread_id,workspace_id,status,latest_sequence,payload,updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(thread_id) DO UPDATE SET workspace_id=excluded.workspace_id,status=excluded.status,latest_sequence=excluded.latest_sequence,payload=excluded.payload,updated_at=excluded.updated_at`, record.ThreadID, record.WorkspaceID, record.Status, record.LatestSequence, append([]byte(nil), record.Payload...), timestamp(when))
	if err != nil {
		return internal("snapshot save")
	}
	return nil
}

func (s *Store) Snapshot(ctx context.Context, threadID string) (SnapshotRecord, error) {
	if err := requireContext(ctx); err != nil {
		return SnapshotRecord{}, err
	}
	if !validWorkspaceText(threadID, 128) {
		return SnapshotRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return SnapshotRecord{}, err
	}
	var record SnapshotRecord
	var updated string
	err = db.QueryRowContext(ctx, `SELECT workspace_id,thread_id,status,latest_sequence,payload,updated_at FROM thread_snapshots WHERE thread_id=?`, threadID).
		Scan(&record.WorkspaceID, &record.ThreadID, &record.Status, &record.LatestSequence, &record.Payload, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return SnapshotRecord{}, ErrNotFound
	}
	if err != nil {
		return SnapshotRecord{}, internal("snapshot read")
	}
	record.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return SnapshotRecord{}, ErrCorrupt
	}
	record.Payload = append([]byte(nil), record.Payload...)
	return record, nil
}

func validEventBinding(value EventBinding) bool {
	return validWorkspaceText(value.OwnerID, 128) && validWorkspaceText(value.NodeID, 128) && validWorkspaceText(value.StreamID, 128)
}

func validOptionalEventTarget(value EventTarget) bool {
	return (value.WorkspaceID == "" || validWorkspaceText(value.WorkspaceID, 128)) &&
		(value.ThreadID == "" || validWorkspaceText(value.ThreadID, 128)) &&
		(value.TurnID == "" || validWorkspaceText(value.TurnID, 128)) &&
		(value.ItemID == "" || validWorkspaceText(value.ItemID, 128))
}

func nullText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
