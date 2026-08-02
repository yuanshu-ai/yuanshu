package v1

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	DefaultControlMaxTTL    = 2 * time.Minute
	DefaultControlClockSkew = 30 * time.Second
	protocolSchemaID        = "https://yuanshu.ai/schemas/protocol/v1/yuanshu-protocol.schema.json"
)

// ValidationStage identifies the sanitized stage at which validation stopped.
// It is safe to use in logs because it contains no wire or cryptographic data.
type ValidationStage string

const (
	ValidationStageFrame     ValidationStage = "frame"
	ValidationStageSyntax    ValidationStage = "syntax"
	ValidationStageHeader    ValidationStage = "header"
	ValidationStageSchema    ValidationStage = "schema"
	ValidationStageTarget    ValidationStage = "target"
	ValidationStageTime      ValidationStage = "time"
	ValidationStageTrust     ValidationStage = "trust"
	ValidationStageSignature ValidationStage = "signature"
	ValidationStageReplay    ValidationStage = "replay"
)

// ValidationError deliberately excludes the raw frame and all message values.
type ValidationError struct {
	Code  ErrorCode
	Stage ValidationStage
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("control validation failed: code=%s stage=%s", e.Code, e.Stage)
}

func validationError(code ErrorCode, stage ValidationStage) error {
	return &ValidationError{Code: code, Stage: stage}
}

// Target is the local routing decision. It must not be populated from the
// untrusted relay frame being validated.
type Target struct {
	OwnerID string
	NodeID  string
}

// Options supplies security state and an injectable clock. Stores are required.
type Options struct {
	TrustStore  TrustStore
	ReplayStore ReplayStore
	Now         func() time.Time
	MaxTTL      time.Duration
	ClockSkew   time.Duration
}

// ValidatedControl is the only successful output of Validator. Its contents
// cannot be constructed outside this package except as an unusable zero value.
type ValidatedControl struct {
	message YuanshuMessage
	keyRef  KeyRef
}

// Message returns a detached copy of the verified control message.
func (v ValidatedControl) Message() YuanshuMessage {
	return cloneMessage(v.message)
}

// Signer returns the trust-store key reference that verified the message.
func (v ValidatedControl) Signer() KeyRef {
	return v.keyRef
}

type Validator struct {
	trustStore  TrustStore
	replayStore ReplayStore
	now         func() time.Time
	maxTTL      time.Duration
	clockSkew   time.Duration
	schema      *jsonschema.Schema
}

var (
	compiledProtocolSchema     *jsonschema.Schema
	compiledProtocolSchemaErr  error
	compiledProtocolSchemaOnce sync.Once
)

func NewValidator(options Options) (*Validator, error) {
	if options.TrustStore == nil {
		return nil, errors.New("protocol validator requires a trust store")
	}
	if options.ReplayStore == nil {
		return nil, errors.New("protocol validator requires a replay store")
	}
	if options.MaxTTL < 0 || options.ClockSkew < 0 {
		return nil, errors.New("protocol validator durations cannot be negative")
	}
	if options.MaxTTL == 0 {
		options.MaxTTL = DefaultControlMaxTTL
	}
	if options.ClockSkew == 0 {
		options.ClockSkew = DefaultControlClockSkew
	}
	if options.Now == nil {
		options.Now = time.Now
	}

	schema, err := protocolSchema()
	if err != nil {
		return nil, err
	}

	return &Validator{
		trustStore:  options.TrustStore,
		replayStore: options.ReplayStore,
		now:         options.Now,
		maxTTL:      options.MaxTTL,
		clockSkew:   options.ClockSkew,
		schema:      schema,
	}, nil
}

