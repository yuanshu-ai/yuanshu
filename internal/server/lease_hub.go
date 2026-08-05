package server

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
	protocolv11 "github.com/yuanshu-ai/yuanshu/internal/protocol/v11"
	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
	"github.com/yuanshu-ai/yuanshu/internal/transport"
)

const serverControlStreamPrefix = "server-control-v1-"

func leaseRequired(controlType string) bool {
	switch protocolv1.ControlType(controlType) {
	case protocolv1.ControlTurnStart, protocolv1.ControlTurnSteer, protocolv1.ControlTurnInterrupt, protocolv1.ControlApprovalResolve:
		return true
	}
	switch protocolv11.ControlType(controlType) {
	case protocolv11.ControlTaskResume, protocolv11.ControlRunStart, protocolv11.ControlRunSteer, protocolv11.ControlRunInterrupt, protocolv11.ControlInteractionResolve:
		return true
	default:
		return false
	}
}

func serverControl(controlType string) bool {
	switch protocolv1.ControlType(controlType) {
	case protocolv1.ControlLeaseAcquire, protocolv1.ControlLeaseRenew, protocolv1.ControlLeaseRelease, protocolv1.ControlLeaseStatus, protocolv1.ControlNotificationsList, protocolv1.ControlNotificationsRead:
		return true
	default:
		return false
	}
}

func (h *Hub) validateControlFrame(ctx context.Context, source *hubConnection, raw []byte) (routedControl, error) {
	message, err := parseRoutedControl(raw)
	if err != nil || source.role != transport.SessionRoleControl || message.OwnerID != source.ownerID || message.Signer == nil || message.Signer.ClientID != source.subjectID || message.Signer.KeyID != source.keyID || message.Signature == nil || message.ExpiresAt == nil || message.Nonce == nil {
		return routedControl{}, errors.New("control frame authorization failed")
	}
	sentAt, err := time.Parse(time.RFC3339Nano, message.SentAt)
	if err != nil {
		return routedControl{}, errors.New("control frame time is invalid")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, *message.ExpiresAt)
	if err != nil || !expiresAt.After(sentAt) || expiresAt.Sub(sentAt) > protocolv1.DefaultControlMaxTTL || expiresAt.Before(h.clock().UTC().Add(-protocolv1.DefaultControlClockSkew)) {
		return routedControl{}, errors.New("control frame is expired")
	}
	if message.Sequence < 1 || len(source.publicKey) != ed25519.PublicKeySize {
		return routedControl{}, errors.New("control frame sequence is invalid")
	}
	decoded, err := decodeSignature(message.Signature)
	if err != nil {
		return routedControl{}, errors.New("control frame signature is invalid")
	}
	input, err := message.signingInput()
	if err != nil || !ed25519.Verify(source.publicKey, input, decoded) {
		return routedControl{}, errors.New("control frame signature is invalid")
	}
	record := protocolv1.ReplayRecord{
		OwnerID: message.OwnerID, NodeID: message.NodeID, MessageID: message.MessageID,
		ClientID: message.Signer.ClientID, KeyID: message.Signer.KeyID, Nonce: *message.Nonce,
		Sequence: message.Sequence, NonceRetainTo: expiresAt.Add(protocolv1.DefaultControlClockSkew),
	}
	if err := h.replay.CheckAndRecord(ctx, record); err != nil {
		return message, err
	}
	return message, nil
}

func decodeSignature(value *string) ([]byte, error) {
	if value == nil {
		return nil, errors.New("signature is missing")
	}
	decoded, err := base64RawURLDecode(*value)
	if err != nil || len(decoded) != ed25519.SignatureSize {
		return nil, errors.New("signature is invalid")
	}
	return decoded, nil
}

func base64RawURLDecode(value string) ([]byte, error) {
	// Keep the helper local to the relay boundary so the raw signature is never
	// logged or included in an error.
	return base64.RawURLEncoding.DecodeString(value)
}

func (h *Hub) leaseLock(scope serverstore.LeaseScope) *sync.Mutex {
	key := leaseKey(scope)
	h.leaseMu.Lock()
	defer h.leaseMu.Unlock()
	lock := h.leaseLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		h.leaseLocks[key] = lock
	}
	return lock
}

