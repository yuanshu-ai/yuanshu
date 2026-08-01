// Package protocol defines the temporary, internal-only M0 PoC wire format.
// It is deliberately not a promise of compatibility with the future Yuanshu protocol.
package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	Version         = "m0-poc-1"
	MaxControlBytes = 256 << 10
	MaxEventBytes   = 1 << 20
	WorkspaceID     = "poc-workspace"
)

var (
	ErrInvalidFrame  = errors.New("invalid m0 PoC frame")
	ErrFrameTooLarge = errors.New("m0 PoC frame exceeds size limit")
)

const (
	TaskStart       = "task.start"
	ApprovalResolve = "approval.resolve"
	EventsResume    = "events.resume"

	NodeStatus        = "node.status"
	ThreadStarted     = "thread.started"
	TurnStarted       = "turn.started"
	TurnCompleted     = "turn.completed"
	TurnFailed        = "turn.failed"
	AgentDelta        = "agent.message.delta"
	CommandEvent      = "command.event"
	FileChange        = "file.changed"
	DiffUpdated       = "diff.updated"
	ApprovalRequested = "approval.requested"
	ApprovalResolved  = "approval.resolved"
	Snapshot          = "snapshot"
	HistoryGap        = "history.gap"
	Ambiguous         = "control.ambiguous"
	ErrorEvent        = "error"
)

type Frame struct {
	POCVersion string          `json:"pocVersion"`
	Type       string          `json:"type"`
	RequestID  string          `json:"requestId,omitempty"`
	NodeID     string          `json:"nodeId,omitempty"`
	Sequence   uint64          `json:"sequence,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

func New(kind, requestID, nodeID string, payload any) (Frame, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Frame{}, fmt.Errorf("encode frame payload: %w", err)
	}
	return Frame{POCVersion: Version, Type: kind, RequestID: requestID, NodeID: nodeID, Payload: raw}, nil
}

func Decode(data []byte, limit int) (Frame, error) {
	if len(data) > limit {
		return Frame{}, ErrFrameTooLarge
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var frame Frame
	if err := dec.Decode(&frame); err != nil {
		return Frame{}, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) || frame.POCVersion != Version || frame.Type == "" {
		return Frame{}, ErrInvalidFrame
	}
	return frame, nil
}

func Encode(frame Frame, limit int) ([]byte, error) {
	if frame.POCVersion != Version || frame.Type == "" {
		return nil, ErrInvalidFrame
	}
	b, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	if len(b) > limit {
		return nil, ErrFrameTooLarge
	}
	return b, nil
}

type TaskStartPayload struct {
	WorkspaceID string `json:"workspaceId"`
	Prompt      string `json:"prompt"`
}

type ApprovalResolvePayload struct {
	ApprovalID string `json:"approvalId"`
	Decision   string `json:"decision"`
}

type ResumePayload struct {
	LastSequence uint64 `json:"lastSequence"`
}

func DecodePayload[T any](frame Frame) (T, error) {
	var value T
	dec := json.NewDecoder(bytes.NewReader(frame.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&value); err != nil {
		return value, fmt.Errorf("%w: invalid %s payload", ErrInvalidFrame, frame.Type)
	}
	return value, nil
}
