package protocol_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/gowebpki/jcs"
	v1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

type signingFixture struct {
	ControlDomain   string `json:"controlDomain"`
	OperationDomain string `json:"operationDomain"`
	JCSCases        []struct {
		Name          string `json:"name"`
		InputJSON     string `json:"inputJson"`
		CanonicalJSON string `json:"canonicalJson"`
	} `json:"jcsCases"`
	Control struct {
		TestOnlySeedHex           string            `json:"testOnlySeedHex"`
		PublicKeyBase64URL        string            `json:"publicKeyBase64Url"`
		Message                   v1.YuanshuMessage `json:"message"`
		CanonicalWithoutSignature string            `json:"canonicalWithoutSignature"`
		SigningInputBase64URL     string            `json:"signingInputBase64Url"`
		SignatureBase64URL        string            `json:"signatureBase64Url"`
	} `json:"control"`
	Approval struct {
		Message          v1.YuanshuMessage `json:"message"`
		CanonicalBinding string            `json:"canonicalBinding"`
		OperationDigest  string            `json:"operationDigest"`
	} `json:"approval"`
}

func TestJCSSharedVectors(t *testing.T) {
	fixture := loadSigningFixture(t)
	for _, testCase := range fixture.JCSCases {
		t.Run(testCase.Name, func(t *testing.T) {
			canonical, err := jcs.Transform([]byte(testCase.InputJSON))
			if err != nil {
				t.Fatal(err)
			}
			if string(canonical) != testCase.CanonicalJSON {
				t.Fatalf("canonical JSON = %q, want %q", canonical, testCase.CanonicalJSON)
			}
		})
	}
}

func TestControlSigningVector(t *testing.T) {
	fixture := loadSigningFixture(t)
	if fixture.ControlDomain != v1.ControlSigningDomain || fixture.OperationDomain != v1.OperationDigestDomain {
		t.Fatal("fixture domains do not match protocol constants")
	}
	original := mustJSON(t, fixture.Control.Message)
	input, err := v1.ControlSigningInput(fixture.Control.Message)
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.RawURLEncoding.EncodeToString(input); got != fixture.Control.SigningInputBase64URL {
		t.Fatalf("signing input = %q, want %q", got, fixture.Control.SigningInputBase64URL)
	}
	canonical := input[len(v1.ControlSigningDomain):]
	if string(canonical) != fixture.Control.CanonicalWithoutSignature {
		t.Fatalf("canonical control = %q, want %q", canonical, fixture.Control.CanonicalWithoutSignature)
	}
	if after := mustJSON(t, fixture.Control.Message); after != original {
		t.Fatal("ControlSigningInput mutated its input")
	}

	seed, err := hex.DecodeString(fixture.Control.TestOnlySeedHex)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	if got := base64.RawURLEncoding.EncodeToString(publicKey); got != fixture.Control.PublicKeyBase64URL {
		t.Fatalf("public key = %q, want %q", got, fixture.Control.PublicKeyBase64URL)
	}
	signature := ed25519.Sign(privateKey, input)
	if got := base64.RawURLEncoding.EncodeToString(signature); got != fixture.Control.SignatureBase64URL {
		t.Fatalf("signature = %q, want %q", got, fixture.Control.SignatureBase64URL)
	}
	if !ed25519.Verify(publicKey, input, signature) {
		t.Fatal("fixed Ed25519 signature did not verify")
	}
	if fixture.Control.Message.Signature == nil || *fixture.Control.Message.Signature != fixture.Control.SignatureBase64URL {
		t.Fatal("wire message does not contain the fixed signature")
	}
}

func TestControlSigningInputBindsEnvelopeAndPayload(t *testing.T) {
	fixture := loadSigningFixture(t)
	baseline, err := v1.ControlSigningInput(fixture.Control.Message)
	if err != nil {
		t.Fatal(err)
	}
	publicKeyBytes, err := base64.RawURLEncoding.DecodeString(fixture.Control.PublicKeyBase64URL)
	if err != nil {
		t.Fatal(err)
	}
	signatureBytes, err := base64.RawURLEncoding.DecodeString(fixture.Control.SignatureBase64URL)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := ed25519.PublicKey(publicKeyBytes)

	mutations := map[string]func(*v1.YuanshuMessage){
		"messageId":     func(message *v1.YuanshuMessage) { message.MessageID += "_changed" },
		"type":          func(message *v1.YuanshuMessage) { message.Type = "turn.steer" },
		"sentAt":        func(message *v1.YuanshuMessage) { message.SentAt = "2026-08-01T06:00:01Z" },
		"expiresAt":     func(message *v1.YuanshuMessage) { changed := "2026-08-01T06:02:00Z"; message.ExpiresAt = &changed },
		"ownerId":       func(message *v1.YuanshuMessage) { message.OwnerID += "_changed" },
		"nodeId":        func(message *v1.YuanshuMessage) { message.NodeID += "_changed" },
		"workspaceId":   func(message *v1.YuanshuMessage) { changed := "workspace_2"; message.WorkspaceID = &changed },
		"threadId":      func(message *v1.YuanshuMessage) { changed := "thread_2"; message.ThreadID = &changed },
		"turnId":        func(message *v1.YuanshuMessage) { changed := "turn_2"; message.TurnID = &changed },
		"itemId":        func(message *v1.YuanshuMessage) { changed := "item_2"; message.ItemID = &changed },
		"streamId":      func(message *v1.YuanshuMessage) { message.StreamID += "_changed" },
		"sequence":      func(message *v1.YuanshuMessage) { message.Sequence-- },
		"correlationId": func(message *v1.YuanshuMessage) { message.CorrelationID += "_changed" },
		"nonce":         func(message *v1.YuanshuMessage) { changed := "different_nonce_1234"; message.Nonce = &changed },
		"signer":        func(message *v1.YuanshuMessage) { message.Signer.KeyID += "_changed" },
		"payload":       func(message *v1.YuanshuMessage) { message.Payload["input"] = "changed" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			message := cloneMessage(t, fixture.Control.Message)
			mutate(&message)
			changed, err := v1.ControlSigningInput(message)
			if err != nil {
				t.Fatal(err)
			}
			if string(changed) == string(baseline) {
				t.Fatal("signed field mutation did not change signing input")
			}
			if ed25519.Verify(publicKey, changed, signatureBytes) {
				t.Fatal("fixed signature verified after a signed field mutation")
			}
		})
	}

	message := cloneMessage(t, fixture.Control.Message)
	changedSignature := strings.Repeat("B", 86)
	message.Signature = &changedSignature
	unchanged, err := v1.ControlSigningInput(message)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != string(baseline) {
		t.Fatal("signature member unexpectedly changed signing input")
	}
}

