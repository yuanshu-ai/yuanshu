// Package probe contains the bounded OpenCode SSE evidence parser used by
// PF-087. It is not a production Adapter and is not registered by builtin.
package probe

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const MaxMessageBytes = 1 << 20

var (
	ErrInvalidMessage  = errors.New("opencode event is invalid")
	ErrMessageTooLarge = errors.New("opencode event is too large")
)

type Kind string

const (
	KindSession  Kind = "session"
	KindMessage  Kind = "message"
	KindApproval Kind = "approval"
	KindQuestion Kind = "question"
	KindUnknown  Kind = "unknown"
)

type ContentKind string

const (
	ContentText      ContentKind = "text"
	ContentReasoning ContentKind = "reasoning"
	ContentTool      ContentKind = "tool"
	ContentOther     ContentKind = "other"
)

type Event struct {
	Kind         Kind
	SourceType   string
	HasSession   bool
	Content      ContentKind
	Terminal     bool
	Failed       bool
	RequiresUser bool
}

type envelope struct {
	Payload payload `json:"payload"`
}

type payload struct {
	Type       string     `json:"type"`
	Properties properties `json:"properties"`
}

type properties struct {
	SessionID string `json:"sessionID"`
	Part      part   `json:"part"`
}

type part struct {
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
	var value envelope
	if err := decoder.Decode(&value); err != nil {
		return Event{}, ErrInvalidMessage
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Event{}, ErrInvalidMessage
	}
	if !validToken(value.Payload.Type) {
		return Event{}, ErrInvalidMessage
	}
	event := Event{
		Kind:         classifyKind(value.Payload.Type),
		SourceType:   value.Payload.Type,
		HasSession:   value.Payload.Properties.SessionID != "",
		Content:      classifyContent(value.Payload.Properties.Part.Type),
		Terminal:     value.Payload.Type == "session.idle" || value.Payload.Type == "session.error",
		Failed:       value.Payload.Type == "session.error",
		RequiresUser: value.Payload.Type == "permission.v2.asked" || value.Payload.Type == "question.asked",
	}
	return event, nil
}

func classifyKind(value string) Kind {
	switch {
	case value == "session.created" || value == "session.updated" || value == "session.idle" || value == "session.error":
		return KindSession
	case value == "message.updated" || value == "message.part.updated" || value == "message.part.removed":
		return KindMessage
	case value == "permission.v2.asked" || value == "permission.v2.replied":
		return KindApproval
	case value == "question.asked" || value == "question.replied" || value == "question.rejected":
		return KindQuestion
	default:
		return KindUnknown
	}
}

func classifyContent(value string) ContentKind {
	switch value {
	case "":
		return ""
	case "text":
		return ContentText
	case "reasoning":
		return ContentReasoning
	case "tool":
		return ContentTool
	default:
		return ContentOther
	}
}

func validToken(value string) bool {
	if len(value) < 1 || len(value) > 64 {
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
