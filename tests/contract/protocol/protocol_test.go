package protocol_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	v1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

type compatibilityFixture struct {
	Cases []struct {
		Name     string            `json:"name"`
		Version  string            `json:"version"`
		Kind     v1.MessageKind    `json:"kind"`
		Type     string            `json:"type"`
		Expected v1.Classification `json:"expected"`
	} `json:"cases"`
	ControlResultStates []struct {
		State    v1.ControlResultStatus `json:"state"`
		Terminal bool                   `json:"terminal"`
	} `json:"controlResultStates"`
}

type messageFixture struct {
	ControlBase             map[string]any   `json:"controlBase"`
	Controls                []map[string]any `json:"controls"`
	EventBase               map[string]any   `json:"eventBase"`
	Events                  []map[string]any `json:"events"`
	ForwardCompatibleEvents []map[string]any `json:"forwardCompatibleEvents"`
}

func TestCompatibilityFixture(t *testing.T) {
	var fixture compatibilityFixture
	readFixture(t, "compatibility.json", &fixture)
	for _, testCase := range fixture.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			if got := v1.Classify(testCase.Version, testCase.Kind, testCase.Type); got != testCase.Expected {
				t.Fatalf("Classify() = %q, want %q", got, testCase.Expected)
			}
		})
	}
	for _, testCase := range fixture.ControlResultStates {
		if got := v1.IsTerminalControlResult(testCase.State); got != testCase.Terminal {
			t.Errorf("IsTerminalControlResult(%q) = %v, want %v", testCase.State, got, testCase.Terminal)
		}
	}
}

func TestGeneratedTypesRoundTripSharedMessages(t *testing.T) {
	var fixture messageFixture
	readFixture(t, filepath.Join("fixtures", "valid-messages.json"), &fixture)
	messages := append(instantiate(fixture.ControlBase, fixture.Controls), instantiate(fixture.EventBase, fixture.Events)...)
	messages = append(messages, instantiate(fixture.EventBase, fixture.ForwardCompatibleEvents)...)

	for _, original := range messages {
		messageType := original["type"].(string)
		t.Run(messageType, func(t *testing.T) {
			encoded, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			var generated v1.YuanshuMessage
			if err := json.Unmarshal(encoded, &generated); err != nil {
				t.Fatalf("generated type decode: %v", err)
			}
			roundTrip, err := json.Marshal(generated)
			if err != nil {
				t.Fatalf("generated type encode: %v", err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(roundTrip, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(original, decoded) {
				t.Fatalf("round trip changed message\noriginal: %#v\ndecoded: %#v", original, decoded)
			}
		})
	}
}

func TestGeneratedCatalogCoversSharedFixture(t *testing.T) {
	var fixture messageFixture
	readFixture(t, filepath.Join("fixtures", "valid-messages.json"), &fixture)
	if len(fixture.Controls) != len(v1.KnownControlTypes) {
		t.Fatalf("control fixture count = %d, catalog count = %d", len(fixture.Controls), len(v1.KnownControlTypes))
	}
	if len(fixture.Events) != len(v1.KnownEventTypes) {
		t.Fatalf("event fixture count = %d, catalog count = %d", len(fixture.Events), len(v1.KnownEventTypes))
	}
	for _, item := range fixture.Controls {
		if messageType, _ := item["type"].(string); !v1.IsKnownControl(messageType) {
			t.Errorf("control fixture %q is missing from generated catalog", messageType)
		}
	}
	for _, item := range fixture.Events {
		if messageType, _ := item["type"].(string); !v1.IsKnownEvent(messageType) {
			t.Errorf("event fixture %q is missing from generated catalog", messageType)
		}
	}
}

func TestFrameLimits(t *testing.T) {
	tests := []struct {
		kind v1.MessageKind
		size int
		want bool
	}{
		{v1.MessageKindControl, v1.ControlFrameMaxBytes, true},
		{v1.MessageKindControl, v1.ControlFrameMaxBytes + 1, false},
		{v1.MessageKindEvent, v1.EventFrameMaxBytes, true},
		{v1.MessageKindEvent, v1.EventFrameMaxBytes + 1, false},
		{v1.MessageKindEvent, -1, false},
		{"unknown", 1, false},
	}
	for _, testCase := range tests {
		if got := v1.FrameWithinLimit(testCase.kind, testCase.size); got != testCase.want {
			t.Errorf("FrameWithinLimit(%q, %d) = %v, want %v", testCase.kind, testCase.size, got, testCase.want)
		}
	}
}

func instantiate(base map[string]any, items []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for index, item := range items {
		message := clone(base)
		for key, value := range item {
			message[key] = value
		}
		message["messageId"] = message["messageId"].(string) + "_" + string(rune('a'+index))
		message["sequence"] = message["sequence"].(float64) + float64(index)
		result = append(result, message)
	}
	return result
}

func clone(value map[string]any) map[string]any {
	encoded, _ := json.Marshal(value)
	var cloned map[string]any
	_ = json.Unmarshal(encoded, &cloned)
	return cloned
}

func readFixture(t *testing.T, name string, target any) {
	t.Helper()
	path := filepath.Join("..", "..", "..", "schemas", "yuanshu", "v1", name)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, target); err != nil {
		t.Fatal(err)
	}
}
