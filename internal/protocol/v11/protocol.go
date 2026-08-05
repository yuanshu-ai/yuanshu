package v11

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/gowebpki/jcs"
	"github.com/santhosh-tekuri/jsonschema/v6"
	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

const (
	DefaultControlMaxTTL    = protocolv1.DefaultControlMaxTTL
	DefaultControlClockSkew = protocolv1.DefaultControlClockSkew
	ControlSigningDomain    = "yuanshu-control-v1.1\x00"
	InteractionDigestDomain = "yuanshu-interaction-v1.1\x00"
	protocolSchemaID        = "https://yuanshu.ai/schemas/protocol/v1.1/yuanshu-protocol.schema.json"
)

var (
	ErrInvalidControl = errors.New("protocol 1.1 control is invalid")
	ErrInvalidEvent   = errors.New("protocol 1.1 event is invalid")
	ErrReplayDetected = protocolv1.ErrReplayDetected
)

type (
	KeyRef       = protocolv1.KeyRef
	TrustedKey   = protocolv1.TrustedKey
	TrustStatus  = protocolv1.TrustStatus
	TrustStore   = protocolv1.TrustStore
	ReplayRecord = protocolv1.ReplayRecord
	ReplayStore  = protocolv1.ReplayStore
)

const (
	TrustStatusActive  = protocolv1.TrustStatusActive
	TrustStatusRevoked = protocolv1.TrustStatusRevoked
)

type Target struct{ OwnerID, NodeID string }

type Options struct {
	TrustStore  TrustStore
	ReplayStore ReplayStore
	Now         func() time.Time
	MaxTTL      time.Duration
	ClockSkew   time.Duration
}

type ValidatedControl struct {
	message YuanshuMessage
	keyRef  KeyRef
}

func (v ValidatedControl) Message() YuanshuMessage { return cloneMessage(v.message) }
func (v ValidatedControl) Signer() KeyRef          { return v.keyRef }

type Validator struct {
	trustStore TrustStore
	replay     ReplayStore
	now        func() time.Time
	maxTTL     time.Duration
	clockSkew  time.Duration
	schema     *jsonschema.Schema
}