func (h *Hub) withLeaseLock(scope serverstore.LeaseScope, fn func() error) error {
	lock := h.leaseLock(scope)
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

func (h *Hub) handleServerControl(ctx context.Context, source *hubConnection, message routedControl) error {
	if protocolv1.ControlType(message.Type) == protocolv1.ControlNotificationsList || protocolv1.ControlType(message.Type) == protocolv1.ControlNotificationsRead {
		return h.handleNotificationControl(ctx, source, message)
	}
	scope, err := messageLeaseScope(message)
	if err != nil {
		return h.sendServerResult(ctx, source, message, protocolv1.ControlResultRejected, protocolv1.ErrorInvalidMessage, nil)
	}
	return h.withLeaseLock(scope, func() error {
		now := h.clock().UTC()
		var record serverstore.LeaseRecord
		var operationErr error
		switch protocolv1.ControlType(message.Type) {
		case protocolv1.ControlLeaseAcquire:
			force, expected, parseErr := leaseAcquirePayload(message.Payload)
			if parseErr != nil {
				return h.sendServerResult(ctx, source, message, protocolv1.ControlResultRejected, protocolv1.ErrorInvalidMessage, nil)
			}
			leaseID, randomErr := h.randomValue(16)
			if randomErr != nil {
				return randomErr
			}
			record, operationErr = h.leases.AcquireLease(ctx, serverstore.LeaseAcquireRequest{Scope: scope, ClientID: source.subjectID, LeaseID: leaseID, Force: force, ExpectedEpoch: expected, Now: now, TTL: serverstore.DefaultLeaseTTL})
		case protocolv1.ControlLeaseRenew, protocolv1.ControlLeaseRelease:
			leaseID, epoch, parseErr := leaseMutationPayload(message.Payload)
			if parseErr != nil {
				return h.sendServerResult(ctx, source, message, protocolv1.ControlResultRejected, protocolv1.ErrorInvalidMessage, nil)
			}
			request := serverstore.LeaseMutationRequest{Scope: scope, ClientID: source.subjectID, LeaseID: leaseID, Epoch: epoch, Now: now, TTL: serverstore.DefaultLeaseTTL}
			if protocolv1.ControlType(message.Type) == protocolv1.ControlLeaseRenew {
				record, operationErr = h.leases.RenewLease(ctx, request)
			} else {
				record, operationErr = h.leases.ReleaseLease(ctx, request)
			}
		case protocolv1.ControlLeaseStatus:
			record, operationErr = h.leases.Lease(ctx, scope, now)
		}
		if operationErr != nil {
			code := protocolv1.ErrorConflict
			if errors.Is(operationErr, serverstore.ErrExpired) {
				code = protocolv1.ErrorExpired
			}
			return h.sendServerResult(ctx, source, message, protocolv1.ControlResultRejected, code, leasePayload(record))
		}
		if record.State == "held" || record.State == "released" {
			if message.Type != string(protocolv1.ControlLeaseStatus) {
				if err := h.broadcastLeaseChange(ctx, record, message.MessageID); err != nil {
					return err
				}
			}
		}
		return h.sendServerResult(ctx, source, message, protocolv1.ControlResultConfirmed, "", leasePayload(record))
	})
}

func (h *Hub) handleNotificationControl(ctx context.Context, source *hubConnection, message routedControl) error {
	switch protocolv1.ControlType(message.Type) {
	case protocolv1.ControlNotificationsList:
		limit := 50
		if value, ok := message.Payload["limit"].(float64); ok && value >= 1 && value <= 200 && value == math.Trunc(value) {
			limit = int(value)
		}
		items, err := h.notifications.ListNotifications(ctx, source.ownerID, limit)
		if err != nil {
			return h.sendServerResult(ctx, source, message, protocolv1.ControlResultRejected, protocolv1.ErrorRuntimeFailed, nil)
		}
		payload := make([]map[string]any, 0, len(items))
		for _, item := range items {
			entry := map[string]any{"id": item.ID, "nodeId": item.NodeID, "type": item.Type, "summary": item.Summary, "sourceSequence": item.SourceSequence, "createdAt": item.CreatedAt.UTC().Format(time.RFC3339Nano), "read": item.ReadAt != nil}
			if item.WorkspaceID != "" {
				entry["workspaceId"] = item.WorkspaceID
			}
			if item.ThreadID != "" {
				entry["threadId"] = item.ThreadID
			}
			if item.TurnID != "" {
				entry["turnId"] = item.TurnID
			}
			payload = append(payload, entry)
		}
		return h.sendServerResult(ctx, source, message, protocolv1.ControlResultConfirmed, "", map[string]any{
			"notifications": payload,
			"onlineNodeIds": h.ownerOnlineNodeIDs(source.ownerID),
		})
	case protocolv1.ControlNotificationsRead:
		notificationID, ok := message.Payload["notificationId"].(string)
		if !ok || notificationID == "" {
			return h.sendServerResult(ctx, source, message, protocolv1.ControlResultRejected, protocolv1.ErrorInvalidMessage, nil)
		}
		if err := h.notifications.MarkNotificationRead(ctx, source.ownerID, notificationID, h.clock().UTC()); err != nil {
			code := protocolv1.ErrorRuntimeFailed
			if errors.Is(err, serverstore.ErrNotFound) {
				code = protocolv1.ErrorNotFound
			}
			return h.sendServerResult(ctx, source, message, protocolv1.ControlResultRejected, code, nil)
		}
		return h.sendServerResult(ctx, source, message, protocolv1.ControlResultConfirmed, "", nil)
	default:
		return h.sendServerResult(ctx, source, message, protocolv1.ControlResultRejected, protocolv1.ErrorInvalidMessage, nil)
	}
}

func (h *Hub) checkLease(ctx context.Context, source *hubConnection, message routedControl) (serverstore.LeaseRecord, error) {
	scope, err := messageLeaseScope(message)
	if err != nil {
		return serverstore.LeaseRecord{}, err
	}
	leaseID, epoch, err := leaseProof(message.Payload)
	if err != nil {
		return serverstore.LeaseRecord{}, err
	}
	record, err := h.leases.Lease(ctx, scope, h.clock().UTC())
	if err != nil {
		return serverstore.LeaseRecord{}, err
	}
	if record.State != "held" || record.LeaseID != leaseID || record.Epoch != epoch || record.HolderClientID != source.subjectID {
		if record.State == "expired" {
			return record, serverstore.ErrExpired
		}
		return record, serverstore.ErrConflict
	}
	return record, nil
}

func (h *Hub) sendServerResult(ctx context.Context, source *hubConnection, request routedControl, status protocolv1.ControlResultStatus, code protocolv1.ErrorCode, extra map[string]any) error {
	payload := map[string]any{"status": string(status), "source": "server"}
	if code != "" {
		payload["errorCode"] = string(code)
	}
	for key, value := range extra {
		payload[key] = value
	}
	messageID, err := h.randomValue(16)
	if err != nil {
		return err
	}
	if request.ProtocolVersion == protocolv11.CurrentVersion {
		result := protocolv11.YuanshuMessage{ProtocolVersion: protocolv11.The11, MessageID: messageID, Type: protocolv11.ControlResult, SentAt: h.clock().UTC().Format(time.RFC3339Nano), OwnerID: request.OwnerID, NodeID: request.NodeID, StreamID: serverControlStreamPrefix + source.subjectID, Sequence: request.Sequence, CorrelationID: request.MessageID, Payload: payload, AgentInstanceID: nil, WorkspaceID: request.WorkspaceID, TaskID: request.TaskID, RunID: request.RunID, ActivityID: request.ActivityID, InteractionID: request.InteractionID}
		encoded, marshalErr := protocolv11.MarshalEvent(result)
		if marshalErr != nil {
			return marshalErr
		}
		return source.relay.Send(ctx, transport.NewFrame(encoded))
	}
	result := protocolv1.YuanshuMessage{ProtocolVersion: protocolv1.CurrentVersion, MessageID: messageID, Type: string(protocolv1.EventControlResult), SentAt: h.clock().UTC().Format(time.RFC3339Nano), OwnerID: request.OwnerID, NodeID: request.NodeID, StreamID: serverControlStreamPrefix + source.subjectID, Sequence: request.Sequence, CorrelationID: request.MessageID, Payload: payload}
	if request.WorkspaceID != nil {
		value := *request.WorkspaceID
		result.WorkspaceID = &value
	}
	if request.TaskID != nil {
		value := *request.TaskID
		result.ThreadID = &value
	}
	if request.RunID != nil {
		value := *request.RunID
		result.TurnID = &value
	}
	return source.relay.Send(ctx, transport.NewFrame(mustMarshal(result)))
}

func (h *Hub) broadcastLeaseChange(ctx context.Context, record serverstore.LeaseRecord, correlationID string) error {
	messageID, err := h.randomValue(16)
	if err != nil {
		return err
	}
	streamID := "lease." + record.Scope.WorkspaceID + "." + record.Scope.ThreadID
	payload := map[string]any{"state": record.State, "epoch": record.Epoch, "reason": "updated"}
	if record.LeaseID != "" {
		payload["leaseId"] = record.LeaseID
	}
	if record.HolderClientID != "" {
		payload["holderClientId"] = record.HolderClientID
	}
	if !record.ExpiresAt.IsZero() {
		payload["expiresAt"] = record.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	message := protocolv1.YuanshuMessage{ProtocolVersion: protocolv1.CurrentVersion, MessageID: messageID, Type: string(protocolv1.EventLeaseChanged), SentAt: h.clock().UTC().Format(time.RFC3339Nano), OwnerID: record.Scope.OwnerID, NodeID: record.Scope.NodeID, WorkspaceID: stringPtr(record.Scope.WorkspaceID), ThreadID: stringPtr(record.Scope.ThreadID), StreamID: streamID, Sequence: record.Epoch, CorrelationID: correlationID, Payload: payload}
	h.broadcast(record.Scope.OwnerID, transport.NewFrame(mustMarshal(message)))
	v11MessageID, err := h.randomValue(16)
	if err != nil {
		return err
	}
	v11Message := protocolv11.YuanshuMessage{ProtocolVersion: protocolv11.The11, MessageID: v11MessageID, Type: protocolv11.LeaseChanged, SentAt: h.clock().UTC().Format(time.RFC3339Nano), OwnerID: record.Scope.OwnerID, NodeID: record.Scope.NodeID, WorkspaceID: stringPtr(record.Scope.WorkspaceID), TaskID: stringPtr(record.Scope.ThreadID), StreamID: streamID + ".1", Sequence: record.Epoch, CorrelationID: correlationID, Payload: payload}
	encoded, err := protocolv11.MarshalEvent(v11Message)
	if err != nil {
		return err
	}
	h.broadcast(record.Scope.OwnerID, transport.NewFrame(encoded))
	return nil
}

func leasePayload(record serverstore.LeaseRecord) map[string]any {
	payload := map[string]any{"state": record.State, "epoch": record.Epoch}
	if record.LeaseID != "" {
		payload["leaseId"] = record.LeaseID
	}
	if record.HolderClientID != "" {
		payload["holderClientId"] = record.HolderClientID
	}
	if !record.ExpiresAt.IsZero() {
		payload["expiresAt"] = record.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return map[string]any{"lease": payload}
}

func messageLeaseScope(message routedControl) (serverstore.LeaseScope, error) {
	if message.WorkspaceID == nil || message.TaskID == nil || *message.WorkspaceID == "" || *message.TaskID == "" || message.OwnerID == "" || message.NodeID == "" {
		return serverstore.LeaseScope{}, serverstore.ErrInvalid
	}
	return serverstore.LeaseScope{OwnerID: message.OwnerID, NodeID: message.NodeID, WorkspaceID: *message.WorkspaceID, ThreadID: *message.TaskID}, nil
}

func leaseAcquirePayload(payload map[string]interface{}) (bool, *int64, error) {
	force := false
	if value, ok := payload["force"]; ok {
		parsed, ok := value.(bool)
		if !ok {
			return false, nil, serverstore.ErrInvalid
		}
		force = parsed
	}
	if value, ok := payload["expectedEpoch"]; ok {
		epoch, ok := jsonNumber(value)
		if !ok {
			return false, nil, serverstore.ErrInvalid
		}
		return force, &epoch, nil
	}
	return force, nil, nil
}

func leaseMutationPayload(payload map[string]interface{}) (string, int64, error) {
	leaseID, ok := payload["leaseId"].(string)
	if !ok || leaseID == "" {
		return "", 0, serverstore.ErrInvalid
	}
	epoch, ok := jsonNumber(payload["epoch"])
	if !ok || epoch < 1 {
		return "", 0, serverstore.ErrInvalid
	}
	return leaseID, epoch, nil
}

func leaseProof(payload map[string]interface{}) (string, int64, error) {
	value, ok := payload["lease"].(map[string]interface{})
	if !ok {
		return "", 0, serverstore.ErrConflict
	}
	return leaseMutationPayload(value)
}

func jsonNumber(value any) (int64, bool) {
	parsed, ok := value.(float64)
	if !ok || parsed < 0 || parsed > 9007199254740991 || parsed != math.Trunc(parsed) {
		return 0, false
	}
	return int64(parsed), true
}

func mustMarshal(message protocolv1.YuanshuMessage) []byte {
	encoded, err := json.Marshal(message)
	if err != nil {
		panic(fmt.Sprintf("server event marshal failed: %v", err))
	}
	return encoded
}

func stringPtr(value string) *string { return &value }

func (h *Hub) randomNotificationID() string {
	value, err := h.randomValue(16)
	if err != nil {
		return "notification-fallback"
	}
	return value
}

func (h *Hub) saveNotification(ctx context.Context, item serverstore.Notification) error {
	return h.notifications.SaveNotification(ctx, item)
}

func (h *Hub) observeNodeEvent(ctx context.Context, source *hubConnection, frame transport.Frame) {
	h.touchNodeFrame(source)
	event, err := parseRoutedEvent(frame.Bytes())
	if err != nil {
		return
	}
	h.observeNodeHealth(source, event)
	// A current Node projects the same Adapter event into v1.0 and v1.1.
	// Notifications remain based on the frozen v1.0 stream during migration so
	// a single Agent transition cannot create two user-visible notifications.
	if event.ProtocolVersion != protocolv1.CurrentVersion {
		return
	}
	typeName := ""
	summary := ""
	switch protocolv1.EventType(event.Type) {
	case protocolv1.EventTurnCompleted:
		typeName, summary = "task.completed", "任务已完成"
	case protocolv1.EventTurnFailed:
		typeName, summary = "task.failed", "任务执行失败"
	case protocolv1.EventApprovalRequested:
		typeName, summary = "approval.required", "任务等待审批"
	case protocolv1.EventDeviceStatus:
		if status, ok := event.Payload["status"].(string); ok && status == "online" {
			typeName, summary = "node.online", "节点已在线"
		}
	default:
		return
	}
	dedup := event.Type + ":" + event.NodeID + ":" + event.StreamID + ":" + fmt.Sprint(event.Sequence)
	_ = h.saveNotification(ctx, serverstore.Notification{
		ID: h.randomNotificationID(), OwnerID: source.ownerID, NodeID: source.subjectID,
		WorkspaceID: value(event.WorkspaceID), ThreadID: value(event.TaskID), TurnID: value(event.RunID),
		Type: typeName, Summary: summary, SourceSequence: event.Sequence, DedupKey: dedup, CreatedAt: h.clock().UTC(),
	})
}

func (h *Hub) touchNodeFrame(source *hubConnection) {
	key := source.ownerID + "\x00" + source.subjectID
	h.mu.Lock()
	detail := h.nodeDetails[key]
	detail.NodeID, detail.Online, detail.LastFrameAt, detail.RelayStatus = source.subjectID, true, h.clock().UTC(), "online"
	h.nodeDetails[key] = detail
	h.mu.Unlock()
}

func (h *Hub) observeNodeHealth(source *hubConnection, event routedEvent) {
	key := source.ownerID + "\x00" + source.subjectID
	now := h.clock().UTC()
	h.mu.Lock()
	detail := h.nodeDetails[key]
	detail.NodeID, detail.Online, detail.LastEventAt, detail.RelayStatus = source.subjectID, true, now, "online"
	switch event.Type {
	case string(protocolv1.EventRuntimeStatus):
		if state, ok := event.Payload["state"].(string); ok {
			detail.RuntimeStatus = state
		} else if state, ok := event.Payload["status"].(string); ok {
			detail.RuntimeStatus = state
		}
	case string(protocolv1.EventDeviceStatus):
		if workspaces, ok := event.Payload["workspaces"].([]any); ok {
			detail.WorkspaceCount = len(workspaces)
		}
		if recovery, ok := event.Payload["recovery"].(string); ok {
			detail.RecoveryStatus = recovery
		}
	case string(protocolv1.EventHistoryGap):
		detail.RecoveryStatus = "history_gap"
	}
	h.nodeDetails[key] = detail
	h.mu.Unlock()
}

func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}
