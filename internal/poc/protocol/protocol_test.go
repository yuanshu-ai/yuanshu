package protocol

import (
	"errors"
	"strings"
	"testing"
)

func TestFrameRoundTripAndLimits(t *testing.T) {
	f, err := New(TaskStart, "request-1", "poc-node", TaskStartPayload{WorkspaceID: WorkspaceID, Prompt: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Encode(f, MaxControlBytes)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b, MaxControlBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != TaskStart || got.POCVersion != Version {
		t.Fatalf("unexpected frame: %+v", got)
	}
	if _, err := Decode([]byte(strings.Repeat("x", 10)), 4); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestTaskPayloadRejectsArbitraryCWD(t *testing.T) {
	f, _ := New(TaskStart, "r", "poc-node", map[string]any{"workspaceId": WorkspaceID, "prompt": "safe", "cwd": "C:/forbidden"})
	if _, err := DecodePayload[TaskStartPayload](f); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("cwd was accepted: %v", err)
	}
}
