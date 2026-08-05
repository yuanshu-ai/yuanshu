package eventlog

import (
	"context"
	"crypto/sha256"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	protocol "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
	protocolv11 "github.com/yuanshu-ai/yuanshu/internal/protocol/v11"
)

// BeginControl records an already verified control without persisting its task
// body. The digest binds the complete signed content for idempotent lookup.
func (m *Manager) BeginControl(ctx context.Context, control protocol.ValidatedControl) (store.ControlRecord, error) {
	message := control.Message()
	input, err := protocol.ControlSigningInput(message)
	if err != nil || message.Signature == nil {
		return store.ControlRecord{}, ErrInvalid
	}
	digestInput := append(append([]byte(nil), input...), []byte(*message.Signature)...)
	digest := sha256.Sum256(digestInput)
	record := store.ControlRecord{
		MessageID: message.MessageID, RequestDigest: digest[:], Type: message.Type, State: store.ControlValidated,
		EventTarget: store.EventTarget{WorkspaceID: dereference(message.WorkspaceID), ThreadID: dereference(message.ThreadID), TurnID: dereference(message.TurnID), ItemID: dereference(message.ItemID)},
	}
	created, err := m.store.CreateControl(ctx, record)
	return created, classifyStore(err)
}

func (m *Manager) BeginControlV11(ctx context.Context, control protocolv11.ValidatedControl) (store.ControlRecord, error) {
	message := control.Message()
	input, err := protocolv11.ControlSigningInput(message)
	if err != nil || message.Signature == nil {
		return store.ControlRecord{}, ErrInvalid
	}
	digestInput := append(append([]byte(nil), input...), []byte(*message.Signature)...)
	digest := sha256.Sum256(digestInput)
	record := store.ControlRecord{
		MessageID: message.MessageID, RequestDigest: digest[:], Type: string(message.Type), State: store.ControlValidated,
		EventTarget: store.EventTarget{WorkspaceID: dereference(message.WorkspaceID), ThreadID: dereference(message.TaskID), TurnID: dereference(message.RunID), ItemID: dereference(message.InteractionID)},
	}
	created, err := m.store.CreateControl(ctx, record)
	return created, classifyStore(err)
}

func (m *Manager) MarkDispatching(ctx context.Context, messageID string) (store.ControlRecord, error) {
	record, err := m.store.TransitionControl(ctx, messageID, store.ControlDispatching, "", "", 0)
	return record, classifyStore(err)
}

func (m *Manager) CompleteControl(ctx context.Context, messageID string, status protocol.ControlResultStatus, errorCode protocol.ErrorCode, message string) (Record, error) {
	return m.CompleteControlWithPayload(ctx, messageID, status, errorCode, message, nil)
}

// CompleteControlWithPayload appends a terminal control.result with a small,
// already-redacted payload extension. It is used for structured configuration
// results; task bodies and secrets must never be supplied here.
func (m *Manager) CompleteControlWithPayload(ctx context.Context, messageID string, status protocol.ControlResultStatus, errorCode protocol.ErrorCode, message string, extra map[string]any) (Record, error) {
	if status != protocol.ControlResultConfirmed && status != protocol.ControlResultRejected && status != protocol.ControlResultAmbiguous {
		return Record{}, ErrInvalid
	}
	control, err := m.store.Control(ctx, messageID)
	if err != nil {
		return Record{}, classifyStore(err)
	}
	if control.State == string(status) && control.ResultSequence > 0 {
		records, _, err := m.store.ReplayEvents(ctx, m.binding, control.ResultSequence-1, 1)
		if err == nil && len(records) == 1 && records[0].Sequence == control.ResultSequence {
			return records[0], nil
		}
		if err != nil {
			return Record{}, classifyStore(err)
		}
		return Record{}, ErrHistoryGap
	}
	if control.State == store.ControlConfirmed || control.State == store.ControlRejected || control.State == store.ControlAmbiguous {
		return Record{}, ErrControlFinalized
	}
	payload := map[string]any{"status": string(status)}
	if errorCode != "" {
		payload["errorCode"] = string(errorCode)
	}
	if message != "" {
		payload["message"] = truncateUTF8(message, 4096)
	}
	for key, value := range extra {
		if key != "config" && key != "pending" && key != "applied" && key != "requiresLocalConfirmation" && key != "changeId" && key != "revision" {
			continue
		}
		payload[key] = value
	}
	specs, err := m.normalize(adapterEventForControl(control, messageID, payload))
	if err != nil || len(specs) != 1 {
		return Record{}, ErrInvalid
	}
	m.mu.Lock()
	result, err := m.appendWithControl(ctx, specs[0], &store.ControlEventMutation{MessageID: messageID, State: string(status), ErrorCode: string(errorCode)})
	m.mu.Unlock()
	if err != nil {
		return Record{}, classifyStore(err)
	}
	if m.protocolVersion == protocol.CurrentVersion {
		_ = m.updateSnapshot(ctx, result, specs[0].payload)
	}
	return result, nil
}

func adapterEventForControl(control store.ControlRecord, correlationID string, payload map[string]any) adapter.AgentEvent {
	return adapter.AgentEvent{
		Type: protocol.EventControlResult, CorrelationID: correlationID, WorkspaceID: control.WorkspaceID,
		ThreadID: control.ThreadID, TurnID: control.TurnID, ItemID: control.ItemID, Payload: payload,
	}
}

func dereference(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
