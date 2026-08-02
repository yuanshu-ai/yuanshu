// Package eventlog persists bounded Protocol v1 events and owns Node-side
// replay, snapshot, and runtime reconciliation semantics.
package eventlog

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	protocol "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
	"github.com/yuanshu-ai/yuanshu/internal/security/credential"
)

const (
	DefaultStreamID    = "node-events-v1"
	DefaultReplayLimit = 256
	maxTextBytes       = 256 << 10
	maxSnapshotItems   = 50
)

var (
	ErrInvalid           = errors.New("node event log argument is invalid")
	ErrHistoryGap        = errors.New("node event history has a gap")
	ErrSequenceExhausted = errors.New("node event sequence is exhausted")
	ErrControlFinalized  = errors.New("node control result is already final")
)

type Options struct {
	OwnerID  string
	NodeID   string
	StreamID string
	MaxAge   time.Duration
	MaxBytes int64
	Clock    func() time.Time
	Random   io.Reader
}

type Manager struct {
	store     *store.Store
	binding   store.EventBinding
	retention store.EventRetention
	clock     func() time.Time
	random    io.Reader
	mu        sync.Mutex
}

type Record = store.EventRecord

type ReplayBatch struct {
	Records          []Record
	EarliestSequence int64
	LatestSequence   int64
	Gap              bool
	HasMore          bool
}

type SnapshotTarget struct {
	WorkspaceID string
	ThreadID    string
}

type ReconcileReport struct {
	Confirmed int
	Ambiguous int
	Deferred  int
}

