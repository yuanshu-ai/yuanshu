// Package appserver implements the production stdio JSONL boundary for a
// Codex app-server process. It deliberately contains no Yuanshu business
// policy or persistence logic.
package appserver

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
	ErrClosed          = errors.New("codex app-server connection is closed")
	ErrMessageTooLarge = errors.New("codex app-server message is too large")
	ErrQueueFull       = errors.New("codex app-server event queue is full")
	ErrInvalidMessage  = errors.New("codex app-server message is invalid")
	ErrProcessExited   = errors.New("codex app-server process exited")
)

type RequestID struct{ raw json.RawMessage }

func parseRequestID(raw json.RawMessage) (RequestID, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return RequestID{}, ErrInvalidMessage
	}
	var integer int64
	if json.Unmarshal(raw, &integer) == nil {
		return RequestID{raw: append(json.RawMessage(nil), raw...)}, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return RequestID{raw: append(json.RawMessage(nil), raw...)}, nil
	}
	return RequestID{}, ErrInvalidMessage
}

func (id RequestID) MarshalJSON() ([]byte, error) {
	if len(id.raw) == 0 {
		return nil, ErrInvalidMessage
	}
	return append([]byte(nil), id.raw...), nil
}

func (id RequestID) Equal(other RequestID) bool { return bytes.Equal(id.raw, other.raw) }
func (id RequestID) Matches(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(id.raw), bytes.TrimSpace(raw))
}

type Message struct {
	ID     *RequestID
	Method string
	Params json.RawMessage
}

func (m Message) IsRequest() bool { return m.ID != nil }

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("codex app-server RPC error %d", e.Code)
}

type ClientInfo struct {
	Name    string  `json:"name"`
	Title   *string `json:"title,omitempty"`
	Version string  `json:"version"`
}

type InitializeResult struct {
	UserAgent      string `json:"userAgent"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
}
