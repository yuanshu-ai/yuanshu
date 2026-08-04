package probe

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
)

type streamEnvelope struct {
	Type         string            `json:"type"`
	Subtype      string            `json:"subtype"`
	SessionID    string            `json:"session_id"`
	IsError      bool              `json:"is_error"`
	Capabilities []string          `json:"capabilities"`
	Message      streamMessageBody `json:"message"`
	Event        streamEventBody   `json:"event"`
}

type streamMessageBody struct {
	Content []streamContent `json:"content"`
}

type streamEventBody struct {
	Type string `json:"type"`
}

type streamContent struct {
	Type string `json:"type"`
}

func ParseLine(line []byte) (Event, error) {
	if len(line) == 0 {
		return Event{}, ErrInvalidMessage
	}
	if len(line) > MaxMessageBytes {
		return Event{}, ErrMessageTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	var envelope streamEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return Event{}, ErrInvalidMessage
	}
	if err := requireEOF(decoder); err != nil {
		return Event{}, err
	}
	if !validToken(envelope.Subtype, true) {
		return Event{}, ErrInvalidMessage
	}

	event := Event{
		Kind:       classifyKind(envelope.Type),
		Subtype:    envelope.Subtype,
		HasSession: envelope.SessionID != "",
		Terminal:   envelope.Type == string(KindResult),
		Failed:     envelope.Type == string(KindResult) && envelope.IsError,
	}
	for _, capability := range envelope.Capabilities {
		if !validToken(capability, false) {
			return Event{}, ErrInvalidMessage
		}
		event.Capabilities = append(event.Capabilities, capability)
	}
	event.Capabilities = stableUniqueStrings(event.Capabilities)
	for _, content := range envelope.Message.Content {
		event.Content = append(event.Content, classifyContent(content.Type))
	}
	if envelope.Type == string(KindStreamEvent) && envelope.Event.Type != "" {
		event.Content = append(event.Content, classifyContent(envelope.Event.Type))
	}
	event.Content = stableUniqueContent(event.Content)
	return event, nil
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrInvalidMessage
	}
	return nil
}

func classifyKind(value string) Kind {
	switch Kind(value) {
	case KindSystem, KindAssistant, KindUser, KindStreamEvent, KindResult, KindControlRequest, KindControlResponse:
		return Kind(value)
	default:
		return KindUnknown
	}
}

func classifyContent(value string) ContentKind {
	switch value {
	case "text", "text_delta":
		return ContentText
	case "thinking", "thinking_delta", "signature_delta":
		return ContentReasoning
	case "tool_use", "input_json_delta":
		return ContentToolUse
	case "tool_result":
		return ContentToolResult
	default:
		return ContentUnknown
	}
}

func validToken(value string, optional bool) bool {
	if value == "" {
		return optional
	}
	if len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func stableUniqueStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return compactStrings(result)
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	write := 1
	for read := 1; read < len(values); read++ {
		if values[read] != values[write-1] {
			values[write] = values[read]
			write++
		}
	}
	return values[:write]
}

func stableUniqueContent(values []ContentKind) []ContentKind {
	result := append([]ContentKind(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) == 0 {
		return nil
	}
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] != result[write-1] {
			result[write] = result[read]
			write++
		}
	}
	return result[:write]
}
