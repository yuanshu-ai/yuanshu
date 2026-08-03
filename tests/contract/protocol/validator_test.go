package protocol_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

var validatorNow = time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)

type validatorHarness struct {
	private ed25519.PrivateKey
	public  ed25519.PublicKey
	ref     v1.KeyRef
	trust   *v1.MemoryTrustStore
	replay  *v1.MemoryReplayStore
	target  v1.Target
}

func newValidatorHarness(t *testing.T) validatorHarness {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = byte(index + 1)
	}
	private := ed25519.NewKeyFromSeed(seed)
	public := private.Public().(ed25519.PublicKey)
	ref := v1.KeyRef{OwnerID: "owner_test", NodeID: "node_test", ClientID: "client_test", KeyID: "key_test"}
	trust := v1.NewMemoryTrustStore()
	if err := trust.Set(ref, v1.TrustedKey{PublicKey: public, Status: v1.TrustStatusActive}); err != nil {
		t.Fatal(err)
	}
	return validatorHarness{
		private: private,
		public:  public,
		ref:     ref,
		trust:   trust,
		replay:  v1.NewMemoryReplayStore(),
		target:  v1.Target{OwnerID: ref.OwnerID, NodeID: ref.NodeID},
	}
}

func (h validatorHarness) validator(t *testing.T) *v1.Validator {
	t.Helper()
	validator, err := v1.NewValidator(v1.Options{
		TrustStore: h.trust, ReplayStore: h.replay, Now: func() time.Time { return validatorNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return validator
}

func TestValidatorAcceptsEveryControlType(t *testing.T) {
	for index, controlType := range v1.KnownControlTypes {
		t.Run(string(controlType), func(t *testing.T) {
			harness := newValidatorHarness(t)
			message := validControlMessage(string(controlType), int64(index+1), harness.ref)
			raw := signControl(t, message, harness.private)
			validated, err := harness.validator(t).Validate(context.Background(), raw, harness.target)
			if err != nil {
				t.Fatal(err)
			}
			if got := validated.Message(); got.Type != string(controlType) || got.MessageID != message.MessageID {
				t.Fatalf("validated message = %s/%s", got.Type, got.MessageID)
			}
			if got := validated.Signer(); got != harness.ref {
				t.Fatalf("validated signer = %#v, want %#v", got, harness.ref)
			}
			first := validated.Message()
			first.Payload["mutated"] = true
			if _, found := validated.Message().Payload["mutated"]; found {
				t.Fatal("ValidatedControl returned a mutable internal payload")
			}
		})
	}
}

func TestValidatorAcceptsEquivalentRawJSONEncoding(t *testing.T) {
	harness := newValidatorHarness(t)
	message := validControlMessage("turn.start", 1, harness.ref)
	message.Payload["input"] = "synthetic 😀"
	raw := signControl(t, message, harness.private)
	raw = []byte(strings.Replace(string(raw), "😀", `\uD83D\uDE00`, 1))
	var indented bytes.Buffer
	if err := json.Indent(&indented, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.validator(t).Validate(context.Background(), []byte(indented.String()), harness.target); err != nil {
		t.Fatalf("equivalent whitespace and surrogate-pair encoding was rejected: %v", err)
	}
}

func TestValidatorRejectsMalformedFramesAndHeaders(t *testing.T) {
	harness := newValidatorHarness(t)
	validRaw := signControl(t, validControlMessage("turn.start", 1, harness.ref), harness.private)
	validText := string(validRaw)

	tests := []struct {
		name string
		raw  []byte
		code v1.ErrorCode
	}{
		{"oversized", []byte(strings.Repeat("x", v1.ControlFrameMaxBytes+1)), v1.ErrorPayloadTooLarge},
		{"empty", nil, v1.ErrorInvalidMessage},
		{"malformed", []byte(`{"protocolVersion":`), v1.ErrorInvalidMessage},
		{"invalid utf8", append([]byte(`{"x":"`), 0xff, '"', '}'), v1.ErrorInvalidMessage},
		{"duplicate top-level key", []byte(strings.Replace(validText, `"messageId":`, `"messageId":"duplicate","messageId":`, 1)), v1.ErrorInvalidMessage},
		{"duplicate nested key", []byte(strings.Replace(validText, `"input":"synthetic input"`, `"input":"synthetic input","input":"duplicate"`, 1)), v1.ErrorInvalidMessage},
		{"lone high surrogate", []byte(strings.Replace(validText, "synthetic input", `\uD800`, 1)), v1.ErrorInvalidMessage},
		{"lone low surrogate", []byte(strings.Replace(validText, "synthetic input", `\uDC00`, 1)), v1.ErrorInvalidMessage},
		{"multiple values", append(append([]byte(nil), validRaw...), []byte(` {}`)...), v1.ErrorInvalidMessage},
		{"non-json number", []byte(strings.Replace(validText, `"sequence":1`, `"sequence":NaN`, 1)), v1.ErrorInvalidMessage},
		{"unknown envelope field", mutateRaw(t, validRaw, func(message map[string]any) { message["unexpected"] = true }), v1.ErrorInvalidMessage},
		{"wrong payload", mutateRaw(t, validRaw, func(message map[string]any) { message["payload"] = map[string]any{"force": true} }), v1.ErrorInvalidMessage},
		{"new major", mutateRaw(t, validRaw, func(message map[string]any) { message["protocolVersion"] = "2.0" }), v1.ErrorUnsupportedProtocol},
		{"new minor", mutateRaw(t, validRaw, func(message map[string]any) { message["protocolVersion"] = "1.1" }), v1.ErrorUnsupportedProtocol},
		{"unknown control", mutateRaw(t, validRaw, func(message map[string]any) { message["type"] = "turn.attach" }), v1.ErrorUnsupportedControl},
		{"event on control path", mutateRaw(t, validRaw, func(message map[string]any) { message["type"] = "turn.completed" }), v1.ErrorUnsupportedControl},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := harness.validator(t).Validate(context.Background(), testCase.raw, harness.target)
			assertValidationCode(t, err, testCase.code)
		})
	}
}

func TestValidatorEnforcesTargetTrustAndSignature(t *testing.T) {
	t.Run("owner target", func(t *testing.T) {
		harness := newValidatorHarness(t)
		message := validControlMessage("turn.start", 1, harness.ref)
		message.OwnerID = "other_owner"
		_, err := harness.validator(t).Validate(context.Background(), signControl(t, message, harness.private), harness.target)
		assertValidationCode(t, err, v1.ErrorForbidden)
	})
	t.Run("node target", func(t *testing.T) {
		harness := newValidatorHarness(t)
		message := validControlMessage("turn.start", 1, harness.ref)
		message.NodeID = "other_node"
		_, err := harness.validator(t).Validate(context.Background(), signControl(t, message, harness.private), harness.target)
		assertValidationCode(t, err, v1.ErrorForbidden)
	})
	t.Run("unknown signer", func(t *testing.T) {
		harness := newValidatorHarness(t)
		message := validControlMessage("turn.start", 1, harness.ref)
		message.Signer.KeyID = "unknown_key"
		_, err := harness.validator(t).Validate(context.Background(), signControl(t, message, harness.private), harness.target)
		assertValidationCode(t, err, v1.ErrorUnauthorized)
	})
	t.Run("revoked signer", func(t *testing.T) {
		harness := newValidatorHarness(t)
		if err := harness.trust.Revoke(harness.ref); err != nil {
			t.Fatal(err)
		}
		message := validControlMessage("turn.start", 1, harness.ref)
		_, err := harness.validator(t).Validate(context.Background(), signControl(t, message, harness.private), harness.target)
		assertValidationCode(t, err, v1.ErrorForbidden)
	})
	t.Run("malformed public key", func(t *testing.T) {
		harness := newValidatorHarness(t)
		validator := mustValidator(t, v1.Options{
			TrustStore:  staticTrustStore{key: v1.TrustedKey{PublicKey: []byte("short"), Status: v1.TrustStatusActive}},
			ReplayStore: harness.replay, Now: func() time.Time { return validatorNow },
		})
		message := validControlMessage("turn.start", 1, harness.ref)
		_, err := validator.Validate(context.Background(), signControl(t, message, harness.private), harness.target)
		assertValidationCode(t, err, v1.ErrorUnauthorized)
	})
	t.Run("trust backend failure", func(t *testing.T) {
		harness := newValidatorHarness(t)
		validator := mustValidator(t, v1.Options{
			TrustStore: staticTrustStore{err: errors.New("backend unavailable")}, ReplayStore: harness.replay,
			Now: func() time.Time { return validatorNow },
		})
		message := validControlMessage("turn.start", 1, harness.ref)
		_, err := validator.Validate(context.Background(), signControl(t, message, harness.private), harness.target)
		assertValidationCode(t, err, v1.ErrorInternal)
	})
	t.Run("payload tamper", func(t *testing.T) {
		harness := newValidatorHarness(t)
		raw := signControl(t, validControlMessage("turn.start", 1, harness.ref), harness.private)
		tampered := mutateRaw(t, raw, func(message map[string]any) { message["payload"].(map[string]any)["input"] = "tampered" })
		_, err := harness.validator(t).Validate(context.Background(), tampered, harness.target)
		assertValidationCode(t, err, v1.ErrorUnauthorized)
	})
	t.Run("signature tamper", func(t *testing.T) {
		harness := newValidatorHarness(t)
		raw := signControl(t, validControlMessage("turn.start", 1, harness.ref), harness.private)
		tampered := mutateRaw(t, raw, func(message map[string]any) {
			signature := message["signature"].(string)
			if signature[0] == 'A' {
				message["signature"] = "B" + signature[1:]
			} else {
				message["signature"] = "A" + signature[1:]
			}
		})
		_, err := harness.validator(t).Validate(context.Background(), tampered, harness.target)
		assertValidationCode(t, err, v1.ErrorUnauthorized)
	})
}

func TestValidatorRejectsEverySignedFieldMutation(t *testing.T) {
	mutations := map[string]func(map[string]any){
		"messageId": func(message map[string]any) { message["messageId"] = "message_tampered" },
		"type":      func(message map[string]any) { message["type"] = "turn.steer" },
		"sentAt": func(message map[string]any) {
			message["sentAt"] = validatorNow.Add(-time.Second).Format(time.RFC3339Nano)
		},
		"expiresAt": func(message map[string]any) {
			message["expiresAt"] = validatorNow.Add(2 * time.Minute).Format(time.RFC3339Nano)
		},
		"workspaceId":     func(message map[string]any) { message["workspaceId"] = "workspace_tampered" },
		"streamId":        func(message map[string]any) { message["streamId"] = "stream_tampered" },
		"sequence":        func(message map[string]any) { message["sequence"] = 2 },
		"correlationId":   func(message map[string]any) { message["correlationId"] = "correlation_tampered" },
		"nonce":           func(message map[string]any) { message["nonce"] = canonicalNonce(999) },
		"signer.clientId": func(message map[string]any) { message["signer"].(map[string]any)["clientId"] = "client_tampered" },
		"payload":         func(message map[string]any) { message["payload"].(map[string]any)["input"] = "tampered" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			harness := newValidatorHarness(t)
			original := signControl(t, validControlMessage("turn.start", 1, harness.ref), harness.private)
			_, err := harness.validator(t).Validate(context.Background(), mutateRaw(t, original, mutate), harness.target)
			assertValidationCode(t, err, v1.ErrorUnauthorized)
		})
	}
}

func TestValidatorTimeAndNonceBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		sentAt    time.Time
		expiresAt time.Time
		nonce     string
		wantCode  v1.ErrorCode
	}{
		{"exact ttl", validatorNow, validatorNow.Add(2 * time.Minute), canonicalNonce(1), ""},
		{"future skew boundary", validatorNow.Add(30 * time.Second), validatorNow.Add(31 * time.Second), canonicalNonce(2), ""},
		{"expiry skew boundary", validatorNow.Add(-2 * time.Minute), validatorNow.Add(-30 * time.Second), canonicalNonce(3), ""},
		{"equal timestamps", validatorNow, validatorNow, canonicalNonce(4), v1.ErrorExpired},
		{"ttl too long", validatorNow, validatorNow.Add(2*time.Minute + time.Nanosecond), canonicalNonce(5), v1.ErrorExpired},
		{"sent too far future", validatorNow.Add(30*time.Second + time.Nanosecond), validatorNow.Add(time.Minute), canonicalNonce(6), v1.ErrorExpired},
		{"expired beyond skew", validatorNow.Add(-2 * time.Minute), validatorNow.Add(-30*time.Second - time.Nanosecond), canonicalNonce(7), v1.ErrorExpired},
		{"short nonce", validatorNow, validatorNow.Add(time.Minute), "short", v1.ErrorInvalidMessage},
		{"padded nonce", validatorNow, validatorNow.Add(time.Minute), canonicalNonce(8) + "=", v1.ErrorInvalidMessage},
		{"noncanonical nonce", validatorNow, validatorNow.Add(time.Minute), "AAAAAAAAAAAAAAAAAAAAAB", v1.ErrorInvalidMessage},
	}
	for index, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newValidatorHarness(t)
			message := validControlMessage("turn.start", int64(index+1), harness.ref)
			message.SentAt = testCase.sentAt.Format(time.RFC3339Nano)
			expiresAt := testCase.expiresAt.Format(time.RFC3339Nano)
			message.ExpiresAt = &expiresAt
			message.Nonce = &testCase.nonce
			_, err := harness.validator(t).Validate(context.Background(), signControl(t, message, harness.private), harness.target)
			if testCase.wantCode == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertValidationCode(t, err, testCase.wantCode)
		})
	}
}

