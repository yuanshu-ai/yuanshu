// Package enrollment defines the small, protocol-independent bindings used
// while pairing and revoking Yuanshu control clients.
package enrollment

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/gowebpki/jcs"
)

const (
	PairingTTL               = 5 * time.Minute
	PairingDecisionDomain    = "yuanshu-pairing-decision-v1\x00"
	ClientRevocationDomain   = "yuanshu-client-revocation-v1\x00"
	CredentialRotationDomain = "yuanshu-node-credential-rotation-v1\x00"
)

type PairingDecision struct {
	Version   string `json:"version"`
	PairingID string `json:"pairingId"`
	OwnerID   string `json:"ownerId"`
	NodeID    string `json:"nodeId"`
	ClientID  string `json:"clientId"`
	KeyID     string `json:"keyId"`
	PublicKey string `json:"publicKey"`
	Name      string `json:"name"`
	Decision  string `json:"decision"`
	ExpiresAt string `json:"expiresAt"`
}

type ClientRevocation struct {
	Version  string `json:"version"`
	OwnerID  string `json:"ownerId"`
	NodeID   string `json:"nodeId"`
	ClientID string `json:"clientId"`
	KeyID    string `json:"keyId"`
	IssuedAt string `json:"issuedAt"`
}

type CredentialRotation struct {
	Version           string `json:"version"`
	OwnerID           string `json:"ownerId"`
	NodeID            string `json:"nodeId"`
	NewCredentialHash string `json:"newCredentialHash"`
	IssuedAt          string `json:"issuedAt"`
}

func PairingDecisionSigningInput(value PairingDecision) ([]byte, error) {
	if value.Version != "1" || !validOpaque(value.PairingID) || !validOpaque(value.OwnerID) || !validOpaque(value.NodeID) ||
		!validOpaque(value.ClientID) || !validOpaque(value.KeyID) || value.PublicKey == "" || !validName(value.Name) ||
		(value.Decision != "accept" && value.Decision != "decline") || !validTime(value.ExpiresAt) {
		return nil, errors.New("pairing decision binding is invalid")
	}
	return signingInput(PairingDecisionDomain, value)
}

func ClientRevocationSigningInput(value ClientRevocation) ([]byte, error) {
	if value.Version != "1" || !validOpaque(value.OwnerID) || !validOpaque(value.NodeID) || !validOpaque(value.ClientID) || !validOpaque(value.KeyID) || !validTime(value.IssuedAt) {
		return nil, errors.New("client revocation binding is invalid")
	}
	return signingInput(ClientRevocationDomain, value)
}

func CredentialRotationSigningInput(value CredentialRotation) ([]byte, error) {
	if value.Version != "1" || !validOpaque(value.OwnerID) || !validOpaque(value.NodeID) || value.NewCredentialHash == "" || !validTime(value.IssuedAt) {
		return nil, errors.New("credential rotation binding is invalid")
	}
	return signingInput(CredentialRotationDomain, value)
}

func signingInput(domain string, value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("enrollment binding is invalid")
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, errors.New("enrollment binding is invalid")
	}
	return append([]byte(domain), canonical...), nil
}

// Fingerprint returns a short, display-only fingerprint. It is never used as
// an authentication decision.
func Fingerprint(publicKey []byte) string {
	digest := sha256.Sum256(publicKey)
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:10])
	parts := make([]string, 0, 4)
	for len(encoded) > 0 {
		size := 4
		if len(encoded) < size {
			size = len(encoded)
		}
		parts = append(parts, encoded[:size])
		encoded = encoded[size:]
	}
	return strings.Join(parts, "-")
}

func validOpaque(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validName(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= 128 && validOpaque(value)
}

func validTime(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}