func protocolSchema() (*jsonschema.Schema, error) {
	compiledProtocolSchemaOnce.Do(func() {
		document, err := jsonschema.UnmarshalJSON(strings.NewReader(protocolSchemaJSON))
		if err != nil {
			compiledProtocolSchemaErr = fmt.Errorf("decode embedded protocol schema: %w", err)
			return
		}
		compiler := jsonschema.NewCompiler()
		compiler.AssertFormat()
		compiler.UseRegexpEngine(compileECMAScriptRegexp)
		if err := compiler.AddResource(protocolSchemaID, document); err != nil {
			compiledProtocolSchemaErr = fmt.Errorf("register embedded protocol schema: %w", err)
			return
		}
		compiledProtocolSchema, err = compiler.Compile(protocolSchemaID)
		if err != nil {
			compiledProtocolSchemaErr = fmt.Errorf("compile embedded protocol schema: %w", err)
		}
	})
	if compiledProtocolSchemaErr != nil {
		return nil, compiledProtocolSchemaErr
	}
	return compiledProtocolSchema, nil
}

type ecmaScriptRegexp regexp2.Regexp

func (re *ecmaScriptRegexp) MatchString(value string) bool {
	matched, err := (*regexp2.Regexp)(re).MatchString(value)
	return err == nil && matched
}

func (re *ecmaScriptRegexp) String() string {
	return (*regexp2.Regexp)(re).String()
}

func compileECMAScriptRegexp(expression string) (jsonschema.Regexp, error) {
	compiled, err := regexp2.Compile(expression, regexp2.ECMAScript)
	if err != nil {
		return nil, err
	}
	return (*ecmaScriptRegexp)(compiled), nil
}

