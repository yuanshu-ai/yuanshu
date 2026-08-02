package transport

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRelayTransportAuthenticatesAndPreservesFrames(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	server := newAuthenticatedEchoServer(t, public)
	defer server.Close()
	url := "wss" + strings.TrimPrefix(server.URL, "https")
	relay, response, err := DialRelay(context.Background(), url, RelayDialOptions{
		HTTPClient: server.Client(), Role: SessionRoleNode, SubjectID: "nod_test",
		Sign:  func(_ context.Context, input []byte) ([]byte, error) { return ed25519.Sign(private, input), nil },
		Relay: RelayOptions{MaxSendBytes: 64, MaxReceiveBytes: 64, HeartbeatInterval: time.Second, IdleTimeout: 3 * time.Second},
	})
	if err != nil || response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("DialRelay() response=%v err=%v", response, err)
	}
	defer relay.Close()
	raw := []byte(` { "signature" : "synthetic", "type" : "not-json-normalized" } `)
	frame := NewFrame(raw)
	raw[1] = 'X'
	if err := relay.Send(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	received, err := relay.Receive(context.Background())
	if err != nil || string(received.Bytes()) != ` { "signature" : "synthetic", "type" : "not-json-normalized" } ` {
		t.Fatalf("Receive()=%q err=%v", received.Bytes(), err)
	}
	copy := received.Bytes()
	copy[0] = 'X'
	if received.Bytes()[0] == 'X' {
		t.Fatal("received frame shared caller bytes")
	}
	if err := relay.Send(context.Background(), NewFrame(make([]byte, 65))); err != ErrFrameTooLarge {
		t.Fatalf("oversize error=%v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := relay.Receive(canceled); err != context.Canceled {
		t.Fatalf("canceled receive error=%v", err)
	}
}

func TestDialRelayRejectsWrongSignatureWithoutLeakingIt(t *testing.T) {
	public, _, _ := ed25519.GenerateKey(nil)
	server := newAuthenticatedEchoServer(t, public)
	defer server.Close()
	url := "wss" + strings.TrimPrefix(server.URL, "https")
	canary := []byte("signature-canary-that-must-not-appear-in-errors")
	_, _, err := DialRelay(context.Background(), url, RelayDialOptions{
		HTTPClient: server.Client(), Role: SessionRoleNode, SubjectID: "nod_test",
		Sign: func(context.Context, []byte) ([]byte, error) {
			return append(make([]byte, 64-len(canary)), canary...), nil
		},
		Relay: RelayOptions{MaxSendBytes: 64, MaxReceiveBytes: 64},
	})
	if err == nil || strings.Contains(err.Error(), string(canary)) {
		t.Fatalf("unsafe authentication error=%v", err)
	}
}

func TestRelayTransportReportsInboundLimitAndDeterministicBackpressure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_ = conn.Write(request.Context(), websocket.MessageText, make([]byte, 65))
		<-request.Context().Done()
	}))
	defer server.Close()
	conn, _, err := websocket.Dial(context.Background(), "wss"+strings.TrimPrefix(server.URL, "https"), &websocket.DialOptions{HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	relay, err := NewRelay(conn, RelayOptions{MaxSendBytes: 64, MaxReceiveBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	if _, err := relay.Receive(context.Background()); err != ErrFrameTooLarge {
		t.Fatalf("inbound limit error=%v", err)
	}

	queued := &relayTransport{maxSend: 64, send: make(chan Frame, 1), done: make(chan struct{})}
	if err := queued.Send(context.Background(), NewFrame([]byte("first"))); err != nil {
		t.Fatal(err)
	}
	if err := queued.Send(context.Background(), NewFrame([]byte("second"))); err != ErrBackpressure {
		t.Fatalf("backpressure error=%v", err)
	}
}

func TestSessionSigningInputIsDeterministicAndBound(t *testing.T) {
	challenge := SessionChallenge{Version: "1", Type: "challenge", Role: SessionRoleNode, ConnectionID: "connection", SubjectID: "node", Nonce: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ExpiresAt: "2026-08-02T12:00:00Z"}
	first, err := SessionSigningInput(challenge)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := SessionSigningInput(challenge)
	if string(first) != string(second) || !strings.HasPrefix(string(first), SessionSigningDomain) {
		t.Fatal("session signing input is not deterministic or domain separated")
	}
	challenge.SubjectID = "other"
	changed, _ := SessionSigningInput(challenge)
	if string(first) == string(changed) {
		t.Fatal("subject was not bound into signing input")
	}
}

func newAuthenticatedEchoServer(t *testing.T, public ed25519.PublicKey) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := websocket.Accept(writer, request, &websocket.AcceptOptions{Subprotocols: []string{RelaySubprotocol}})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		challenge := SessionChallenge{
			Version: "1", Type: "challenge", Role: SessionRoleNode, ConnectionID: "connection", SubjectID: "nod_test",
			Nonce: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
		}
		encoded, _ := json.Marshal(challenge)
		if err := conn.Write(request.Context(), websocket.MessageText, encoded); err != nil {
			return
		}
		var authentication SessionAuthentication
		if err := readStrictSessionJSON(request.Context(), conn, &authentication); err != nil {
			return
		}
		signature, err := base64.RawURLEncoding.DecodeString(authentication.Signature)
		input, inputErr := SessionSigningInput(challenge)
		if err != nil || inputErr != nil || !ed25519.Verify(public, input, signature) {
			_ = conn.Close(websocket.StatusPolicyViolation, "authentication failed")
			return
		}
		ready, _ := json.Marshal(SessionReady{Version: "1", Type: "authenticated"})
		if err := conn.Write(request.Context(), websocket.MessageText, ready); err != nil {
			return
		}
		for {
			messageType, raw, err := conn.Read(request.Context())
			if err != nil {
				return
			}
			if err := conn.Write(request.Context(), messageType, raw); err != nil {
				return
			}
		}
	}))
}
