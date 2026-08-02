package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/enrollment"
	"github.com/yuanshu-ai/yuanshu/internal/node/identity"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
	"github.com/yuanshu-ai/yuanshu/internal/transport"
)

type PairingCandidate struct {
	PairingID, ClientID, KeyID, Name, PublicKey, Fingerprint, ExpiresAt string
}

type TrustedClientSummary struct {
	ClientID, KeyID, Fingerprint, Status string
}

type pairingManager struct {
	baseURL, relayURL string
	httpClient        *http.Client
	identity          identity.Identity
	signer            *identity.Manager
	local             *store.Store
	secrets           platform.SecureStore
	credentialRef     platform.SecretRef
	credential        []byte
	random            io.Reader
	clock             func() time.Time
}

type pairingManagerOptions struct {
	RelayURL      string
	Timeout       time.Duration
	HTTPClient    *http.Client
	Identity      identity.Identity
	Signer        *identity.Manager
	Local         *store.Store
	Secrets       platform.SecureStore
	CredentialRef platform.SecretRef
	Credential    []byte
	Random        io.Reader
	Clock         func() time.Time
}

func newPairingManager(options pairingManagerOptions) (*pairingManager, error) {
	parsed, err := url.Parse(options.RelayURL)
	if err != nil || parsed.Scheme != "wss" || parsed.Host == "" || options.Signer == nil || options.Local == nil || options.Secrets == nil || options.Identity.OwnerID == "" || options.Identity.NodeID == "" || len(options.Identity.PublicKey) != ed25519.PublicKeySize || len(options.Credential) < 32 || options.CredentialRef == "" {
		return nil, errors.New("node pairing configuration is unavailable")
	}
	base := *parsed
	base.Scheme = "https"
	base.Path = ""
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	client := options.HTTPClient
	if client == nil {
		timeout := options.Timeout
		if timeout <= 0 {
			timeout = 15 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	random := options.Random
	if random == nil {
		random = rand.Reader
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	return &pairingManager{baseURL: strings.TrimRight(base.String(), "/"), relayURL: options.RelayURL, httpClient: client, identity: options.Identity, signer: options.Signer, local: options.Local, secrets: options.Secrets, credentialRef: options.CredentialRef, credential: append([]byte(nil), options.Credential...), random: random, clock: clock}, nil
}

func (m *pairingManager) Connect(ctx context.Context) (transport.Transport, error) {
	header := make(http.Header)
	header.Set("X-Yuanshu-Node-ID", m.identity.NodeID)
	header.Set("Authorization", "Bearer "+string(m.credential))
	connection, _, err := transport.DialRelay(ctx, m.relayURL, transport.RelayDialOptions{HTTPClient: m.httpClient, Header: header, Role: transport.SessionRoleNode, SubjectID: m.identity.NodeID, Sign: m.signer.Sign, Relay: transport.RelayOptions{MaxSendBytes: protocolv1.EventFrameMaxBytes, MaxReceiveBytes: protocolv1.ControlFrameMaxBytes}})
	if err != nil {
		return nil, errors.New("node relay connection failed")
	}
	return connection, nil
}

func (m *pairingManager) Create(ctx context.Context) (string, error) {
	secret := make([]byte, 32)
	challenge := make([]byte, 32)
	if _, err := io.ReadFull(m.random, secret); err != nil {
		return "", errors.New("pairing generation failed")
	}
	defer clear(secret)
	if _, err := io.ReadFull(m.random, challenge); err != nil {
		return "", errors.New("pairing generation failed")
	}
	hash := sha256.Sum256(secret)
	var response struct{ PairingID, ExpiresAt string }
	err := m.nodeJSON(ctx, http.MethodPost, "/v1/control-client-pairings", map[string]string{"codeHash": base64.RawURLEncoding.EncodeToString(hash[:]), "challenge": base64.RawURLEncoding.EncodeToString(challenge)}, &response)
	clear(challenge)
	if err != nil {
		return "", err
	}
	if response.PairingID == "" {
		return "", errors.New("pairing service response is invalid")
	}
	return m.baseURL + "/pair#" + response.PairingID + "." + base64.RawURLEncoding.EncodeToString(secret), nil
}

func (m *pairingManager) Pending(ctx context.Context) ([]PairingCandidate, error) {
	var response struct {
		Pairings []PairingCandidate `json:"pairings"`
	}
	if err := m.nodeJSON(ctx, http.MethodGet, "/v1/control-client-pairings", nil, &response); err != nil {
		return nil, err
	}
	return append([]PairingCandidate(nil), response.Pairings...), nil
}

func (m *pairingManager) Decide(ctx context.Context, pairingID, decision string) error {
	if decision != "accept" && decision != "decline" {
		return errors.New("pairing decision is invalid")
	}
	items, err := m.Pending(ctx)
	if err != nil {
		return err
	}
	var item PairingCandidate
	for _, candidate := range items {
		if candidate.PairingID == pairingID {
			item = candidate
			break
		}
	}
	if item.PairingID == "" {
		return errors.New("pairing request was not found")
	}
	binding := enrollment.PairingDecision{Version: "1", PairingID: item.PairingID, OwnerID: m.identity.OwnerID, NodeID: m.identity.NodeID, ClientID: item.ClientID, KeyID: item.KeyID, PublicKey: item.PublicKey, Name: item.Name, Decision: decision, ExpiresAt: item.ExpiresAt}
	input, err := enrollment.PairingDecisionSigningInput(binding)
	if err != nil {
		return errors.New("pairing request is invalid")
	}
	signature, err := m.signer.Sign(ctx, input)
	if err != nil {
		return errors.New("pairing decision signing failed")
	}
	defer clear(signature)
	publicKey, err := base64.RawURLEncoding.DecodeString(item.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("pairing request is invalid")
	}
	ref := protocolv1.KeyRef{OwnerID: m.identity.OwnerID, NodeID: m.identity.NodeID, ClientID: item.ClientID, KeyID: item.KeyID}
	trusted := false
	if decision == "accept" {
		if err := m.local.PutTrustedKey(ctx, ref, protocolv1.TrustedKey{PublicKey: publicKey, Status: protocolv1.TrustStatusActive}); err != nil {
			return errors.New("control client trust could not be saved")
		}
		trusted = true
	}
	err = m.nodeJSON(ctx, http.MethodPost, "/v1/control-client-pairings/"+url.PathEscape(pairingID)+"/decision", map[string]string{"decision": decision, "signature": base64.RawURLEncoding.EncodeToString(signature)}, nil)
	if err != nil && trusted {
		_ = m.local.RevokeTrustedKey(context.Background(), ref)
	}
	return err
}

func (m *pairingManager) Clients(ctx context.Context) ([]TrustedClientSummary, error) {
	items, err := m.local.TrustedClients(ctx, m.identity.OwnerID, m.identity.NodeID)
	if err != nil {
		return nil, errors.New("trusted clients are unavailable")
	}
	result := make([]TrustedClientSummary, 0, len(items))
	for _, item := range items {
		result = append(result, TrustedClientSummary{ClientID: item.ClientID, KeyID: item.KeyID, Fingerprint: enrollment.Fingerprint(item.PublicKey), Status: string(item.Status)})
	}
	return result, nil
}

func (m *pairingManager) Revoke(ctx context.Context, clientID, keyID string) error {
	ref := protocolv1.KeyRef{OwnerID: m.identity.OwnerID, NodeID: m.identity.NodeID, ClientID: clientID, KeyID: keyID}
	if err := m.local.RevokeTrustedKey(ctx, ref); err != nil {
		return errors.New("control client was not found")
	}
	issued := m.clock().UTC().Format(time.RFC3339Nano)
	binding := enrollment.ClientRevocation{Version: "1", OwnerID: m.identity.OwnerID, NodeID: m.identity.NodeID, ClientID: clientID, KeyID: keyID, IssuedAt: issued}
	input, err := enrollment.ClientRevocationSigningInput(binding)
	if err != nil {
		return errors.New("control client revocation failed")
	}
	signature, err := m.signer.Sign(ctx, input)
	if err != nil {
		return errors.New("control client revocation failed")
	}
	defer clear(signature)
	return m.nodeJSON(ctx, http.MethodDelete, "/v1/control-clients/"+url.PathEscape(clientID), map[string]string{"nodeId": m.identity.NodeID, "keyId": keyID, "issuedAt": issued, "signature": base64.RawURLEncoding.EncodeToString(signature)}, nil)
}

func (m *pairingManager) RotateCredential(ctx context.Context) error {
	newCredential := make([]byte, 32)
	if _, err := io.ReadFull(m.random, newCredential); err != nil {
		return errors.New("credential rotation failed")
	}
	defer clear(newCredential)
	hash := sha256.Sum256(newCredential)
	issued := m.clock().UTC().Format(time.RFC3339Nano)
	binding := enrollment.CredentialRotation{Version: "1", OwnerID: m.identity.OwnerID, NodeID: m.identity.NodeID, NewCredentialHash: base64.RawURLEncoding.EncodeToString(hash[:]), IssuedAt: issued}
	input, err := enrollment.CredentialRotationSigningInput(binding)
	if err != nil {
		return errors.New("credential rotation failed")
	}
	signature, err := m.signer.Sign(ctx, input)
	if err != nil {
		return errors.New("credential rotation failed")
	}
	defer clear(signature)
	old := append([]byte(nil), m.credential...)
	defer clear(old)
	if err := m.secrets.Put(ctx, m.credentialRef, newCredential); err != nil {
		return errors.New("credential rotation failed")
	}
	err = m.nodeJSON(ctx, http.MethodPost, "/v1/nodes/"+url.PathEscape(m.identity.NodeID)+"/credential/rotate", map[string]string{"newCredentialHash": binding.NewCredentialHash, "issuedAt": issued, "signature": base64.RawURLEncoding.EncodeToString(signature)}, nil)
	if err != nil {
		_ = m.secrets.Put(context.Background(), m.credentialRef, old)
		return err
	}
	clear(m.credential)
	m.credential = append([]byte(nil), newCredential...)
	return nil
}

func (m *pairingManager) Close() { clear(m.credential); m.credential = nil }

func (m *pairingManager) nodeJSON(ctx context.Context, method, path string, body any, target any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return errors.New("pairing request failed")
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, m.baseURL+path, reader)
	if err != nil {
		return errors.New("pairing request failed")
	}
	request.Header.Set("X-Yuanshu-Node-ID", m.identity.NodeID)
	request.Header.Set("Authorization", "Bearer "+string(m.credential))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := m.httpClient.Do(request)
	if err != nil {
		return errors.New("pairing service is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("pairing request was rejected")
	}
	if target == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if decoder.Decode(target) != nil {
		return errors.New("pairing service response is invalid")
	}
	return nil
}