func TestValidatorReplayRevocationAndAtomicity(t *testing.T) {
	t.Run("all replay dimensions and key rotation", func(t *testing.T) {
		harness := newValidatorHarness(t)
		validator := harness.validator(t)
		base := validControlMessage("turn.start", 10, harness.ref)
		if _, err := validator.Validate(context.Background(), signControl(t, base, harness.private), harness.target); err != nil {
			t.Fatal(err)
		}
		assertReplay := func(message v1.YuanshuMessage) {
			t.Helper()
			_, err := validator.Validate(context.Background(), signControl(t, message, harness.private), harness.target)
			assertValidationCode(t, err, v1.ErrorReplay)
		}
		assertReplay(base)

		duplicateMessage := validControlMessage("turn.start", 11, harness.ref)
		duplicateMessage.MessageID = base.MessageID
		assertReplay(duplicateMessage)

		duplicateNonce := validControlMessage("turn.start", 11, harness.ref)
		duplicateNonce.MessageID = "message_nonce_replay"
		duplicateNonce.Nonce = base.Nonce
		assertReplay(duplicateNonce)

		equalSequence := validControlMessage("turn.start", 10, harness.ref)
		equalSequence.MessageID = "message_equal_sequence"
		nonce := canonicalNonce(100)
		equalSequence.Nonce = &nonce
		assertReplay(equalSequence)

		lowerSequence := validControlMessage("turn.start", 9, harness.ref)
		lowerSequence.MessageID = "message_lower_sequence"
		nonce = canonicalNonce(101)
		lowerSequence.Nonce = &nonce
		assertReplay(lowerSequence)

		higher := validControlMessage("turn.start", 11, harness.ref)
		higher.MessageID = "message_higher_sequence"
		nonce = canonicalNonce(102)
		higher.Nonce = &nonce
		if _, err := validator.Validate(context.Background(), signControl(t, higher, harness.private), harness.target); err != nil {
			t.Fatal(err)
		}

		rotatedRef := harness.ref
		rotatedRef.KeyID = "key_rotated"
		rotatedSeed := make([]byte, ed25519.SeedSize)
		rotatedSeed[0] = 99
		rotatedPrivate := ed25519.NewKeyFromSeed(rotatedSeed)
		if err := harness.trust.Set(rotatedRef, v1.TrustedKey{PublicKey: rotatedPrivate.Public().(ed25519.PublicKey), Status: v1.TrustStatusActive}); err != nil {
			t.Fatal(err)
		}
		rotated := validControlMessage("turn.start", 1, rotatedRef)
		if _, err := validator.Validate(context.Background(), signControl(t, rotated, rotatedPrivate), harness.target); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("revocation affects next message", func(t *testing.T) {
		harness := newValidatorHarness(t)
		validator := harness.validator(t)
		first := validControlMessage("turn.start", 1, harness.ref)
		if _, err := validator.Validate(context.Background(), signControl(t, first, harness.private), harness.target); err != nil {
			t.Fatal(err)
		}
		if err := harness.trust.Revoke(harness.ref); err != nil {
			t.Fatal(err)
		}
		second := validControlMessage("turn.start", 2, harness.ref)
		_, err := validator.Validate(context.Background(), signControl(t, second, harness.private), harness.target)
		assertValidationCode(t, err, v1.ErrorForbidden)
	})

	t.Run("concurrent duplicate has one winner", func(t *testing.T) {
		harness := newValidatorHarness(t)
		validator := harness.validator(t)
		raw := signControl(t, validControlMessage("turn.start", 1, harness.ref), harness.private)
		var successes atomic.Int32
		var replays atomic.Int32
		var wait sync.WaitGroup
		for range 32 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				_, err := validator.Validate(context.Background(), raw, harness.target)
				if err == nil {
					successes.Add(1)
					return
				}
				var validationErr *v1.ValidationError
				if errors.As(err, &validationErr) && validationErr.Code == v1.ErrorReplay {
					replays.Add(1)
				}
			}()
		}
		wait.Wait()
		if successes.Load() != 1 || replays.Load() != 31 {
			t.Fatalf("successes=%d replays=%d", successes.Load(), replays.Load())
		}
	})

	t.Run("failed checks do not consume replay state", func(t *testing.T) {
		harness := newValidatorHarness(t)
		validator := harness.validator(t)
		message := validControlMessage("turn.start", 1, harness.ref)
		raw := signControl(t, message, harness.private)
		badSignature := mutateRaw(t, raw, func(document map[string]any) { document["signature"] = strings.Repeat("A", 86) })
		_, err := validator.Validate(context.Background(), badSignature, harness.target)
		assertValidationCode(t, err, v1.ErrorUnauthorized)
		if _, err := validator.Validate(context.Background(), raw, harness.target); err != nil {
			t.Fatalf("valid message rejected after invalid signature: %v", err)
		}

		second := validControlMessage("turn.start", 2, harness.ref)
		expiredSent := validatorNow.Add(-3 * time.Minute)
		expiredAt := validatorNow.Add(-time.Minute).Format(time.RFC3339Nano)
		second.SentAt = expiredSent.Format(time.RFC3339Nano)
		second.ExpiresAt = &expiredAt
		_, err = validator.Validate(context.Background(), signControl(t, second, harness.private), harness.target)
		assertValidationCode(t, err, v1.ErrorExpired)
		second.SentAt = validatorNow.Format(time.RFC3339Nano)
		validExpiry := validatorNow.Add(time.Minute).Format(time.RFC3339Nano)
		second.ExpiresAt = &validExpiry
		if _, err := validator.Validate(context.Background(), signControl(t, second, harness.private), harness.target); err != nil {
			t.Fatalf("valid message rejected after expiry failure: %v", err)
		}
	})

	t.Run("replay backend failure is atomic", func(t *testing.T) {
		harness := newValidatorHarness(t)
		toggle := &toggleReplayStore{delegate: harness.replay, fail: true}
		validator := mustValidator(t, v1.Options{TrustStore: harness.trust, ReplayStore: toggle, Now: func() time.Time { return validatorNow }})
		raw := signControl(t, validControlMessage("turn.start", 1, harness.ref), harness.private)
		_, err := validator.Validate(context.Background(), raw, harness.target)
		assertValidationCode(t, err, v1.ErrorInternal)
		toggle.fail = false
		if _, err := validator.Validate(context.Background(), raw, harness.target); err != nil {
			t.Fatalf("message consumed by failed replay backend: %v", err)
		}
	})
}

