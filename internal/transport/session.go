package transport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/gowebpki/jcs"
)

const (
	RelaySubprotocol     = "yuanshu-relay-v1"
	SessionSigningDomain = "yuanshu-relay-session-v1\x00"
)

type SessionRole string

const (
	SessionRoleNode    SessionRole = "node"
	SessionRoleControl SessionRole = "control"
)

type SessionChallenge struct {
	Version      string      `json:"version"`
	Type         string      `json:"type"`
	Role         SessionRole `json:"role"`
	ConnectionID string      `json:"connectionId"`
	SubjectID    string      `json:"subjectId"`
	Nonce        string      `json:"nonce"`
	ExpiresAt    string      `json:"expiresAt"`
}

type SessionAuthentication struct {
	Version   string `json:"version"`
	Type      string `json:"type"`
	Signature string `json:"signature"`
}

type SessionReady struct {
	Version string `json:"version"`
	Type    string `json:"type"`
}

type RelayDialOptions struct {
	HTTPClient *http.Client
	Header     http.Header
	Role       SessionRole
	SubjectID  string
	Sign       func(context.Context, []byte) ([]byte, error)
	Relay      RelayOptions
	Clock      func() time.Time
}

// DialRelay establishes and authenticates one WSS connection. Reconnection is
// intentionally owned by a higher-level connection manager.
func DialRelay(ctx context.Context, url string, options RelayDialOptions) (Transport, *http.Response, error) {
	if ctx == nil {
		return nil, nil, context.Canceled
	}
	if options.SubjectID == "" || options.Sign == nil || (options.Role != SessionRoleNode && options.Role != SessionRoleControl) {
		return nil, nil, errors.New("relay session options are invalid")
	}
	conn, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient: options.HTTPClient, HTTPHeader: options.Header, Subprotocols: []string{RelaySubprotocol},
	})
	if err != nil {
		return nil, response, errors.New("relay connection failed")
	}
	fail := func(err error) (Transport, *http.Response, error) {
		_ = conn.CloseNow()
		return nil, response, err
	}
	if conn.Subprotocol() != RelaySubprotocol {
		return fail(errors.New("relay subprotocol negotiation failed"))
	}
	conn.SetReadLimit(64 << 10)
	var challenge SessionChallenge
	if err := readStrictSessionJSON(ctx, conn, &challenge); err != nil {
		return fail(errors.New("relay authentication failed"))
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, challenge.ExpiresAt)
	if err != nil || challenge.Version != "1" || challenge.Type != "challenge" || challenge.Role != options.Role || challenge.SubjectID != options.SubjectID || challenge.ConnectionID == "" || !clock().UTC().Before(expiresAt) {
		return fail(errors.New("relay authentication failed"))
	}
	if nonce, err := base64.RawURLEncoding.DecodeString(challenge.Nonce); err != nil || len(nonce) != 32 || base64.RawURLEncoding.EncodeToString(nonce) != challenge.Nonce {
		return fail(errors.New("relay authentication failed"))
	}
	input, err := SessionSigningInput(challenge)
	if err != nil {
		return fail(errors.New("relay authentication failed"))
	}
	signature, err := options.Sign(ctx, input)
	if err != nil || len(signature) != 64 {
		return fail(errors.New("relay authentication failed"))
	}
	authentication := SessionAuthentication{Version: "1", Type: "authenticate", Signature: base64.RawURLEncoding.EncodeToString(signature)}
	clear(signature)
	encoded, _ := json.Marshal(authentication)
	if err := conn.Write(ctx, websocket.MessageText, encoded); err != nil {
		return fail(errors.New("relay authentication failed"))
	}
	var ready SessionReady
	if err := readStrictSessionJSON(ctx, conn, &ready); err != nil || ready.Version != "1" || ready.Type != "authenticated" {
		return fail(errors.New("relay authentication failed"))
	}
	relay, err := NewRelay(conn, options.Relay)
	if err != nil {
		return fail(err)
	}
	return relay, response, nil
}

func SessionSigningInput(challenge SessionChallenge) ([]byte, error) {
	if challenge.Version != "1" || challenge.Type != "challenge" || challenge.ConnectionID == "" || challenge.SubjectID == "" || challenge.Nonce == "" || challenge.ExpiresAt == "" ||
		(challenge.Role != SessionRoleNode && challenge.Role != SessionRoleControl) {
		return nil, errors.New("relay challenge is invalid")
	}
	encoded, err := json.Marshal(challenge)
	if err != nil {
		return nil, errors.New("relay challenge is invalid")
	}
	canonical, err := jcs.Transform(encoded)
	if err != nil {
		return nil, errors.New("relay challenge is invalid")
	}
	return append([]byte(SessionSigningDomain), canonical...), nil
}

func readStrictSessionJSON(ctx context.Context, conn *websocket.Conn, target any) error {
	messageType, raw, err := conn.Read(ctx)
	if err != nil || messageType != websocket.MessageText {
		return errors.New("relay session message is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.More() {
		return errors.New("relay session message is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("relay session message is invalid")
	}
	return nil
}
