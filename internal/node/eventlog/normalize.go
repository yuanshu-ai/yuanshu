package eventlog

import (
	"errors"
	"fmt"
	"strings"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	protocol "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

func normalizeEvent(event adapter.AgentEvent) ([]eventSpec, error) {
	if !protocol.IsKnownEvent(string(event.Type)) {
		return nil, ErrInvalid
	}
	target := store.EventTarget{WorkspaceID: event.WorkspaceID, ThreadID: event.ThreadID, TurnID: event.TurnID, ItemID: event.ItemID}
	payload, _ := event.Payload.(map[string]any)
	payload = clonePayload(payload)
	spec := eventSpec{kind: event.Type, target: target, correlationID: event.CorrelationID, payload: payload}
	switch event.Type {
	case protocol.EventDeviceStatus, protocol.EventRuntimeStatus:
		if _, ok := payload["status"]; !ok {
			payload["status"] = firstString(payload, "state", "unknown")
		}
	case protocol.EventThreadStarted, protocol.EventTurnStarted, protocol.EventTurnCompleted, protocol.EventTurnFailed, protocol.EventTurnInterrupted:
		// Lifecycle payloads are intentionally open and may be empty.
	case protocol.EventAgentMessageDelta:
		payload["text"] = firstString(payload, "text", firstString(payload, "delta", ""))
		delete(payload, "delta")
	case protocol.EventAgentMessageCompleted:
		payload["text"] = firstString(payload, "text", "")
	case protocol.EventCommandStarted, protocol.EventCommandCompleted:
		if event.ItemID == "" {
			return nil, ErrInvalid
		}
		payload["commandId"] = event.ItemID
		if command := firstString(payload, "displayText", firstString(payload, "command", "")); command != "" {
			if len(command) > 4096 {
				payload["truncated"] = true
			}
			payload["displayText"] = truncateUTF8(command, 4096)
		}
		delete(payload, "command")
	case protocol.EventCommandOutputDelta:
		if event.ItemID == "" {
			return nil, ErrInvalid
		}
		payload = map[string]any{"commandId": event.ItemID, "stream": "stdout", "sourceStream": "combined", "text": firstString(payload, "text", firstString(payload, "delta", ""))}
		spec.payload = payload
	case protocol.EventToolStarted, protocol.EventToolCompleted:
		payload["toolName"] = firstString(payload, "toolName", firstString(payload, "kind", "codex-tool"))
	case protocol.EventFileChanged:
		if changes := changeMaps(payload["changes"]); len(changes) > 0 {
			result := make([]eventSpec, 0, len(changes))
			for _, item := range changes {
				path := firstString(item, "path", "")
				if path == "" {
					continue
				}
				result = append(result, eventSpec{kind: event.Type, target: target, correlationID: event.CorrelationID, payload: map[string]any{"path": path, "changeType": mapChangeKind(firstString(item, "kind", "modified"))}})
			}
			if len(result) == 0 {
				return nil, ErrInvalid
			}
			return result, nil
		}
		payload["path"] = firstString(payload, "path", "")
		payload["changeType"] = mapChangeKind(firstString(payload, "changeType", firstString(payload, "kind", "modified")))
	case protocol.EventDiffUpdated:
		payload["path"] = firstString(payload, "path", ".")
		payload["diff"] = firstString(payload, "diff", "")
	case protocol.EventApprovalRequested:
		if event.Approval == nil || !validID(event.Approval.ID) {
			return nil, ErrInvalid
		}
		payload = map[string]any{"approvalId": event.Approval.ID, "kind": event.Approval.Kind, "summary": event.Approval.Summary, "operation": sanitizeValue(event.Approval.Operation), "expiresAt": event.Approval.ExpiresAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")}
		spec.payload = payload
	case protocol.EventApprovalResolved:
		if firstString(payload, "approvalId", "") == "" {
			return nil, ErrInvalid
		}
		if decision := firstString(payload, "decision", ""); decision != "accept" && decision != "decline" {
			payload["decision"] = "decline"
			payload["resolution"] = "runtime_cleared"
		}
	case protocol.EventControlResult:
		status := firstString(payload, "status", "")
		if status == "" {
			return nil, ErrInvalid
		}
	case protocol.EventHistoryGap:
		if _, ok := payload["afterSequence"]; !ok {
			return nil, ErrInvalid
		}
	case protocol.EventError:
		if event.Ambiguous {
			payload["code"] = "ambiguous"
		}
		if firstString(payload, "code", "") == "" {
			payload["code"] = "runtime_failed"
		}
		if firstString(payload, "message", "") == "" {
			payload["message"] = "Agent runtime reported a sanitized failure."
		}
	case protocol.EventThreadSnapshot:
		if firstString(payload, "status", "") == "" {
			payload["status"] = "unknown"
		}
	default:
		return nil, errors.New("node event mapping is unsupported")
	}
	if err := ensureRequiredPayload(event.Type, spec.payload); err != nil {
		return nil, err
	}
	return []eventSpec{spec}, nil
}

func changeMaps(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapped, ok := item.(map[string]any); ok {
				result = append(result, mapped)
			}
		}
		return result
	default:
		return nil
	}
}

func ensureRequiredPayload(kind protocol.EventType, payload map[string]any) error {
	for _, key := range requiredPayloadKeys(kind) {
		value, exists := payload[key]
		if !exists || value == nil || fmt.Sprint(value) == "" {
			return ErrInvalid
		}
	}
	return nil
}

func requiredPayloadKeys(kind protocol.EventType) []string {
	switch kind {
	case protocol.EventDeviceStatus, protocol.EventRuntimeStatus, protocol.EventThreadSnapshot, protocol.EventControlResult:
		return []string{"status"}
	case protocol.EventAgentMessageDelta, protocol.EventAgentMessageCompleted:
		return []string{"text"}
	case protocol.EventCommandStarted, protocol.EventCommandCompleted:
		return []string{"commandId"}
	case protocol.EventCommandOutputDelta:
		return []string{"commandId", "stream", "text"}
	case protocol.EventToolStarted, protocol.EventToolCompleted:
		return []string{"toolName"}
	case protocol.EventFileChanged:
		return []string{"path", "changeType"}
	case protocol.EventDiffUpdated:
		return []string{"path", "diff"}
	case protocol.EventApprovalRequested:
		return []string{"approvalId", "kind"}
	case protocol.EventApprovalResolved:
		return []string{"approvalId", "decision"}
	case protocol.EventHistoryGap:
		return []string{"afterSequence", "earliestSequence"}
	case protocol.EventError:
		return []string{"code", "message"}
	default:
		return nil
	}
}

func firstString(values map[string]any, key, fallback string) string {
	if value, ok := values[key].(string); ok {
		return value
	}
	return fallback
}

func mapChangeKind(value string) string {
	switch strings.ToLower(value) {
	case "add", "added", "create", "created":
		return "created"
	case "delete", "deleted", "remove", "removed":
		return "deleted"
	case "rename", "renamed", "move", "moved":
		return "renamed"
	default:
		return "modified"
	}
}
