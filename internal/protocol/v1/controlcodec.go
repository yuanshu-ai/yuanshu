package v1

import (
	"encoding/json"
	"errors"
)

var ErrInvalidControl = errors.New("protocol control is invalid")

// ParseControl validates the syntax, schema and known control type without
// checking trust, freshness or replay state. Callers that accept a control
// must still verify its signature and authorization.
func ParseControl(raw []byte) (YuanshuMessage, error) {
	if len(raw) > ControlFrameMaxBytes {
		return YuanshuMessage{}, ErrInvalidControl
	}
	document, err := decodeStrictJSON(raw)
	if err != nil {
		return YuanshuMessage{}, ErrInvalidControl
	}
	object, ok := document.(map[string]any)
	if !ok {
		return YuanshuMessage{}, ErrInvalidControl
	}
	version, versionOK := object["protocolVersion"].(string)
	kind, kindOK := object["type"].(string)
	if !versionOK || version != CurrentVersion || !kindOK || !IsKnownControl(kind) {
		return YuanshuMessage{}, ErrInvalidControl
	}
	schema, err := protocolSchema()
	if err != nil || schema.Validate(document) != nil {
		return YuanshuMessage{}, ErrInvalidControl
	}
	var message YuanshuMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return YuanshuMessage{}, ErrInvalidControl
	}
	return cloneMessage(message), nil
}
