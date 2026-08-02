package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	ControlReceived    = "received"
	ControlValidated   = "validated"
	ControlDispatching = "dispatching"
	ControlConfirmed   = "confirmed"
	ControlRejected    = "rejected"
	ControlAmbiguous   = "ambiguous"
)

type ControlRecord struct {
	MessageID     string
	RequestDigest []byte
	Type          string
	EventTarget
	State          string
	ErrorCode      string
	ResultStreamID string
	ResultSequence int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (s *Store) CreateControl(ctx context.Context, record ControlRecord) (ControlRecord, error) {
	if err := requireContext(ctx); err != nil {
		return ControlRecord{}, err
	}
	if !validControlRecord(record) || record.State != ControlReceived && record.State != ControlValidated {
		return ControlRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return ControlRecord{}, err
	}
	now := record.CreatedAt
	if now.IsZero() {
		now = s.clock()
	}
	_, err = db.ExecContext(ctx, `INSERT INTO control_requests(message_id,request_digest,control_type,workspace_id,thread_id,turn_id,item_id,state,error_code,result_stream_id,result_sequence,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,NULL,NULL,NULL,?,?) ON CONFLICT(message_id) DO NOTHING`, record.MessageID, append([]byte(nil), record.RequestDigest...), record.Type,
		nullText(record.WorkspaceID), nullText(record.ThreadID), nullText(record.TurnID), nullText(record.ItemID), record.State, timestamp(now), timestamp(now))
	if err != nil {
		return ControlRecord{}, internal("control create")
	}
	existing, err := s.Control(ctx, record.MessageID)
	if err != nil {
		return ControlRecord{}, err
	}
	if !bytes.Equal(existing.RequestDigest, record.RequestDigest) || existing.Type != record.Type || existing.EventTarget != record.EventTarget {
		return ControlRecord{}, ErrConflict
	}
	return existing, nil
}

func (s *Store) TransitionControl(ctx context.Context, messageID, nextState, errorCode, resultStreamID string, resultSequence int64) (ControlRecord, error) {
	if err := requireContext(ctx); err != nil {
		return ControlRecord{}, err
	}
	if !validWorkspaceText(messageID, 128) || !validControlState(nextState) || len(errorCode) > 128 || len(resultStreamID) > 128 || resultSequence < 0 || resultSequence > MaxJavaScriptSequence {
		return ControlRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return ControlRecord{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ControlRecord{}, internal("control transition")
	}
	defer tx.Rollback()
	current, err := scanControl(tx.QueryRowContext(ctx, `SELECT message_id,request_digest,control_type,workspace_id,thread_id,turn_id,item_id,state,error_code,result_stream_id,result_sequence,created_at,updated_at FROM control_requests WHERE message_id=?`, messageID))
	if err != nil {
		return ControlRecord{}, err
	}
	if current.State == nextState {
		return current, tx.Commit()
	}
	if !allowedControlTransition(current.State, nextState) {
		return ControlRecord{}, ErrConflict
	}
	if isTerminalControlState(nextState) && (resultStreamID == "" || resultSequence < 1) {
		return ControlRecord{}, ErrInvalid
	}
	_, err = tx.ExecContext(ctx, `UPDATE control_requests SET state=?,error_code=?,result_stream_id=?,result_sequence=?,updated_at=? WHERE message_id=?`, nextState, nullText(errorCode), nullText(resultStreamID), nullableSequence(resultSequence), timestamp(s.clock()), messageID)
	if err != nil {
		return ControlRecord{}, internal("control transition")
	}
	if err := tx.Commit(); err != nil {
		return ControlRecord{}, internal("control transition")
	}
	return s.Control(ctx, messageID)
}

func (s *Store) Control(ctx context.Context, messageID string) (ControlRecord, error) {
	if err := requireContext(ctx); err != nil {
		return ControlRecord{}, err
	}
	if !validWorkspaceText(messageID, 128) {
		return ControlRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return ControlRecord{}, err
	}
	return scanControl(db.QueryRowContext(ctx, `SELECT message_id,request_digest,control_type,workspace_id,thread_id,turn_id,item_id,state,error_code,result_stream_id,result_sequence,created_at,updated_at FROM control_requests WHERE message_id=?`, messageID))
}

type controlScanner interface{ Scan(...any) error }

func scanControl(scanner controlScanner) (ControlRecord, error) {
	var record ControlRecord
	var workspaceID, threadID, turnID, itemID, errorCode, streamID sql.NullString
	var sequence sql.NullInt64
	var created, updated string
	err := scanner.Scan(&record.MessageID, &record.RequestDigest, &record.Type, &workspaceID, &threadID, &turnID, &itemID, &record.State, &errorCode, &streamID, &sequence, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return ControlRecord{}, ErrNotFound
	}
	if err != nil {
		return ControlRecord{}, internal("control read")
	}
	record.WorkspaceID, record.ThreadID, record.TurnID, record.ItemID = workspaceID.String, threadID.String, turnID.String, itemID.String
	record.ErrorCode, record.ResultStreamID, record.ResultSequence = errorCode.String, streamID.String, sequence.Int64
	record.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return ControlRecord{}, ErrCorrupt
	}
	record.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return ControlRecord{}, ErrCorrupt
	}
	record.RequestDigest = append([]byte(nil), record.RequestDigest...)
	return record, nil
}

func validControlRecord(record ControlRecord) bool {
	return validWorkspaceText(record.MessageID, 128) && len(record.RequestDigest) == 32 && validWorkspaceText(record.Type, 128) && validOptionalEventTarget(record.EventTarget) && validControlState(record.State)
}

func validControlState(value string) bool {
	switch value {
	case ControlReceived, ControlValidated, ControlDispatching, ControlConfirmed, ControlRejected, ControlAmbiguous:
		return true
	default:
		return false
	}
}

func isTerminalControlState(value string) bool {
	return value == ControlConfirmed || value == ControlRejected || value == ControlAmbiguous
}

func allowedControlTransition(current, next string) bool {
	if isTerminalControlState(current) {
		return false
	}
	switch current {
	case ControlReceived:
		return next == ControlValidated || next == ControlRejected
	case ControlValidated:
		return next == ControlDispatching || next == ControlRejected
	case ControlDispatching:
		return isTerminalControlState(next)
	default:
		return false
	}
}

func nullableSequence(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}
