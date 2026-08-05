package v11_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
	protocolv11 "github.com/yuanshu-ai/yuanshu/internal/protocol/v11"
)

var protocolNow = time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)

func TestValidatorAcceptsSignedAgentControl(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	trust := protocolv1.NewMemoryTrustStore()
	replay := protocolv1.NewMemoryReplayStore()
	ref := protocolv1.KeyRef{OwnerID: "owner", NodeID: "node", ClientID: "client", KeyID: "key"}
	if err := trust.Set(ref, protocolv1.TrustedKey{PublicKey: publicKey, Status: protocolv1.TrustStatusActive}); err != nil {
		t.Fatal(err)
	}
	validator, err := protocolv11.NewValidator(protocolv11.Options{TrustStore: trust, ReplayStore: replay, Now: func() time.Time { return protocolNow }})
	if err != nil {
		t.Fatal(err)
	}
	message := taskStartMessage(1, "message-v11", "AAAAAAAAAAAAAAAAAAAAAA")
	signatureInput, err := protocolv11.ControlSigningInput(message)
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, signatureInput))
	message.Signature = &signature
	raw, _ := json.Marshal(message)
	validated, err := validator.Validate(context.Background(), raw, protocolv11.Target{OwnerID: "owner", NodeID: "node"})
	if err != nil || validated.Message().AgentInstanceID == nil || *validated.Message().AgentInstanceID != "codex-default" {
		t.Fatalf("Validate() = %#v, %v", validated.Message(), err)
	}
}

func TestControlSequenceCannotDowngradeAcrossProtocolVersions(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	trust := protocolv1.NewMemoryTrustStore()
	replay := protocolv1.NewMemoryReplayStore()
	ref := protocolv1.KeyRef{OwnerID: "owner", NodeID: "node", ClientID: "client", KeyID: "key"}
	_ = trust.Set(ref, protocolv1.TrustedKey{PublicKey: publicKey, Status: protocolv1.TrustStatusActive})
	v11Validator, _ := protocolv11.NewValidator(protocolv11.Options{TrustStore: trust, ReplayStore: replay, Now: func() time.Time { return protocolNow }})
	v1Validator, _ := protocolv1.NewValidator(protocolv1.Options{TrustStore: trust, ReplayStore: replay, Now: func() time.Time { return protocolNow }})

	v11Message := taskStartMessage(8, "message-v11", "AAAAAAAAAAAAAAAAAAAAAA")
	input, _ := protocolv11.ControlSigningInput(v11Message)
	v11Signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, input))
	v11Message.Signature = &v11Signature
	v11Raw, _ := json.Marshal(v11Message)
	if _, err := v11Validator.Validate(context.Background(), v11Raw, protocolv11.Target{OwnerID: "owner", NodeID: "node"}); err != nil {
		t.Fatal(err)
	}

	expiresAt := protocolNow.Add(time.Minute).Format(time.RFC3339Nano)
	nonce := "AQEBAQEBAQEBAQEBAQEBAQ"
	v1Message := protocolv1.YuanshuMessage{
		ProtocolVersion: protocolv1.CurrentVersion, MessageID: "message-v1", Type: string(protocolv1.ControlDeviceSync),
		SentAt: protocolNow.Format(time.RFC3339Nano), ExpiresAt: &expiresAt, OwnerID: "owner", NodeID: "node",
		StreamID: "control-stream", Sequence: 8, CorrelationID: "correlation-v1", Nonce: &nonce,
		Signer: &protocolv1.Signer{ClientID: "client", KeyID: "key"}, Payload: map[string]any{},
	}
	v1Input, _ := protocolv1.ControlSigningInput(v1Message)
	v1Signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, v1Input))
	v1Message.Signature = &v1Signature
	v1Raw, _ := json.Marshal(v1Message)
	if _, err := v1Validator.Validate(context.Background(), v1Raw, protocolv1.Target{OwnerID: "owner", NodeID: "node"}); !errors.As(err, new(*protocolv1.ValidationError)) {
		t.Fatalf("downgrade validation error = %v", err)
	}
}

func TestProtocol11RejectsNativeSessionEnvelopeFields(t *testing.T) {
	message := taskStartMessage(1, "message", "AAAAAAAAAAAAAAAAAAAAAA")
	raw, _ := json.Marshal(message)
	var object map[string]any
	_ = json.Unmarshal(raw, &object)
	object["threadId"] = "native-codex-thread"
	raw, _ = json.Marshal(object)
	if _, err := protocolv11.ParseControl(raw); err == nil {
		t.Fatal("Protocol 1.1 accepted threadId")
	}
}

func taskStartMessage(sequence int64, messageID, nonce string) protocolv11.YuanshuMessage {
	expiresAt := protocolNow.Add(time.Minute).Format(time.RFC3339Nano)
	agentID, workspaceID := "codex-default", "workspace"
	return protocolv11.YuanshuMessage{
		ProtocolVersion: protocolv11.The11, MessageID: messageID, Type: protocolv11.Type(protocolv11.ControlTaskStart),
		SentAt: protocolNow.Format(time.RFC3339Nano), ExpiresAt: &expiresAt, OwnerID: "owner", NodeID: "node",
		AgentInstanceID: &agentID, WorkspaceID: &workspaceID, StreamID: "control-stream", Sequence: sequence,
		CorrelationID: "correlation", Nonce: &nonce, Signer: &protocolv11.Signer{ClientID: "client", KeyID: "key"},
		Payload: map[string]any{"input": "Inspect this project"},
	}
}
