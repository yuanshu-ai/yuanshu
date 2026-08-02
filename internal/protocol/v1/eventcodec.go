package v1

import (
	"encoding/json"
	"errors"
)

var ErrInvalidEvent = errors.New("protocol event is invalid")

// MarshalEvent encodes and validates one Protocol v1 event. The returned
// bytes are detached from the input and safe to persist as an immutable frame.
func MarshalEvent(message YuanshuMessage) ([]byte, error) {
	if message.ProtocolVersion != CurrentVersion || !IsKnownEvent(message.Type) || message.Sequence < 1 || message.Sequence > 9007199254740991 || message.ExpiresAt != nil || message.Nonce != nil || message.Signer != nil || message.Signature != nil {
		return nil, ErrInvalidEvent
	}
	raw, err := json.Marshal(message)
	if err != nil || len(raw) > EventFrameMaxBytes {
		return nil, ErrInvalidEvent
	}
	document, err := decodeStrictJSON(raw)
	if err != nil {
		return nil, ErrInvalidEvent
	}
	schema, err := protocolSchema()
	if err != nil || schema.Validate(document) != nil {
		return nil, ErrInvalidEvent
	}
	return append([]byte(nil), raw...), nil
}

// ParseEvent strictly validates a raw Protocol v1 event and returns a detached
// generated message value.
func ParseEvent(raw []byte) (YuanshuMessage, error) {
	if len(raw) > EventFrameMaxBytes {
		return YuanshuMessage{}, ErrInvalidEvent
	}
	document, err := decodeStrictJSON(raw)
	if err != nil {
		return YuanshuMessage{}, ErrInvalidEvent
	}
	object, ok := document.(map[string]any)
	if !ok {
		return YuanshuMessage{}, ErrInvalidEvent
	}
	version, _ := object["protocolVersion"].(string)
	kind, _ := object["type"].(string)
	if version != CurrentVersion || !IsKnownEvent(kind) {
		return YuanshuMessage{}, ErrInvalidEvent
	}
	schema, err := protocolSchema()
	if err != nil || schema.Validate(document) != nil {
		return YuanshuMessage{}, ErrInvalidEvent
	}
	var message YuanshuMessage
	if json.Unmarshal(raw, &message) != nil {
		return YuanshuMessage{}, ErrInvalidEvent
	}
	return cloneMessage(message), nil
}
