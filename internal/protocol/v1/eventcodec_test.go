package v1

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEventCodecStrictRoundTrip(t *testing.T) {
	message := YuanshuMessage{
		ProtocolVersion: CurrentVersion, MessageID: "event-test", Type: string(EventRuntimeStatus), SentAt: time.Date(2026, 8, 2, 1, 2, 3, 0, time.UTC).Format(time.RFC3339Nano),
		OwnerID: "owner", NodeID: "node", StreamID: "events", Sequence: 1, CorrelationID: "event-test", Payload: map[string]any{"status": "ready"},
	}
	raw, err := MarshalEvent(message)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseEvent(raw)
	if err != nil || parsed.MessageID != message.MessageID || parsed.Payload["status"] != "ready" {
		t.Fatalf("ParseEvent = %#v, %v", parsed, err)
	}
	raw[0] = 'x'
	again, err := MarshalEvent(message)
	if err != nil || again[0] == 'x' {
		t.Fatal("encoded event bytes were not detached")
	}
}

func TestEventCodecRejectsInvalidFrames(t *testing.T) {
	base := YuanshuMessage{
		ProtocolVersion: CurrentVersion, MessageID: "event-test", Type: string(EventRuntimeStatus), SentAt: time.Now().UTC().Format(time.RFC3339Nano),
		OwnerID: "owner", NodeID: "node", StreamID: "events", Sequence: 1, CorrelationID: "event-test", Payload: map[string]any{"status": "ready"},
	}
	tests := []YuanshuMessage{
		func() YuanshuMessage { value := base; value.Type = "future.event"; return value }(),
		func() YuanshuMessage { value := base; value.Sequence = 0; return value }(),
		func() YuanshuMessage { value := base; value.Payload = map[string]any{}; return value }(),
		func() YuanshuMessage {
			value := base
			value.Payload = map[string]any{"status": strings.Repeat("x", EventFrameMaxBytes+1)}
			return value
		}(),
	}
	for _, test := range tests {
		if _, err := MarshalEvent(test); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("MarshalEvent(%s) error = %v", test.Type, err)
		}
	}
	if _, err := ParseEvent([]byte(`{"protocolVersion":"1.0","protocolVersion":"1.0"}`)); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("duplicate-key ParseEvent error = %v", err)
	}
}
