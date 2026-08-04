// Package probe contains the bounded Claude Code stream-json evidence parser
// used by PF-087. It is not a production Adapter and is not registered by the
// builtin registry.
package probe

import "errors"

const MaxMessageBytes = 1 << 20

var (
	ErrInvalidMessage  = errors.New("claude stream message is invalid")
	ErrMessageTooLarge = errors.New("claude stream message is too large")
)

type Kind string

const (
	KindSystem          Kind = "system"
	KindAssistant       Kind = "assistant"
	KindUser            Kind = "user"
	KindStreamEvent     Kind = "stream_event"
	KindResult          Kind = "result"
	KindControlRequest  Kind = "control_request"
	KindControlResponse Kind = "control_response"
	KindUnknown         Kind = "unknown"
)

type ContentKind string

const (
	ContentText       ContentKind = "text"
	ContentReasoning  ContentKind = "reasoning"
	ContentToolUse    ContentKind = "tool_use"
	ContentToolResult ContentKind = "tool_result"
	ContentUnknown    ContentKind = "unknown"
)

// Event deliberately excludes session IDs, text, tool input/output, paths,
// credentials, model names, and raw JSON. It is safe for capability fixtures.
type Event struct {
	Kind         Kind
	Subtype      string
	HasSession   bool
	Terminal     bool
	Failed       bool
	Capabilities []string
	Content      []ContentKind
}