func (v *Validator) Validate(ctx context.Context, raw []byte, target Target) (ValidatedControl, error) {
	if len(raw) > ControlFrameMaxBytes {
		return ValidatedControl{}, validationError(ErrorPayloadTooLarge, ValidationStageFrame)
	}
	if ctx == nil || ctx.Err() != nil {
		return ValidatedControl{}, validationError(ErrorInternal, ValidationStageFrame)
	}

	document, err := decodeStrictJSON(raw)
	if err != nil {
		return ValidatedControl{}, validationError(ErrorInvalidMessage, ValidationStageSyntax)
	}
	object, ok := document.(map[string]any)
	if !ok {
		return ValidatedControl{}, validationError(ErrorInvalidMessage, ValidationStageHeader)
	}
	version, versionOK := object["protocolVersion"].(string)
	messageType, typeOK := object["type"].(string)
	if !versionOK || version == "" || !typeOK || messageType == "" {
		return ValidatedControl{}, validationError(ErrorInvalidMessage, ValidationStageHeader)
	}
	if version != CurrentVersion {
		return ValidatedControl{}, validationError(ErrorUnsupportedProtocol, ValidationStageHeader)
	}
	if !IsKnownControl(messageType) {
		return ValidatedControl{}, validationError(ErrorUnsupportedControl, ValidationStageHeader)
	}
	if err := v.schema.Validate(document); err != nil {
		return ValidatedControl{}, validationError(ErrorInvalidMessage, ValidationStageSchema)
	}

	var message YuanshuMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return ValidatedControl{}, validationError(ErrorInvalidMessage, ValidationStageSchema)
	}
	if err := validateCanonicalNonce(*message.Nonce); err != nil {
		return ValidatedControl{}, validationError(ErrorInvalidMessage, ValidationStageSchema)
	}
	if target.OwnerID == "" || target.NodeID == "" || message.OwnerID != target.OwnerID || message.NodeID != target.NodeID {
		return ValidatedControl{}, validationError(ErrorForbidden, ValidationStageTarget)
	}

	sentAt, err := time.Parse(time.RFC3339Nano, message.SentAt)
	if err != nil {
		return ValidatedControl{}, validationError(ErrorInvalidMessage, ValidationStageTime)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, *message.ExpiresAt)
	if err != nil {
		return ValidatedControl{}, validationError(ErrorInvalidMessage, ValidationStageTime)
	}
	now := v.now()
	if !expiresAt.After(sentAt) || expiresAt.Sub(sentAt) > v.maxTTL || sentAt.After(now.Add(v.clockSkew)) || expiresAt.Before(now.Add(-v.clockSkew)) {
		return ValidatedControl{}, validationError(ErrorExpired, ValidationStageTime)
	}

	keyRef := KeyRef{
		OwnerID:  message.OwnerID,
		NodeID:   message.NodeID,
		ClientID: message.Signer.ClientID,
		KeyID:    message.Signer.KeyID,
	}
	trustedKey, err := v.trustStore.LookupControlKey(ctx, keyRef)
	if err != nil {
		if errors.Is(err, ErrTrustKeyNotFound) {
			return ValidatedControl{}, validationError(ErrorUnauthorized, ValidationStageTrust)
		}
		return ValidatedControl{}, validationError(ErrorInternal, ValidationStageTrust)
	}
	if trustedKey.Status == TrustStatusRevoked {
		return ValidatedControl{}, validationError(ErrorForbidden, ValidationStageTrust)
	}
	if trustedKey.Status != TrustStatusActive || len(trustedKey.PublicKey) != ed25519.PublicKeySize {
		return ValidatedControl{}, validationError(ErrorUnauthorized, ValidationStageTrust)
	}

	signature, err := decodeCanonicalBase64URL(*message.Signature, ed25519.SignatureSize)
	if err != nil {
		return ValidatedControl{}, validationError(ErrorUnauthorized, ValidationStageSignature)
	}
	signingInput, err := ControlSigningInput(message)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(trustedKey.PublicKey), signingInput, signature) {
		return ValidatedControl{}, validationError(ErrorUnauthorized, ValidationStageSignature)
	}

	record := ReplayRecord{
		OwnerID:       message.OwnerID,
		NodeID:        message.NodeID,
		MessageID:     message.MessageID,
		ClientID:      message.Signer.ClientID,
		KeyID:         message.Signer.KeyID,
		Nonce:         *message.Nonce,
		Sequence:      message.Sequence,
		NonceRetainTo: expiresAt.Add(v.clockSkew),
	}
	if err := v.replayStore.CheckAndRecord(ctx, record); err != nil {
		if errors.Is(err, ErrReplayDetected) {
			return ValidatedControl{}, validationError(ErrorReplay, ValidationStageReplay)
		}
		return ValidatedControl{}, validationError(ErrorInternal, ValidationStageReplay)
	}

	return ValidatedControl{message: cloneMessage(message), keyRef: keyRef}, nil
}

func validateCanonicalNonce(nonce string) error {
	_, err := decodeCanonicalBase64URL(nonce, 16)
	return err
}

func decodeCanonicalBase64URL(value string, size int) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != size || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("invalid canonical base64url value")
	}
	return decoded, nil
}

func cloneMessage(message YuanshuMessage) YuanshuMessage {
	clone := message
	clone.ExpiresAt = cloneStringPointer(message.ExpiresAt)
	clone.ItemID = cloneStringPointer(message.ItemID)
	clone.Nonce = cloneStringPointer(message.Nonce)
	clone.Signature = cloneStringPointer(message.Signature)
	clone.ThreadID = cloneStringPointer(message.ThreadID)
	clone.TurnID = cloneStringPointer(message.TurnID)
	clone.WorkspaceID = cloneStringPointer(message.WorkspaceID)
	if message.Signer != nil {
		signer := *message.Signer
		clone.Signer = &signer
	}
	clone.Payload = cloneJSONObject(message.Payload)
	return clone
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneJSONObject(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	clone := make(map[string]any, len(value))
	for key, item := range value {
		clone[key] = cloneJSONValue(item)
	}
	return clone
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneJSONObject(typed)
	case []any:
		clone := make([]any, len(typed))
		for index, item := range typed {
			clone[index] = cloneJSONValue(item)
		}
		return clone
	default:
		return typed
	}
}