func NewManager(local *store.Store, options Options) (*Manager, error) {
	if local == nil || !validID(options.OwnerID) || !validID(options.NodeID) || options.MaxAge <= 0 || options.MaxBytes < store.MaxEventFrameBytes {
		return nil, ErrInvalid
	}
	if options.StreamID == "" {
		options.StreamID = DefaultStreamID
	}
	if !validID(options.StreamID) {
		return nil, ErrInvalid
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	return &Manager{
		store: local, binding: store.EventBinding{OwnerID: options.OwnerID, NodeID: options.NodeID, StreamID: options.StreamID},
		retention: store.EventRetention{MaxAge: options.MaxAge, MaxBytes: options.MaxBytes}, clock: options.Clock, random: options.Random,
	}, nil
}

func (m *Manager) Publish(ctx context.Context, event adapter.AgentEvent) ([]Record, error) {
	if ctx == nil || ctx.Err() != nil {
		if ctx == nil {
			return nil, context.Canceled
		}
		return nil, ctx.Err()
	}
	specs, err := normalizeEvent(event)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	records := make([]Record, 0, len(specs))
	for _, spec := range specs {
		record, err := m.append(ctx, spec)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
		_ = m.updateSnapshot(ctx, record, spec.payload)
		_ = m.updateApproval(ctx, record, spec.payload)
	}
	return records, nil
}

type eventSpec struct {
	kind          protocol.EventType
	target        store.EventTarget
	correlationID string
	payload       map[string]any
}

func (m *Manager) append(ctx context.Context, spec eventSpec) (Record, error) {
	return m.appendWithControl(ctx, spec, nil)
}

func (m *Manager) appendWithControl(ctx context.Context, spec eventSpec, mutation *store.ControlEventMutation) (Record, error) {
	createdAt := m.clock().UTC()
	build := func(sequence int64) (string, []byte, error) {
		messageID, err := m.messageID()
		if err != nil {
			return "", nil, err
		}
		correlationID := spec.correlationID
		if correlationID == "" {
			correlationID = messageID
		}
		message := protocol.YuanshuMessage{
			ProtocolVersion: protocol.CurrentVersion, MessageID: messageID, Type: string(spec.kind), SentAt: createdAt.Format(time.RFC3339Nano),
			OwnerID: m.binding.OwnerID, NodeID: m.binding.NodeID, StreamID: m.binding.StreamID, Sequence: sequence, CorrelationID: correlationID,
			WorkspaceID: pointer(spec.target.WorkspaceID), ThreadID: pointer(spec.target.ThreadID), TurnID: pointer(spec.target.TurnID), ItemID: pointer(spec.target.ItemID),
			Payload: clonePayload(spec.payload),
		}
		if spec.kind == protocol.EventApprovalRequested {
			digest, err := protocol.ApprovalOperationDigest(message)
			if err != nil {
				return "", nil, err
			}
			message.Payload["operationDigest"] = digest
			spec.payload["operationDigest"] = digest
		}
		if spec.kind == protocol.EventThreadSnapshot {
			message.Payload["latestSequence"] = sequence
			spec.payload["latestSequence"] = sequence
		}
		raw, err := protocol.MarshalEvent(message)
		return messageID, raw, err
	}
	if mutation != nil {
		return m.store.AppendControlEvent(ctx, m.binding, spec.target, string(spec.kind), createdAt, m.retention, *mutation, build)
	}
	return m.store.AppendEvent(ctx, m.binding, spec.target, string(spec.kind), createdAt, m.retention, build)
}

func (m *Manager) Replay(ctx context.Context, afterSequence int64, limit int) (ReplayBatch, error) {
	if limit == 0 {
		limit = DefaultReplayLimit
	}
	records, head, err := m.store.ReplayEvents(ctx, m.binding, afterSequence, limit)
	if err != nil {
		return ReplayBatch{}, classifyStore(err)
	}
	gap := head.EarliestSequence > 0 && afterSequence+1 < head.EarliestSequence
	return ReplayBatch{
		Records: records, EarliestSequence: head.EarliestSequence, LatestSequence: head.LatestSequence,
		Gap: gap, HasMore: len(records) > 0 && records[len(records)-1].Sequence < head.LatestSequence,
	}, nil
}

func (m *Manager) Acknowledge(ctx context.Context, sequence int64) error {
	return classifyStore(m.store.AcknowledgeEvents(ctx, m.binding, sequence))
}

func (m *Manager) Snapshot(ctx context.Context, runtime adapter.Runtime, target SnapshotTarget) (Record, error) {
	if runtime == nil || !validID(target.WorkspaceID) || !validID(target.ThreadID) {
		return Record{}, ErrInvalid
	}
	snapshot, err := runtime.ReadThread(ctx, adapter.ReadThreadRequest{WorkspaceID: target.WorkspaceID, ThreadID: target.ThreadID, IncludeTurns: true})
	if err != nil {
		return Record{}, err
	}
	payload := snapshotPayload(snapshot, "")
	payload["pendingApprovals"] = m.pendingApprovalPayloads(ctx, target.ThreadID)
	records, err := m.Publish(ctx, adapter.AgentEvent{Type: protocol.EventThreadSnapshot, WorkspaceID: target.WorkspaceID, ThreadID: target.ThreadID, Payload: payload})
	if err != nil || len(records) != 1 {
		if err != nil {
			return Record{}, err
		}
		return Record{}, ErrInvalid
	}
	return records[0], nil
}

// Recover returns retained events or emits an explicit gap followed by a
// current snapshot. The gap and snapshot are themselves durable events.
func (m *Manager) Recover(ctx context.Context, runtime adapter.Runtime, target SnapshotTarget, afterSequence int64, limit int) (ReplayBatch, error) {
	batch, err := m.Replay(ctx, afterSequence, limit)
	if err != nil || !batch.Gap {
		return batch, err
	}
	gapRecords, err := m.Publish(ctx, adapter.AgentEvent{Type: protocol.EventHistoryGap, WorkspaceID: target.WorkspaceID, ThreadID: target.ThreadID, Payload: map[string]any{"afterSequence": afterSequence, "earliestSequence": batch.EarliestSequence}})
	if err != nil {
		return ReplayBatch{}, err
	}
	snapshot, err := m.Snapshot(ctx, runtime, target)
	if err != nil {
		return ReplayBatch{}, err
	}
	batch.Records = append(gapRecords, snapshot)
	batch.LatestSequence = snapshot.Sequence
	batch.HasMore = false
	return batch, nil
}

func (m *Manager) Reconcile(ctx context.Context, runtime adapter.Runtime) (ReconcileReport, error) {
	if runtime == nil {
		return ReconcileReport{}, ErrInvalid
	}
	records, err := m.store.RuntimeThreads(ctx)
	if err != nil {
		return ReconcileReport{}, classifyStore(err)
	}
	var report ReconcileReport
	for _, record := range records {
		if record.State == store.RuntimeThreadIdle {
			continue
		}
		snapshot, readErr := runtime.ReadThread(ctx, adapter.ReadThreadRequest{WorkspaceID: record.WorkspaceID, ThreadID: record.ThreadID, IncludeTurns: true})
		if readErr != nil || snapshot.Thread.Status == "active" || snapshot.Thread.Status == "systemError" {
			report.Deferred++
			continue
		}
		status := terminalStatus(snapshot.Turns, record.ActiveTurnID)
		if status != "" {
			eventType := terminalEvent(status)
			if !m.snapshotHasTerminal(ctx, record.ThreadID, record.ActiveTurnID, string(eventType)) {
				_, err = m.Publish(ctx, adapter.AgentEvent{Type: eventType, WorkspaceID: record.WorkspaceID, ThreadID: record.ThreadID, TurnID: record.ActiveTurnID, Payload: map[string]any{"status": status, "reconciled": true}})
				if err != nil {
					return report, err
				}
			}
			report.Confirmed++
		} else {
			_ = m.store.MarkThreadApprovalsAmbiguous(ctx, record.ThreadID)
			payload := snapshotPayload(snapshot, "ambiguous")
			payload["pendingApprovals"] = []any{}
			payload["reason"] = "runtime_confirmation_lost"
			_, err = m.Publish(ctx, adapter.AgentEvent{Type: protocol.EventThreadSnapshot, WorkspaceID: record.WorkspaceID, ThreadID: record.ThreadID, TurnID: record.ActiveTurnID, Payload: payload, Ambiguous: true})
			if err != nil {
				return report, err
			}
			report.Ambiguous++
		}
		record.State, record.ActiveTurnID = store.RuntimeThreadIdle, ""
		if err := m.store.SaveRuntimeThread(ctx, record); err != nil {
			return report, classifyStore(err)
		}
	}
	return report, nil
}

func (m *Manager) snapshotHasTerminal(ctx context.Context, threadID, turnID, eventType string) bool {
	record, err := m.store.Snapshot(ctx, threadID)
	if err != nil {
		return false
	}
	var payload map[string]any
	if json.Unmarshal(record.Payload, &payload) != nil {
		return false
	}
	recent, _ := payload["recent"].([]any)
	for _, item := range recent {
		entry, _ := item.(map[string]any)
		if firstString(entry, "type", "") == eventType && firstString(entry, "turnId", "") == turnID {
			return true
		}
	}
	return false
}

func (m *Manager) updateApproval(ctx context.Context, record Record, payload map[string]any) error {
	if record.Type != string(protocol.EventApprovalRequested) && record.Type != string(protocol.EventApprovalResolved) {
		return nil
	}
	approvalID, _ := payload["approvalId"].(string)
	if !validID(approvalID) {
		return ErrInvalid
	}
	raw, err := boundedJSON(payload, store.MaxSnapshotBytes)
	if err != nil {
		return err
	}
	if record.Type == string(protocol.EventApprovalRequested) {
		expiresAt, _ := time.Parse(time.RFC3339Nano, firstString(payload, "expiresAt", ""))
		return m.store.SaveApproval(ctx, store.ApprovalRecord{
			ApprovalID: approvalID, WorkspaceID: record.WorkspaceID, ThreadID: record.ThreadID, TurnID: record.TurnID, ItemID: record.ItemID,
			Status: store.ApprovalPending, OperationDigest: firstString(payload, "operationDigest", ""), Payload: raw, ExpiresAt: expiresAt, UpdatedAt: record.CreatedAt,
		})
	}
	existing, err := m.store.Approval(ctx, approvalID)
	if err != nil {
		return err
	}
	existing.Status, existing.Payload, existing.UpdatedAt = store.ApprovalResolved, raw, record.CreatedAt
	return m.store.SaveApproval(ctx, existing)
}

func (m *Manager) pendingApprovalPayloads(ctx context.Context, threadID string) []any {
	records, err := m.store.PendingApprovals(ctx, threadID)
	if err != nil {
		return []any{}
	}
	result := make([]any, 0, len(records))
	for _, record := range records {
		var payload map[string]any
		if json.Unmarshal(record.Payload, &payload) == nil {
			result = append(result, payload)
		}
	}
	return result
}

func (m *Manager) updateSnapshot(ctx context.Context, record Record, payload map[string]any) error {
	if record.ThreadID == "" {
		return nil
	}
	status := eventStatus(record.Type, payload)
	state := map[string]any{"status": status, "latestSequence": record.Sequence, "recent": []any{map[string]any{"type": record.Type, "turnId": record.TurnID, "itemId": record.ItemID, "payload": clonePayload(payload)}}}
	if existing, err := m.store.Snapshot(ctx, record.ThreadID); err == nil {
		var previous map[string]any
		if json.Unmarshal(existing.Payload, &previous) == nil {
			state = previous
			state["status"] = status
			state["latestSequence"] = record.Sequence
			recent, _ := state["recent"].([]any)
			recent = append(recent, map[string]any{"type": record.Type, "turnId": record.TurnID, "itemId": record.ItemID, "payload": clonePayload(payload)})
			if len(recent) > maxSnapshotItems {
				recent = recent[len(recent)-maxSnapshotItems:]
				state["truncated"] = true
			}
			state["recent"] = recent
		}
	}
	raw, err := boundedJSON(state, store.MaxSnapshotBytes)
	if err != nil {
		return err
	}
	return m.store.SaveSnapshot(ctx, store.SnapshotRecord{WorkspaceID: record.WorkspaceID, ThreadID: record.ThreadID, Status: status, LatestSequence: record.Sequence, Payload: raw, UpdatedAt: record.CreatedAt})
}

func (m *Manager) messageID() (string, error) {
	value := make([]byte, 16)
	if _, err := io.ReadFull(m.random, value); err != nil {
		return "", errors.New("node event identifier generation failed")
	}
	return "evt_" + base64.RawURLEncoding.EncodeToString(value), nil
}

func pointer(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

func validID(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= 128 && utf8.ValidString(value)
}

func classifyStore(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, store.ErrInvalid):
		return ErrInvalid
	case errors.Is(err, store.ErrConflict):
		return ErrSequenceExhausted
	default:
		return errors.New("node event log storage failed")
	}
}

