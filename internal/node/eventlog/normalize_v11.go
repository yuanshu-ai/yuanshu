package eventlog

import (
	"errors"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
	protocolv11 "github.com/yuanshu-ai/yuanshu/internal/protocol/v11"
)

func (m *Manager) normalize(event adapter.AgentEvent) ([]eventSpec, error) {
	if m.protocolVersion == protocolv1.CurrentVersion {
		return normalizeEvent(event)
	}
	return normalizeEventV11(event)
}

func normalizeEventV11(event adapter.AgentEvent) ([]eventSpec, error) {
	if protocolv11.IsKnownEvent(string(event.Type)) {
		payload, _ := event.Payload.(map[string]any)
		interactionID := ""
		if string(event.Type) == string(protocolv11.EventInteractionRequested) || string(event.Type) == string(protocolv11.EventInteractionResolved) {
			interactionID = firstString(payload, "id", "")
		}
		return []eventSpec{{
			kind: string(event.Type), agentInstanceID: event.AgentInstanceID,
			interactionID: interactionID, target: eventTarget(event), correlationID: event.CorrelationID, payload: clonePayload(payload),
		}}, nil
	}
	legacy, err := normalizeEvent(event)
	if err != nil {
		return nil, err
	}
	result := make([]eventSpec, 0, len(legacy))
	for _, spec := range legacy {
		spec.kind, spec.payload, err = projectLegacyEventV11(spec.kind, spec.payload, event)
		if err != nil {
			return nil, err
		}
		spec.agentInstanceID = event.AgentInstanceID
		if spec.kind == string(protocolv11.EventInteractionRequested) || spec.kind == string(protocolv11.EventInteractionResolved) {
			spec.interactionID = firstString(spec.payload, "id", "")
		}
		result = append(result, spec)
	}
	return result, nil
}

func eventTarget(event adapter.AgentEvent) store.EventTarget {
	return store.EventTarget{WorkspaceID: event.WorkspaceID, ThreadID: event.ThreadID, TurnID: event.TurnID, ItemID: event.ItemID}
}

func projectLegacyEventV11(kind string, payload map[string]any, event adapter.AgentEvent) (string, map[string]any, error) {
	switch protocolv1.EventType(kind) {
	case protocolv1.EventDeviceStatus:
		return string(protocolv11.EventDeviceStatus), payload, nil
	case protocolv1.EventRuntimeStatus:
		return string(protocolv11.EventRuntimeStatus), payload, nil
	case protocolv1.EventThreadSnapshot:
		return string(protocolv11.EventTaskSnapshot), taskSnapshotPayloadV11(payload, event), nil
	case protocolv1.EventThreadStarted:
		return string(protocolv11.EventTaskStarted), payload, nil
	case protocolv1.EventTurnStarted:
		return string(protocolv11.EventRunStarted), payload, nil
	case protocolv1.EventTurnCompleted:
		return string(protocolv11.EventRunCompleted), payload, nil
	case protocolv1.EventTurnFailed:
		return string(protocolv11.EventRunFailed), payload, nil
	case protocolv1.EventTurnInterrupted:
		return string(protocolv11.EventRunInterrupted), payload, nil
	case protocolv1.EventAgentMessageDelta:
		return string(protocolv11.EventMessageDelta), payload, nil
	case protocolv1.EventAgentMessageCompleted:
		return string(protocolv11.EventMessageCompleted), payload, nil
	case protocolv1.EventCommandStarted:
		return string(protocolv11.EventActivityStarted), activityPayloadV11("command", "running", payload, event.ItemID), nil
	case protocolv1.EventCommandOutputDelta:
		return string(protocolv11.EventActivityUpdated), activityPayloadV11("command", "running", payload, event.ItemID), nil
	case protocolv1.EventCommandCompleted:
		return string(protocolv11.EventActivityCompleted), activityPayloadV11("command", "completed", payload, event.ItemID), nil
	case protocolv1.EventToolStarted:
		return string(protocolv11.EventActivityStarted), activityPayloadV11(firstString(payload, "activityKind", "tool"), "running", payload, event.ItemID), nil
	case protocolv1.EventToolCompleted:
		return string(protocolv11.EventActivityCompleted), activityPayloadV11(firstString(payload, "activityKind", "tool"), "completed", payload, event.ItemID), nil
	case protocolv1.EventFileChanged:
		return string(protocolv11.EventFileChanged), payload, nil
	case protocolv1.EventDiffUpdated:
		return string(protocolv11.EventDiffUpdated), payload, nil
	case protocolv1.EventApprovalRequested:
		return string(protocolv11.EventInteractionRequested), interactionPayloadV11(payload, event), nil
	case protocolv1.EventApprovalResolved:
		return string(protocolv11.EventInteractionResolved), interactionPayloadV11(payload, event), nil
	case protocolv1.EventControlResult:
		return string(protocolv11.EventControlResult), payload, nil
	case protocolv1.EventLeaseChanged:
		return string(protocolv11.EventLeaseChanged), payload, nil
	case protocolv1.EventHistoryGap:
		return string(protocolv11.EventHistoryGap), payload, nil
	case protocolv1.EventError:
		return string(protocolv11.EventError), payload, nil
	default:
		return "", nil, errors.New("Protocol 1.1 event projection is unsupported")
	}
}

