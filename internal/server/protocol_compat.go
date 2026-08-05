package server

import (
	"encoding/json"
	"errors"

	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
	protocolv11 "github.com/yuanshu-ai/yuanshu/internal/protocol/v11"
)

// routedControl is the Server's Agent-neutral view of a signed control frame.
// It deliberately contains only envelope fields needed for routing, replay and
// leases. The original versioned envelope remains authoritative for signing.
type routedControl struct {
	ProtocolVersion string
	MessageID       string
	Type            string
	SentAt          string
	ExpiresAt       *string
	OwnerID         string
	NodeID          string
	WorkspaceID     *string
	TaskID          *string
	RunID           *string
	ActivityID      *string
	InteractionID   *string
	StreamID        string
	Sequence        int64
	CorrelationID   string
	Nonce           *string
	Signer          *protocolv1.Signer
	Signature       *string
	Payload         map[string]any
	v1              *protocolv1.YuanshuMessage
	v11             *protocolv11.YuanshuMessage
}

type routedEvent struct {
	ProtocolVersion string
	MessageID       string
	Type            string
	OwnerID         string
	NodeID          string
	WorkspaceID     *string
	TaskID          *string
	RunID           *string
	StreamID        string
	Sequence        int64
	Payload         map[string]any
}

func parseRoutedControl(raw []byte) (routedControl, error) {
	var header routeHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		return routedControl{}, errors.New("control frame is invalid")
	}
	switch header.ProtocolVersion {
	case protocolv1.CurrentVersion:
		message, err := protocolv1.ParseControl(raw)
		if err != nil {
			return routedControl{}, err
		}
		return routedControl{
			ProtocolVersion: message.ProtocolVersion, MessageID: message.MessageID, Type: message.Type,
			SentAt: message.SentAt, ExpiresAt: message.ExpiresAt, OwnerID: message.OwnerID, NodeID: message.NodeID,
			WorkspaceID: message.WorkspaceID, TaskID: message.ThreadID, RunID: message.TurnID, ActivityID: message.ItemID,
			InteractionID: message.ItemID, StreamID: message.StreamID, Sequence: message.Sequence,
			CorrelationID: message.CorrelationID, Nonce: message.Nonce, Signer: message.Signer,
			Signature: message.Signature, Payload: message.Payload, v1: &message,
		}, nil
	case protocolv11.CurrentVersion:
		message, err := protocolv11.ParseControl(raw)
		if err != nil {
			return routedControl{}, err
		}
		var signer *protocolv1.Signer
		if message.Signer != nil {
			signer = &protocolv1.Signer{ClientID: message.Signer.ClientID, KeyID: message.Signer.KeyID}
		}
		return routedControl{
			ProtocolVersion: string(message.ProtocolVersion), MessageID: message.MessageID, Type: string(message.Type),
			SentAt: message.SentAt, ExpiresAt: message.ExpiresAt, OwnerID: message.OwnerID, NodeID: message.NodeID,
			WorkspaceID: message.WorkspaceID, TaskID: message.TaskID, RunID: message.RunID, ActivityID: message.ActivityID,
			InteractionID: message.InteractionID, StreamID: message.StreamID, Sequence: message.Sequence,
			CorrelationID: message.CorrelationID, Nonce: message.Nonce, Signer: signer,
			Signature: message.Signature, Payload: message.Payload, v11: &message,
		}, nil
	default:
		return routedControl{}, errors.New("control protocol is unsupported")
	}
}

func (m routedControl) signingInput() ([]byte, error) {
	if m.v11 != nil {
		return protocolv11.ControlSigningInput(*m.v11)
	}
	if m.v1 != nil {
		return protocolv1.ControlSigningInput(*m.v1)
	}
	return nil, errors.New("control frame is invalid")
}

func classifyControl(version, controlType string) bool {
	switch version {
	case protocolv1.CurrentVersion:
		return protocolv1.Classify(version, protocolv1.MessageKindControl, controlType) == protocolv1.ClassificationKnownControl
	case protocolv11.CurrentVersion:
		return protocolv11.IsKnownControl(controlType)
	default:
		return false
	}
}

func classifyEvent(version, eventType string) bool {
	classification := protocolv1.Classify(version, protocolv1.MessageKindEvent, eventType)
	return classification == protocolv1.ClassificationKnownEvent || classification == protocolv1.ClassificationUnknownEvent
}

func parseRoutedEvent(raw []byte) (routedEvent, error) {
	var header routeHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		return routedEvent{}, err
	}
	switch header.ProtocolVersion {
	case protocolv1.CurrentVersion:
		message, err := protocolv1.ParseEvent(raw)
		if err != nil {
			return routedEvent{}, err
		}
		return routedEvent{ProtocolVersion: message.ProtocolVersion, MessageID: message.MessageID, Type: message.Type, OwnerID: message.OwnerID, NodeID: message.NodeID, WorkspaceID: message.WorkspaceID, TaskID: message.ThreadID, RunID: message.TurnID, StreamID: message.StreamID, Sequence: message.Sequence, Payload: message.Payload}, nil
	case protocolv11.CurrentVersion:
		message, err := protocolv11.ParseEvent(raw)
		if err != nil {
			return routedEvent{}, err
		}
		return routedEvent{ProtocolVersion: string(message.ProtocolVersion), MessageID: message.MessageID, Type: string(message.Type), OwnerID: message.OwnerID, NodeID: message.NodeID, WorkspaceID: message.WorkspaceID, TaskID: message.TaskID, RunID: message.RunID, StreamID: message.StreamID, Sequence: message.Sequence, Payload: message.Payload}, nil
	default:
		return routedEvent{}, errors.New("event protocol is unsupported")
	}
}