func NewValidator(options Options) (*Validator, error) {
	if options.TrustStore == nil || options.ReplayStore == nil || options.MaxTTL < 0 || options.ClockSkew < 0 {
		return nil, errors.New("protocol 1.1 validator configuration is invalid")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.MaxTTL == 0 {
		options.MaxTTL = DefaultControlMaxTTL
	}
	if options.ClockSkew == 0 {
		options.ClockSkew = DefaultControlClockSkew
	}
	schema, err := compiledSchema()
	if err != nil {
		return nil, err
	}
	return &Validator{trustStore: options.TrustStore, replay: options.ReplayStore, now: options.Now, maxTTL: options.MaxTTL, clockSkew: options.ClockSkew, schema: schema}, nil
}

func (v *Validator) Validate(ctx context.Context, raw []byte, target Target) (ValidatedControl, error) {
	message, err := parse(raw, true, v.schema)
	if err != nil || ctx == nil || ctx.Err() != nil {
		return ValidatedControl{}, ErrInvalidControl
	}
	if target.OwnerID == "" || target.NodeID == "" || message.OwnerID != target.OwnerID || message.NodeID != target.NodeID || message.Signer == nil || message.Signature == nil || message.ExpiresAt == nil || message.Nonce == nil {
		return ValidatedControl{}, errors.New("protocol 1.1 control target is forbidden")
	}
	sentAt, sentErr := time.Parse(time.RFC3339Nano, message.SentAt)
	expiresAt, expiryErr := time.Parse(time.RFC3339Nano, *message.ExpiresAt)
	now := v.now()
	if sentErr != nil || expiryErr != nil || !expiresAt.After(sentAt) || expiresAt.Sub(sentAt) > v.maxTTL || sentAt.After(now.Add(v.clockSkew)) || expiresAt.Before(now.Add(-v.clockSkew)) {
		return ValidatedControl{}, errors.New("protocol 1.1 control is expired")
	}
	keyRef := KeyRef{OwnerID: message.OwnerID, NodeID: message.NodeID, ClientID: message.Signer.ClientID, KeyID: message.Signer.KeyID}
	trusted, lookupErr := v.trustStore.LookupControlKey(ctx, keyRef)
	if lookupErr != nil || trusted.Status != TrustStatusActive || len(trusted.PublicKey) != ed25519.PublicKeySize {
		return ValidatedControl{}, errors.New("protocol 1.1 control is unauthorized")
	}
	signature, decodeErr := decodeCanonicalBase64URL(*message.Signature, ed25519.SignatureSize)
	input, inputErr := ControlSigningInput(message)
	if decodeErr != nil || inputErr != nil || !ed25519.Verify(ed25519.PublicKey(trusted.PublicKey), input, signature) {
		return ValidatedControl{}, errors.New("protocol 1.1 control signature is invalid")
	}
	record := ReplayRecord{
		OwnerID: message.OwnerID, NodeID: message.NodeID, MessageID: message.MessageID,
		ClientID: message.Signer.ClientID, KeyID: message.Signer.KeyID, Nonce: *message.Nonce,
		Sequence: message.Sequence, NonceRetainTo: expiresAt.Add(v.clockSkew),
	}
	if err := v.replay.CheckAndRecord(ctx, record); err != nil {
		return ValidatedControl{}, err
	}
	return ValidatedControl{message: cloneMessage(message), keyRef: keyRef}, nil
}

func ParseControl(raw []byte) (YuanshuMessage, error) {
	schema, err := compiledSchema()
	if err != nil {
		return YuanshuMessage{}, ErrInvalidControl
	}
	return parse(raw, true, schema)
}

func ParseEvent(raw []byte) (YuanshuMessage, error) {
	schema, err := compiledSchema()
	if err != nil {
		return YuanshuMessage{}, ErrInvalidEvent
	}
	return parse(raw, false, schema)
}

func MarshalEvent(message YuanshuMessage) ([]byte, error) {
	if string(message.ProtocolVersion) != CurrentVersion || !IsKnownEvent(string(message.Type)) || message.Sequence < 1 || message.ExpiresAt != nil || message.Nonce != nil || message.Signer != nil || message.Signature != nil {
		return nil, ErrInvalidEvent
	}
	raw, err := json.Marshal(message)
	if err != nil || len(raw) > EventFrameMaxBytes {
		return nil, ErrInvalidEvent
	}
	if _, err = ParseEvent(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func parse(raw []byte, control bool, schema *jsonschema.Schema) (YuanshuMessage, error) {
	limit, invalid := EventFrameMaxBytes, ErrInvalidEvent
	if control {
		limit, invalid = ControlFrameMaxBytes, ErrInvalidControl
	}
	if len(raw) > limit {
		return YuanshuMessage{}, invalid
	}
	document, err := protocolv1.DecodeStrictJSON(raw)
	if err != nil || schema.Validate(document) != nil {
		return YuanshuMessage{}, invalid
	}
	object, ok := document.(map[string]any)
	if !ok || object["protocolVersion"] != CurrentVersion {
		return YuanshuMessage{}, invalid
	}
	typeValue, _ := object["type"].(string)
	if control && !IsKnownControl(typeValue) || !control && !IsKnownEvent(typeValue) {
		return YuanshuMessage{}, invalid
	}
	var message YuanshuMessage
	if json.Unmarshal(raw, &message) != nil {
		return YuanshuMessage{}, invalid
	}
	if control {
		if message.Nonce == nil || decodeNonce(*message.Nonce) != nil {
			return YuanshuMessage{}, invalid
		}
	}
	return cloneMessage(message), nil
}

func ControlSigningInput(message YuanshuMessage) ([]byte, error) {
	if string(message.ProtocolVersion) != CurrentVersion || !IsKnownControl(string(message.Type)) || message.MessageID == "" || message.OwnerID == "" || message.NodeID == "" || message.StreamID == "" || message.CorrelationID == "" || message.ExpiresAt == nil || message.Nonce == nil || message.Signer == nil || message.Payload == nil || message.Sequence < 0 {
		return nil, ErrInvalidControl
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return nil, ErrInvalidControl
	}
	if _, err := protocolv1.DecodeStrictJSON(encoded); err != nil {
		return nil, ErrInvalidControl
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(encoded, &members) != nil {
		return nil, ErrInvalidControl
	}
	delete(members, "signature")
	unsigned, err := json.Marshal(members)
	if err != nil {
		return nil, ErrInvalidControl
	}
	canonical, err := jcs.Transform(unsigned)
	if err != nil {
		return nil, ErrInvalidControl
	}
	return append([]byte(ControlSigningDomain), canonical...), nil
}

func InteractionOperationDigest(message YuanshuMessage) (string, error) {
	if string(message.ProtocolVersion) != CurrentVersion || string(message.Type) != string(EventInteractionRequested) || message.AgentInstanceID == nil || message.WorkspaceID == nil || message.TaskID == nil || message.RunID == nil || message.InteractionID == nil {
		return "", ErrInvalidEvent
	}
	payload := cloneMap(message.Payload)
	delete(payload, "operationDigest")
	binding := map[string]any{
		"protocolVersion": CurrentVersion, "type": string(message.Type), "ownerId": message.OwnerID, "nodeId": message.NodeID,
		"agentInstanceId": *message.AgentInstanceID, "workspaceId": *message.WorkspaceID, "taskId": *message.TaskID,
		"runId": *message.RunID, "interactionId": *message.InteractionID, "payload": payload,
	}
	if message.ActivityID != nil {
		binding["activityId"] = *message.ActivityID
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		return "", ErrInvalidEvent
	}
	canonical, err := jcs.Transform(encoded)
	if err != nil {
		return "", ErrInvalidEvent
	}
	digest := sha256.Sum256(append([]byte(InteractionDigestDomain), canonical...))
	return base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

func IsKnownControl(value string) bool {
	for _, item := range KnownControlTypes {
		if string(item) == value {
			return true
		}
	}
	return false
}

func IsKnownEvent(value string) bool {
	for _, item := range KnownEventTypes {
		if string(item) == value {
			return true
		}
	}
	return false
}

func decodeNonce(value string) error {
	_, err := decodeCanonicalBase64URL(value, 16)
	return err
}

func decodeCanonicalBase64URL(value string, size int) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != size || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("invalid base64url")
	}
	return decoded, nil
}

func cloneMessage(message YuanshuMessage) YuanshuMessage {
	raw, _ := json.Marshal(message)
	var clone YuanshuMessage
	_ = json.Unmarshal(raw, &clone)
	return clone
}

func cloneMap(value map[string]any) map[string]any {
	raw, _ := json.Marshal(value)
	var clone map[string]any
	_ = json.Unmarshal(raw, &clone)
	return clone
}

var (
	schemaOnce  sync.Once
	schemaValue *jsonschema.Schema
	schemaErr   error
)

func compiledSchema() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		document, err := jsonschema.UnmarshalJSON(strings.NewReader(protocolSchemaJSON))
		if err != nil {
			schemaErr = err
			return
		}
		compiler := jsonschema.NewCompiler()
		compiler.AssertFormat()
		compiler.UseRegexpEngine(func(expression string) (jsonschema.Regexp, error) {
			compiled, compileErr := regexp2.Compile(expression, regexp2.ECMAScript)
			return (*ecmaScriptRegexp)(compiled), compileErr
		})
		if err = compiler.AddResource(protocolSchemaID, document); err == nil {
			schemaValue, err = compiler.Compile(protocolSchemaID)
		}
		if err != nil {
			schemaErr = fmt.Errorf("compile protocol 1.1 schema: %w", err)
		}
	})
	return schemaValue, schemaErr
}

type ecmaScriptRegexp regexp2.Regexp

func (re *ecmaScriptRegexp) MatchString(value string) bool {
	matched, err := (*regexp2.Regexp)(re).MatchString(value)
	return err == nil && matched
}
func (re *ecmaScriptRegexp) String() string { return (*regexp2.Regexp)(re).String() }