func taskSnapshotPayloadV11(payload map[string]any, event adapter.AgentEvent) map[string]any {
	result := clonePayload(payload)
	if threads, ok := result["threads"].([]any); ok {
		tasks := make([]any, 0, len(threads))
		for _, value := range threads {
			if task, ok := value.(map[string]any); ok {
				task = clonePayload(task)
				task["agentInstanceId"], task["workspaceId"] = event.AgentInstanceID, event.WorkspaceID
				tasks = append(tasks, task)
			}
		}
		delete(result, "threads")
		result["tasks"] = tasks
		return result
	}
	var task map[string]any
	if value, ok := result["thread"].(map[string]any); ok {
		task = clonePayload(value)
		delete(result, "thread")
	} else {
		task = map[string]any{"id": firstString(result, "id", event.ThreadID), "status": firstString(result, "status", "unknown")}
		for _, key := range []string{"title", "preview", "createdAt", "updatedAt", "historyState"} {
			if value, exists := result[key]; exists {
				task[key] = value
			}
		}
	}
	task["agentInstanceId"], task["workspaceId"] = event.AgentInstanceID, event.WorkspaceID
	result["task"] = task
	if turns, ok := result["turns"]; ok {
		result["runs"] = turns
		delete(result, "turns")
	}
	if pending, ok := result["pendingApprovals"].([]any); ok {
		interactions := make([]any, 0, len(pending))
		for _, value := range pending {
			entry, ok := value.(map[string]any)
			if !ok {
				continue
			}
			entry = clonePayload(entry)
			runID, activityID := firstString(entry, "turnId", ""), firstString(entry, "itemId", "")
			delete(entry, "turnId")
			delete(entry, "itemId")
			if _, modern := entry["id"]; !modern {
				entry = interactionPayloadV11(entry, adapter.AgentEvent{ItemID: activityID})
			}
			if runID != "" {
				entry["runId"] = runID
			}
			if activityID != "" {
				entry["activityId"] = activityID
			}
			interactions = append(interactions, entry)
		}
		result["interactions"] = interactions
		delete(result, "pendingApprovals")
	}
	return result
}

func activityPayloadV11(kind, status string, payload map[string]any, itemID string) map[string]any {
	result := clonePayload(payload)
	delete(result, "activityKind")
	result["id"], result["kind"], result["status"] = itemID, kind, status
	if title := firstString(result, "displayText", firstString(result, "toolName", "")); title != "" {
		result["title"] = title
	}
	if text := firstString(result, "text", ""); text != "" {
		result["text"] = text
	}
	for _, key := range []string{"commandId", "displayText", "toolName", "stream", "sourceStream"} {
		delete(result, key)
	}
	return result
}

func interactionPayloadV11(payload map[string]any, event adapter.AgentEvent) map[string]any {
	result := clonePayload(payload)
	result["id"] = firstString(result, "approvalId", event.ItemID)
	delete(result, "approvalId")
	kind := firstString(result, "kind", "permission")
	switch kind {
	case "command", "command_execution":
		kind = "command_approval"
	case "file", "file_change", "file-change":
		kind = "file_approval"
	}
	result["kind"] = kind
	if operation, exists := result["operation"]; exists {
		result["details"] = operation
		delete(result, "operation")
	}
	if _, exists := result["status"]; !exists {
		if _, resolved := result["decision"]; resolved {
			result["status"] = map[string]string{"accept": "accepted", "decline": "declined"}[firstString(result, "decision", "decline")]
		} else {
			result["status"] = "pending"
		}
	}
	return result
}
