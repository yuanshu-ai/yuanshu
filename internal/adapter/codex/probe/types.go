// Package probe provides a minimal Codex app-server stdio protocol probe.
// It is intentionally separate from the production adapter planned for later tasks.
package probe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	DefaultMaxMessageBytes = 16 << 20
	DefaultQueueSize       = 256
)

var (
	ErrClosed          = errors.New("codex probe client is closed")
	ErrMessageTooLarge = errors.New("codex app-server message exceeds the configured limit")
	ErrQueueFull       = errors.New("codex probe message queue is full")
	ErrInvalidMessage  = errors.New("invalid codex app-server message")
)

// RequestID preserves the JSON representation of a server-initiated request ID.
// Codex may use either an integer or a string and clients must echo it unchanged.
type RequestID struct {
	raw json.RawMessage
}

func parseRequestID(raw json.RawMessage) (RequestID, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return RequestID{}, fmt.Errorf("%w: missing request id", ErrInvalidMessage)
	}

	var integer int64
	if err := json.Unmarshal(raw, &integer); err == nil {
		return RequestID{raw: append(json.RawMessage(nil), raw...)}, nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return RequestID{raw: append(json.RawMessage(nil), raw...)}, nil
	}

	return RequestID{}, fmt.Errorf("%w: request id must be an integer or string", ErrInvalidMessage)
}

func (id RequestID) MarshalJSON() ([]byte, error) {
	if len(id.raw) == 0 {
		return nil, fmt.Errorf("%w: empty request id", ErrInvalidMessage)
	}
	return append([]byte(nil), id.raw...), nil
}

// Message is a server notification or server-initiated request.
type Message struct {
	ID     *RequestID
	Method string
	Params json.RawMessage
}

// IsRequest reports whether the message requires a response.
func (m Message) IsRequest() bool {
	return m.ID != nil
}

// RPCError is the error object returned by JSON-RPC responses.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("codex app-server RPC error %d: %s", e.Code, e.Message)
}

// ClientInfo identifies this integration during the app-server handshake.
type ClientInfo struct {
	Name    string  `json:"name"`
	Title   *string `json:"title,omitempty"`
	Version string  `json:"version"`
}

// InitializeResult contains only the non-sensitive fields needed by the probe.
type InitializeResult struct {
	UserAgent      string `json:"userAgent"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
}

// AuthMode is the deliberately coarse authentication state recorded by Yuanshu.
type AuthMode string

const (
	AuthNone           AuthMode = "none"
	AuthAPIKey         AuthMode = "api_key"
	AuthChatGPT        AuthMode = "chatgpt"
	AuthCustomProvider AuthMode = "custom_provider"
	AuthOther          AuthMode = "other"
)

// ClassifyAuth maps account/read output without retaining identity or credential fields.
func ClassifyAuth(result json.RawMessage) (AuthMode, error) {
	var envelope struct {
		Account json.RawMessage `json:"account"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		return "", fmt.Errorf("decode account/read response: %w", err)
	}
	if len(envelope.Account) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Account), []byte("null")) {
		return AuthNone, nil
	}

	var account struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(envelope.Account, &account); err != nil {
		return "", fmt.Errorf("decode account type: %w", err)
	}

	switch account.Type {
	case "apiKey":
		return AuthAPIKey, nil
	case "chatgpt", "chatgptAuthTokens":
		return AuthChatGPT, nil
	case "amazonBedrock":
		return AuthCustomProvider, nil
	case "":
		return AuthNone, nil
	default:
		return AuthOther, nil
	}
}
