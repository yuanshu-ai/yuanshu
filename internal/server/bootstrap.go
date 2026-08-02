package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"

	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
)

const (
	bootstrapSecretBytes = 32
	identifierBytes      = 16
	defaultRetryWindow   = 5 * time.Minute
)

var (
	ErrInvalid      = errors.New("server request is invalid")
	ErrUnauthorized = errors.New("server request is unauthorized")
	ErrConflict     = errors.New("server state conflicts with the request")
)

type ClaimRequest struct {
	RequestID      string `json:"requestId"`
	Name           string `json:"name"`
	OS             string `json:"os"`
	Version        string `json:"version"`
	PublicKey      string `json:"publicKey"`
	CredentialHash string `json:"credentialHash"`
}

type ClaimResponse struct {
	OwnerID string `json:"ownerId"`
	NodeID  string `json:"nodeId"`
	Status  string `json:"status"`
}

type bootstrapStore interface {
	RotateBootstrap(context.Context, []byte, time.Time) (serverstore.BootstrapStatus, error)
	BootstrapStatus(context.Context) (serverstore.BootstrapStatus, error)
	ClaimBootstrap(context.Context, serverstore.BootstrapClaim) (serverstore.ClaimResult, error)
}

type BootstrapOptions struct {
	Random      io.Reader
	Clock       func() time.Time
	RetryWindow time.Duration
}

type BootstrapService struct {
	store       bootstrapStore
	random      io.Reader
	clock       func() time.Time
	retryWindow time.Duration
}

func NewBootstrapService(store bootstrapStore, options BootstrapOptions) (*BootstrapService, error) {
	if store == nil {
		return nil, ErrInvalid
	}
	random := options.Random
	if random == nil {
		random = rand.Reader
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	retryWindow := options.RetryWindow
	if retryWindow == 0 {
		retryWindow = defaultRetryWindow
	}
	if retryWindow < 0 {
		return nil, ErrInvalid
	}
	return &BootstrapService{store: store, random: random, clock: clock, retryWindow: retryWindow}, nil
}

// Rotate issues a new secret for an uninitialized Server. The raw value is
// returned to the composition root and is never passed to the Store.
func (s *BootstrapService) Rotate(ctx context.Context) (string, bool, error) {
	if ctx == nil {
		return "", false, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	value := make([]byte, bootstrapSecretBytes)
	if _, err := io.ReadFull(s.random, value); err != nil {
		return "", false, errors.New("server bootstrap generation failed")
	}
	digest := sha256.Sum256(value)
	status, err := s.store.RotateBootstrap(ctx, digest[:], s.clock().UTC())
	if err != nil {
		return "", false, err
	}
	if status.State == serverstore.BootstrapCompleted {
		return "", false, nil
	}
	return base64.RawURLEncoding.EncodeToString(value), true, nil
}

func (s *BootstrapService) Status(ctx context.Context) (serverstore.BootstrapStatus, error) {
	if ctx == nil {
		return serverstore.BootstrapStatus{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return serverstore.BootstrapStatus{}, err
	}
	return s.store.BootstrapStatus(ctx)
}

func (s *BootstrapService) Claim(ctx context.Context, secret string, request ClaimRequest) (ClaimResponse, bool, error) {
	if ctx == nil {
		return ClaimResponse{}, false, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return ClaimResponse{}, false, err
	}
	secretValue, err := decodeCanonical(secret, bootstrapSecretBytes)
	if err != nil {
		return ClaimResponse{}, false, ErrUnauthorized
	}
	publicKey, err := decodeCanonical(request.PublicKey, 32)
	if err != nil {
		return ClaimResponse{}, false, ErrInvalid
	}
	credentialHash, err := decodeCanonical(request.CredentialHash, 32)
	if err != nil || !validOpaque(request.RequestID, 128) || !validDisplay(request.Name, 128) || !validVersion(request.Version) || !validOS(request.OS) {
		return ClaimResponse{}, false, ErrInvalid
	}
	canonical, err := json.Marshal(request)
	if err != nil {
		return ClaimResponse{}, false, ErrInvalid
	}
	claimDigest := sha256.Sum256(append([]byte("yuanshu-bootstrap-claim-v1\x00"), canonical...))
	secretHash := sha256.Sum256(secretValue)
	ownerID, err := s.randomID("own_")
	if err != nil {
		return ClaimResponse{}, false, err
	}
	nodeID, err := s.randomID("nod_")
	if err != nil {
		return ClaimResponse{}, false, err
	}
	now := s.clock().UTC()
	result, err := s.store.ClaimBootstrap(ctx, serverstore.BootstrapClaim{
		SecretHash: secretHash[:], ClaimDigest: claimDigest[:], OwnerID: ownerID, NodeID: nodeID,
		RequestID: request.RequestID, Name: request.Name, OS: request.OS, Version: request.Version,
		PublicKey: publicKey, CredentialHash: credentialHash, Now: now, RetryUntil: now.Add(s.retryWindow),
	})
	if err != nil {
		switch {
		case errors.Is(err, serverstore.ErrUnauthorized):
			return ClaimResponse{}, false, ErrUnauthorized
		case errors.Is(err, serverstore.ErrBootstrapCompleted), errors.Is(err, serverstore.ErrConflict):
			return ClaimResponse{}, false, ErrConflict
		default:
			return ClaimResponse{}, false, err
		}
	}
	return ClaimResponse{OwnerID: result.OwnerID, NodeID: result.NodeID, Status: "enrolled"}, result.Replayed, nil
}

func (s *BootstrapService) randomID(prefix string) (string, error) {
	value := make([]byte, identifierBytes)
	if _, err := io.ReadFull(s.random, value); err != nil {
		return "", errors.New("server identifier generation failed")
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}

func decodeCanonical(value string, size int) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != size || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, ErrInvalid
	}
	return decoded, nil
}

func validOpaque(value string, maximum int) bool {
	if len(value) < 1 || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validDisplay(value string, maximum int) bool {
	return value == strings.TrimSpace(value) && validOpaque(value, maximum)
}

func validVersion(value string) bool { return validDisplay(value, 64) }

func validOS(value string) bool { return value == "windows" || value == "linux" || value == "darwin" }