func clonePayload(value map[string]any) map[string]any {
	return sanitizeMap(value)
}

func sanitizeMap(value map[string]any) map[string]any {
	redacted := credential.RedactFields(value)
	result := make(map[string]any, len(redacted))
	truncated := false
	for key, item := range redacted {
		result[key], truncated = sanitizeValueFlag(item, truncated)
	}
	if truncated {
		result["truncated"] = true
	}
	return result
}

func sanitizeValue(value any) any {
	result, _ := sanitizeValueFlag(value, false)
	return result
}

func sanitizeValueFlag(value any, truncated bool) (any, bool) {
	switch typed := value.(type) {
	case string:
		if len(typed) > maxTextBytes {
			truncated = true
		}
		return truncateUTF8(typed, maxTextBytes), truncated
	case map[string]any:
		mapped := sanitizeMap(typed)
		if value, _ := mapped["truncated"].(bool); value {
			truncated = true
		}
		return mapped, truncated
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index], truncated = sanitizeValueFlag(item, truncated)
		}
		return result, truncated
	default:
		return value, truncated
	}
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func boundedJSON(value map[string]any, limit int) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalid
	}
	if len(raw) <= limit {
		return raw, nil
	}
	minimal := map[string]any{"status": value["status"], "latestSequence": value["latestSequence"], "truncated": true}
	raw, err = json.Marshal(minimal)
	if err != nil || len(raw) > limit {
		return nil, ErrInvalid
	}
	return raw, nil
}

