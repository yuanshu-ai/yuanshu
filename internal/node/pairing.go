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
	"sync"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/enrollment"
	"github.com/yuanshu-ai/yuanshu/internal/node/identity"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
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
	random            io.Reader
	clock             func() time.Time
	mu                sync.RWMutex
	sessionToken      []byte
	sessionExpiresAt  time.Time
	migrateLegacy     func(context.Context) error
}

type pairingManagerOptions struct {
	RelayURL         string
	Timeout          time.Duration
	HTTPClient       *http.Client
	Identity         identity.Identity
	Signer           *identity.Manager
	Local            *store.Store
	Random           io.Reader
	Clock            func() time.Time
	SessionToken     []byte
	SessionExpiresAt time.Time
	MigrateLegacy    func(context.Context) error
}

var errRelayRevoked = errors.New("node identity is revoked or requires an authentication upgrade")

func newPairingManager(options pairingManagerOptions) (*pairingManager, error) {
	parsed, err := url.Parse(options.RelayURL)
	if err != nil || !validNodeRelayEndpoint(parsed) || options.Signer == nil || options.Local == nil || options.Identity.OwnerID == "" || options.Identity.NodeID == "" || len(options.Identity.PublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("node pairing configuration is unavailable")
	}
	base := *parsed
	if parsed.Scheme == "wss" {
		base.Scheme = "https"
	} else {
		base.Scheme = "http"
	}
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
	return &pairingManager{baseURL: strings.TrimRight(base.String(), "/"), relayURL: options.RelayURL, httpClient: client, identity: options.Identity, signer: options.Signer, local: options.Local, random: random, clock: clock, sessionToken: append([]byte(nil), options.SessionToken...), sessionExpiresAt: options.SessionExpiresAt, migrateLegacy: options.MigrateLegacy}, nil
}

func validNodeRelayEndpoint(parsed *url.URL) bool {
	if parsed == nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme == "wss" {
		return true
	}
	host := parsed.Hostname()
	return parsed.Scheme == "ws" && (host == "127.0.0.1" || host == "::1")
}

func (m *pairingManager) Connect(ctx context.Context) (transport.Transport, error) {
	header := make(http.Header)
	header.Set("X-Yuanshu-Node-ID", m.identity.NodeID)
	connection, response, err := transport.DialRelay(ctx, m.relayURL, transport.RelayDialOptions{HTTPClient: m.httpClient, Header: header, Role: transport.SessionRoleNode, SubjectID: m.identity.NodeID, Sign: m.signer.Sign, OnAuthenticated: func(ready transport.SessionReady) error {
		token, decodeErr := base64.RawURLEncoding.DecodeString(ready.SessionToken)
		if decodeErr != nil || len(token) != 32 {
			clear(token)
			return errors.New("node session is invalid")
		}
		m.setSession(token, ready.SessionExpiresAt)
		clear(token)
		return nil
	}, Relay: transport.RelayOptions{MaxSendBytes: protocolv1.EventFrameMaxBytes, MaxReceiveBytes: protocolv1.ControlFrameMaxBytes}})
	if err != nil {
		if response != nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUpgradeRequired) {
			return nil, errRelayRevoked
		}
		return nil, errors.New("node relay connection failed")
	}
	if m.migrateLegacy != nil {
		_ = m.migrateLegacy(ctx)
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
	token := base64.RawURLEncoding.EncodeToString(secret)
	hash := sha256.Sum256([]byte(token))
	var response struct{ PairingID, ExpiresAt string }
	err := m.nodeJSON(ctx, http.MethodPost, "/v1/control-client-pairings", map[string]string{"codeHash": base64.RawURLEncoding.EncodeToString(hash[:]), "challenge": base64.RawURLEncoding.EncodeToString(challenge)}, &response)
	clear(challenge)
	if err != nil {
		return "", err
	}
	if response.PairingID == "" {
		return "", errors.New("pairing service response is invalid")
	}
	return m.baseURL + "/pair#" + response.PairingID + "." + token, nil
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
	return m.refreshSession(ctx, true)
}

func (m *pairingManager) Close() {
	m.mu.Lock()
	clear(m.sessionToken)
	m.sessionToken = nil
	m.sessionExpiresAt = time.Time{}
	m.mu.Unlock()
}

func (m *pairingManager) setSession(token []byte, expires string) {
	expiresAt, _ := time.Parse(time.RFC3339Nano, expires)
	m.mu.Lock()
	clear(m.sessionToken)
	m.sessionToken = append([]byte(nil), token...)
	m.sessionExpiresAt = expiresAt
	m.mu.Unlock()
}

func (m *pairingManager) sessionCopy() ([]byte, time.Time) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]byte(nil), m.sessionToken...), m.sessionExpiresAt
}

func (m *pairingManager) sessionAuthorization(ctx context.Context) ([]byte, error) {
	token, expiresAt := m.sessionCopy()
	if len(token) == 32 && expiresAt.After(m.clock().UTC().Add(5*time.Minute)) {
		return token, nil
	}
	clear(token)
	if err := m.refreshSession(ctx, false); err != nil {
		return nil, err
	}
	token, expiresAt = m.sessionCopy()
	if len(token) != 32 || !expiresAt.After(m.clock().UTC()) {
		clear(token)
		return nil, errors.New("node session is unavailable")
	}
	return token, nil
}

func (m *pairingManager) refreshSession(ctx context.Context, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sessionToken) != 32 || !m.sessionExpiresAt.After(m.clock().UTC()) {
		return errors.New("node session is unavailable")
	}
	if !force && m.sessionExpiresAt.After(m.clock().UTC().Add(5*time.Minute)) {
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/v1/node-sessions/refresh", bytes.NewReader([]byte("{}")))
	if err != nil {
		return errors.New("node session refresh failed")
	}
	request.Header.Set("X-Yuanshu-Node-ID", m.identity.NodeID)
	request.Header.Set("Authorization", "YuanshuNodeSession "+base64.RawURLEncoding.EncodeToString(m.sessionToken))
	request.Header.Set("Content-Type", "application/json")
	response, err := m.httpClient.Do(request)
	if err != nil {
		return errors.New("node session refresh failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("node session refresh rejected")
	}
	var result struct{ SessionToken, SessionExpiresAt string }
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil {
		return errors.New("node session refresh invalid")
	}
	raw, decodeErr := base64.RawURLEncoding.DecodeString(result.SessionToken)
	expiresAt, timeErr := time.Parse(time.RFC3339Nano, result.SessionExpiresAt)
	if decodeErr != nil || timeErr != nil || len(raw) != 32 || !expiresAt.After(m.clock().UTC()) {
		clear(raw)
		return errors.New("node session refresh invalid")
	}
	clear(m.sessionToken)
	m.sessionToken = raw
	m.sessionExpiresAt = expiresAt
	return nil
}

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
	token, err := m.sessionAuthorization(ctx)
	if err != nil {
		return err
	}
	defer clear(token)
	request.Header.Set("Authorization", "YuanshuNodeSession "+base64.RawURLEncoding.EncodeToString(token))
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
