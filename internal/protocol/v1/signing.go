package v1

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"unicode/utf8"

	"github.com/gowebpki/jcs"
)

const (
	ControlSigningDomain  = "yuanshu-control-v1\x00"
	OperationDigestDomain = "yuanshu-operation-v1\x00"
)

// ControlSigningInput returns the domain-separated RFC 8785 representation of
// a Protocol v1 control message with the signature member removed. It does not
// validate trust, freshness, replay state, or the signature itself.
func ControlSigningInput(message YuanshuMessage) ([]byte, error) {
	if err := validateControlForEncoding(message); err != nil {
		return nil, err
	}
	if err := validateIJSON(message); err != nil {
		return nil, fmt.Errorf("control message is not I-JSON: %w", err)
	}

	encoded, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("marshal control message: %w", err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &members); err != nil {
		return nil, fmt.Errorf("split control message: %w", err)
	}
	delete(members, "signature")
	unsigned, err := json.Marshal(members)
	if err != nil {
		return nil, fmt.Errorf("marshal unsigned control message: %w", err)
	}
	canonical, err := jcs.Transform(unsigned)
	if err != nil {
		return nil, fmt.Errorf("canonicalize control message: %w", err)
	}
	return append([]byte(ControlSigningDomain), canonical...), nil
}

// ApprovalOperationDigest binds the stable target identity and complete
// approval payload while excluding replay-specific event metadata. An existing
// operationDigest member is removed before hashing.
func ApprovalOperationDigest(message YuanshuMessage) (string, error) {
	if err := validateApprovalForDigest(message); err != nil {
		return "", err
	}
	if err := validateIJSON(message); err != nil {
		return "", fmt.Errorf("approval message is not I-JSON: %w", err)
	}

	payloadJSON, err := json.Marshal(message.Payload)
	if err != nil {
		return "", fmt.Errorf("marshal approval payload: %w", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return "", fmt.Errorf("split approval payload: %w", err)
	}
	delete(payload, "operationDigest")
	payloadJSON, err = json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal digest payload: %w", err)
	}

	binding := approvalDigestBinding{
		ProtocolVersion: message.ProtocolVersion,
		Type:            message.Type,
		OwnerID:         message.OwnerID,
		NodeID:          message.NodeID,
		WorkspaceID:     message.WorkspaceID,
		ThreadID:        message.ThreadID,
		TurnID:          message.TurnID,
		ItemID:          message.ItemID,
		Payload:         payloadJSON,
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		return "", fmt.Errorf("marshal approval binding: %w", err)
	}
	canonical, err := jcs.Transform(encoded)
	if err != nil {
		return "", fmt.Errorf("canonicalize approval binding: %w", err)
	}
	hashInput := append([]byte(OperationDigestDomain), canonical...)
	digest := sha256.Sum256(hashInput)
	return base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

type approvalDigestBinding struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Type            string          `json:"type"`
	OwnerID         string          `json:"ownerId"`
	NodeID          string          `json:"nodeId"`
	WorkspaceID     *string         `json:"workspaceId,omitempty"`
	ThreadID        *string         `json:"threadId,omitempty"`
	TurnID          *string         `json:"turnId,omitempty"`
	ItemID          *string         `json:"itemId,omitempty"`
	Payload         json.RawMessage `json:"payload"`
}

func validateControlForEncoding(message YuanshuMessage) error {
	if message.ProtocolVersion != CurrentVersion {
		return fmt.Errorf("unsupported control protocol version %q", message.ProtocolVersion)
	}
	if !IsKnownControl(message.Type) {
		return fmt.Errorf("unsupported control type %q", message.Type)
	}
	if message.MessageID == "" || message.SentAt == "" || message.OwnerID == "" || message.NodeID == "" || message.StreamID == "" || message.CorrelationID == "" {
		return errors.New("control message is missing a required envelope identifier")
	}
	if message.ExpiresAt == nil || *message.ExpiresAt == "" || message.Nonce == nil || *message.Nonce == "" || message.Signer == nil || message.Signer.ClientID == "" || message.Signer.KeyID == "" || message.Payload == nil {
		return errors.New("control message is missing required signing fields")
	}
	if message.Sequence < 0 || message.Sequence > 9007199254740991 {
		return errors.New("control sequence is outside the JavaScript safe-integer range")
	}
	return nil
}

func validateApprovalForDigest(message YuanshuMessage) error {
	if message.ProtocolVersion != CurrentVersion || message.Type != string(EventApprovalRequested) {
		return errors.New("operation digest requires a Protocol v1 approval.requested event")
	}
	if message.OwnerID == "" || message.NodeID == "" || message.Payload == nil {
		return errors.New("approval event is missing a required binding field")
	}
	approvalID, approvalOK := message.Payload["approvalId"].(string)
	kind, kindOK := message.Payload["kind"].(string)
	if !approvalOK || approvalID == "" || !kindOK || kind == "" {
		return errors.New("approval payload requires non-empty approvalId and kind")
	}
	return nil
}

func validateIJSON(value any) error {
	return validateJSONValue(reflect.ValueOf(value), make(map[visit]bool))
}

type visit struct {
	typeName reflect.Type
	pointer  uintptr
}

func validateJSONValue(value reflect.Value, seen map[visit]bool) error {
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		if value.Kind() == reflect.Pointer {
			key := visit{value.Type(), value.Pointer()}
			if seen[key] {
				return errors.New("cyclic value")
			}
			seen[key] = true
			defer delete(seen, key)
		}
		return validateJSONValue(value.Elem(), seen)
	}

	switch value.Kind() {
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return errors.New("invalid UTF-8 string")
		}
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return errors.New("non-finite number")
		}
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return errors.New("JSON object key is not a string")
		}
		if value.IsNil() {
			return nil
		}
		key := visit{value.Type(), value.Pointer()}
		if seen[key] {
			return errors.New("cyclic value")
		}
		seen[key] = true
		defer delete(seen, key)
		iterator := value.MapRange()
		for iterator.Next() {
			if !utf8.ValidString(iterator.Key().String()) {
				return errors.New("invalid UTF-8 object key")
			}
			if err := validateJSONValue(iterator.Value(), seen); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if value.IsNil() {
			return nil
		}
		key := visit{value.Type(), value.Pointer()}
		if seen[key] {
			return errors.New("cyclic value")
		}
		seen[key] = true
		defer delete(seen, key)
		for index := 0; index < value.Len(); index++ {
			if err := validateJSONValue(value.Index(index), seen); err != nil {
				return err
			}
		}
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateJSONValue(value.Index(index), seen); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if value.Type().Field(index).PkgPath != "" {
				continue
			}
			if err := validateJSONValue(value.Field(index), seen); err != nil {
				return err
			}
		}
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return nil
	default:
		return fmt.Errorf("unsupported JSON value kind %s", value.Kind())
	}
	return nil
}
