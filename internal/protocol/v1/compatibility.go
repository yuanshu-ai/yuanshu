package v1

import "strconv"

// MessageKind tells the classifier whether the sender is presenting a control
// or event frame. An unknown wire type cannot safely determine that intent by
// name alone.
type MessageKind string

const (
	MessageKindControl MessageKind = "control"
	MessageKindEvent   MessageKind = "event"
)

type Classification string

const (
	ClassificationKnownControl       Classification = "known_control"
	ClassificationKnownEvent         Classification = "known_event"
	ClassificationUnknownEvent       Classification = "unknown_event"
	ClassificationIncompatible       Classification = "incompatible_version"
	ClassificationUnsupportedControl Classification = "unsupported_control"
)

// Classify applies the v1 compatibility rules without accepting or verifying
// a message. Validation and signature verification remain separate steps.
func Classify(version string, kind MessageKind, messageType string) Classification {
	major, minor, ok := parseVersion(version)
	if !ok || major != 1 {
		return ClassificationIncompatible
	}

	if kind == MessageKindControl {
		if minor != 0 || !IsKnownControl(messageType) {
			return ClassificationUnsupportedControl
		}
		return ClassificationKnownControl
	}
	if kind == MessageKindEvent {
		if IsKnownEvent(messageType) {
			return ClassificationKnownEvent
		}
		return ClassificationUnknownEvent
	}
	return ClassificationIncompatible
}

func IsKnownControl(messageType string) bool {
	for _, known := range KnownControlTypes {
		if string(known) == messageType {
			return true
		}
	}
	return false
}

func IsKnownEvent(messageType string) bool {
	for _, known := range KnownEventTypes {
		if string(known) == messageType {
			return true
		}
	}
	return false
}

func IsTerminalControlResult(status ControlResultStatus) bool {
	return status == ControlResultConfirmed || status == ControlResultRejected || status == ControlResultAmbiguous
}

func FrameWithinLimit(kind MessageKind, size int) bool {
	if size < 0 {
		return false
	}
	switch kind {
	case MessageKindControl:
		return size <= ControlFrameMaxBytes
	case MessageKindEvent:
		return size <= EventFrameMaxBytes
	default:
		return false
	}
}

func parseVersion(version string) (int, int, bool) {
	for i := 1; i < len(version)-1; i++ {
		if version[i] != '.' {
			continue
		}
		major, majorErr := strconv.Atoi(version[:i])
		minor, minorErr := strconv.Atoi(version[i+1:])
		return major, minor, majorErr == nil && minorErr == nil && major >= 0 && minor >= 0
	}
	return 0, 0, false
}