func TestValidationErrorsAreSanitized(t *testing.T) {
	harness := newValidatorHarness(t)
	canary := "private-payload-canary"
	message := validControlMessage("turn.start", 1, harness.ref)
	message.Payload["input"] = canary
	raw := signControl(t, message, harness.private)
	var signedDocument map[string]any
	if err := json.Unmarshal(raw, &signedDocument); err != nil {
		t.Fatal(err)
	}
	signature := signedDocument["signature"].(string)
	tampered := mutateRaw(t, raw, func(document map[string]any) { document["signature"] = strings.Repeat("A", 86) })
	_, err := harness.validator(t).Validate(context.Background(), tampered, harness.target)
	assertValidationCode(t, err, v1.ErrorUnauthorized)

	for _, secret := range []string{
		canary,
		string(raw),
		signature,
		base64.RawURLEncoding.EncodeToString(harness.public),
	} {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatal("validation error exposed message or cryptographic material")
		}
	}
}

type staticTrustStore struct {
	key v1.TrustedKey
	err error
}

func (s staticTrustStore) LookupControlKey(context.Context, v1.KeyRef) (v1.TrustedKey, error) {
	return s.key, s.err
}

type toggleReplayStore struct {
	mu       sync.Mutex
	delegate v1.ReplayStore
	fail     bool
}

