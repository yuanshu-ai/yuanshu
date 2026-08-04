package probe_test

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yuanshu-ai/yuanshu/internal/adapter/claude/probe"
)

func TestFixtureProducesOnlyBoundedCapabilityEvents(t *testing.T) {
	file, err := os.Open(filepath.Join("testdata", "managed-stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	var events []probe.Event
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), probe.MaxMessageBytes)
	for scanner.Scan() {
		event, err := probe.ParseLine(scanner.Bytes())
		if err != nil {
			t.Fatalf("ParseLine: %v", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("events=%d", len(events))
	}
	if events[0].Kind != probe.KindSystem || events[0].Subtype != "init" || !events[0].HasSession {
		t.Fatalf("init=%#v", events[0])
	}
	if !reflect.DeepEqual(events[0].Capabilities, []string{"interrupt_cancel_queued_v1", "interrupt_receipt_v1"}) {
		t.Fatalf("capabilities=%#v", events[0].Capabilities)
	}
	if !reflect.DeepEqual(events[1].Content, []probe.ContentKind{probe.ContentReasoning, probe.ContentText, probe.ContentToolUse}) {
		t.Fatalf("assistant content=%#v", events[1].Content)
	}
	if !reflect.DeepEqual(events[2].Content, []probe.ContentKind{probe.ContentToolResult}) {
		t.Fatalf("user content=%#v", events[2].Content)
	}
	if !events[3].Terminal || events[3].Failed || events[3].Kind != probe.KindResult {
		t.Fatalf("result=%#v", events[3])
	}
}

func TestParserIsForwardCompatibleAndDoesNotLeakInvalidInput(t *testing.T) {
	event, err := probe.ParseLine([]byte(`{"type":"future_event","subtype":"future_v1","session_id":"native-canary","message":{"content":[{"type":"future_content","text":"secret-canary"}]}}`))
	if err != nil || event.Kind != probe.KindUnknown || !reflect.DeepEqual(event.Content, []probe.ContentKind{probe.ContentUnknown}) {
		t.Fatalf("event=%#v err=%v", event, err)
	}
	if event.HasSession != true {
		t.Fatal("session presence was not retained")
	}

	canary := `C:\Users\canary\secret sk-secret-canary`
	_, err = probe.ParseLine([]byte(`{"type":"system","subtype":"` + canary + `"}`))
	if !errors.Is(err, probe.ErrInvalidMessage) {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "sk-") {
		t.Fatalf("error leaked input: %v", err)
	}
}

func TestParserRejectsOversizedMessages(t *testing.T) {
	_, err := probe.ParseLine(make([]byte, probe.MaxMessageBytes+1))
	if !errors.Is(err, probe.ErrMessageTooLarge) {
		t.Fatalf("error=%v", err)
	}
}