func snapshotPayload(snapshot adapter.ThreadSnapshot, override string) map[string]any {
	status := snapshot.Thread.Status
	if override != "" {
		status = override
	}
	turns := make([]any, 0, len(snapshot.Turns))
	for _, turn := range snapshot.Turns {
		turns = append(turns, map[string]any{"id": turn.ID, "status": turn.Status})
	}
	return map[string]any{"status": status, "thread": map[string]any{"id": snapshot.Thread.ID, "status": snapshot.Thread.Status}, "turns": turns}
}

func terminalStatus(turns []adapter.Turn, id string) string {
	if id == "" {
		return ""
	}
	for _, turn := range turns {
		if turn.ID == id && (turn.Status == "completed" || turn.Status == "failed" || turn.Status == "interrupted") {
			return turn.Status
		}
	}
	return ""
}

func terminalEvent(status string) protocol.EventType {
	switch status {
	case "failed":
		return protocol.EventTurnFailed
	case "interrupted":
		return protocol.EventTurnInterrupted
	default:
		return protocol.EventTurnCompleted
	}
}

func eventStatus(kind string, payload map[string]any) string {
	if value, ok := payload["status"].(string); ok && value != "" {
		return value
	}
	switch kind {
	case string(protocol.EventTurnCompleted):
		return "completed"
	case string(protocol.EventTurnFailed):
		return "failed"
	case string(protocol.EventTurnInterrupted):
		return "interrupted"
	case string(protocol.EventApprovalRequested):
		return "waiting_approval"
	default:
		return "running"
	}
}