func (s *toggleReplayStore) CheckAndRecord(ctx context.Context, record v1.ReplayRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("backend unavailable")
	}
	return s.delegate.CheckAndRecord(ctx, record)
}

func mustValidator(t *testing.T, options v1.Options) *v1.Validator {
	t.Helper()
	validator, err := v1.NewValidator(options)
	if err != nil {
		t.Fatal(err)
	}
	return validator
}

func validControlMessage(messageType string, sequence int64, ref v1.KeyRef) v1.YuanshuMessage {
	expiresAt := validatorNow.Add(time.Minute).Format(time.RFC3339Nano)
	nonce := canonicalNonce(sequence)
	return v1.YuanshuMessage{
		ProtocolVersion: v1.CurrentVersion,
		MessageID:       fmt.Sprintf("message_%d", sequence),
		Type:            messageType,
		SentAt:          validatorNow.Format(time.RFC3339Nano),
		ExpiresAt:       &expiresAt,
		OwnerID:         ref.OwnerID,
		NodeID:          ref.NodeID,
		StreamID:        "stream_test",
		Sequence:        sequence,
		CorrelationID:   fmt.Sprintf("correlation_%d", sequence),
		Nonce:           &nonce,
		Signer:          &v1.Signer{ClientID: ref.ClientID, KeyID: ref.KeyID},
		Payload:         payloadForControl(messageType),
	}
}