func TestApprovalOperationDigestVectorAndBinding(t *testing.T) {
	fixture := loadSigningFixture(t)
	original := mustJSON(t, fixture.Approval.Message)
	digest, err := v1.ApprovalOperationDigest(fixture.Approval.Message)
	if err != nil {
		t.Fatal(err)
	}
	if digest != fixture.Approval.OperationDigest {
		t.Fatalf("operation digest = %q, want %q", digest, fixture.Approval.OperationDigest)
	}
	if after := mustJSON(t, fixture.Approval.Message); after != original {
		t.Fatal("ApprovalOperationDigest mutated its input")
	}

	transportMutations := []func(*v1.YuanshuMessage){
		func(message *v1.YuanshuMessage) { message.MessageID = "replayed_message" },
		func(message *v1.YuanshuMessage) { message.SentAt = "2026-08-01T07:00:00Z" },
		func(message *v1.YuanshuMessage) { message.StreamID = "replay_stream" },
		func(message *v1.YuanshuMessage) { message.Sequence = 999 },
		func(message *v1.YuanshuMessage) { message.CorrelationID = "replay_request" },
		func(message *v1.YuanshuMessage) { message.Payload["operationDigest"] = strings.Repeat("B", 43) },
	}
	for _, mutate := range transportMutations {
		message := cloneMessage(t, fixture.Approval.Message)
		mutate(&message)
		got, err := v1.ApprovalOperationDigest(message)
		if err != nil {
			t.Fatal(err)
		}
		if got != digest {
			t.Fatal("replay metadata changed operation digest")
		}
	}

	bindingMutations := []func(*v1.YuanshuMessage){
		func(message *v1.YuanshuMessage) { message.NodeID = "node_2" },
		func(message *v1.YuanshuMessage) { changed := "workspace_2"; message.WorkspaceID = &changed },
		func(message *v1.YuanshuMessage) { message.Payload["approvalId"] = "approval_2" },
		func(message *v1.YuanshuMessage) { message.Payload["summary"] = "Changed summary" },
		func(message *v1.YuanshuMessage) { message.Payload["futureField"] = true },
	}
	for _, mutate := range bindingMutations {
		message := cloneMessage(t, fixture.Approval.Message)
		mutate(&message)
		got, err := v1.ApprovalOperationDigest(message)
		if err != nil {
			t.Fatal(err)
		}
		if got == digest {
			t.Fatal("bound operation mutation did not change digest")
		}
	}
}

func TestSigningEncodingRejectsInvalidInput(t *testing.T) {
	fixture := loadSigningFixture(t)
	tests := map[string]func(*v1.YuanshuMessage){
		"wrong version":     func(message *v1.YuanshuMessage) { message.ProtocolVersion = "1.1" },
		"event type":        func(message *v1.YuanshuMessage) { message.Type = "turn.completed" },
		"unsafe sequence":   func(message *v1.YuanshuMessage) { message.Sequence = 9007199254740992 },
		"non-finite number": func(message *v1.YuanshuMessage) { message.Payload["input"] = math.Inf(1) },
		"invalid UTF-8":     func(message *v1.YuanshuMessage) { message.OwnerID = string([]byte{0xff}) },
		"missing signer":    func(message *v1.YuanshuMessage) { message.Signer = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			message := cloneMessage(t, fixture.Control.Message)
			mutate(&message)
			if _, err := v1.ControlSigningInput(message); err == nil {
				t.Fatal("expected encoding error")
			}
		})
	}

	if _, err := v1.ApprovalOperationDigest(fixture.Control.Message); err == nil {
		t.Fatal("control message was accepted as an approval event")
	}
}

func loadSigningFixture(t *testing.T) signingFixture {
	t.Helper()
	var fixture signingFixture
	readFixture(t, "fixtures/signing-vectors.json", &fixture)
	return fixture
}

func cloneMessage(t *testing.T, message v1.YuanshuMessage) v1.YuanshuMessage {
	t.Helper()
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var cloned v1.YuanshuMessage
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