func payloadForControl(messageType string) map[string]any {
	switch messageType {
	case "workspace.list", "thread.list":
		return map[string]any{"limit": 20}
	case "thread.read":
		return map[string]any{"includeTurns": true}
	case "thread.start", "turn.start", "turn.steer":
		return map[string]any{"input": "synthetic input"}
	case "approval.resolve":
		return map[string]any{"approvalId": "approval_test", "decision": "decline", "operationDigest": strings.Repeat("A", 43)}
	case "lease.renew", "lease.release":
		return map[string]any{"leaseId": "lease_test", "epoch": 1}
	case "notifications.read":
		return map[string]any{"notificationId": "notification_test"}
	case "events.replay":
		return map[string]any{"afterSequence": 0}
	default:
		return map[string]any{}
	}
}

func canonicalNonce(sequence int64) string {
	value := make([]byte, 16)
	value[0] = 0x59
	binary.BigEndian.PutUint64(value[8:], uint64(sequence))
	return base64.RawURLEncoding.EncodeToString(value)
}

func signControl(t *testing.T, message v1.YuanshuMessage, privateKey ed25519.PrivateKey) []byte {
	t.Helper()
	message.Signature = nil
	input, err := v1.ControlSigningInput(message)
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, input))
	message.Signature = &signature
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mutateRaw(t *testing.T, raw []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var message map[string]any
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	mutate(message)
	changed, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

func assertValidationCode(t *testing.T, err error, expected v1.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected validation error %q", expected)
	}
	var validationErr *v1.ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error type = %T, want *ValidationError", err)
	}
	if validationErr.Code != expected {
		t.Fatalf("validation code = %q stage=%q, want %q", validationErr.Code, validationErr.Stage, expected)
	}
}
