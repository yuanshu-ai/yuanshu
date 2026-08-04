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
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/enrollment"
	"github.com/yuanshu-ai/yuanshu/internal/node/identity"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

type NodeEnrollmentCandidate struct{ EnrollmentID, CandidateNodeID, Name, OS, Version, PublicKey, CredentialHash, Fingerprint, ExpiresAt string }
type DeviceSummary struct {
	NodeID, Name, OS, Version, Status, Fingerprint string
	Online                                         bool
}

func (m *pairingManager) CreateNodeEnrollment(ctx context.Context) (string, error) {
	secret := make([]byte, 32)
	if _, err := io.ReadFull(m.random, secret); err != nil {
		return "", errors.New("node enrollment generation failed")
	}
	defer clear(secret)
	token := base64.RawURLEncoding.EncodeToString(secret)
	digest := sha256.Sum256([]byte(token))
	var response struct{ EnrollmentID, ExpiresAt string }
	if err := m.nodeJSON(ctx, http.MethodPost, "/v1/node-enrollments", map[string]string{"codeHash": base64.RawURLEncoding.EncodeToString(digest[:])}, &response); err != nil {
		return "", err
	}
	if response.EnrollmentID == "" {
		return "", errors.New("node enrollment response is invalid")
	}
	return m.baseURL + "/join#" + response.EnrollmentID + "." + token + "." + base64.RawURLEncoding.EncodeToString(m.identity.PublicKey), nil
}
func (m *pairingManager) PendingNodeEnrollments(ctx context.Context) ([]NodeEnrollmentCandidate, error) {
	var response struct {
		Enrollments []NodeEnrollmentCandidate `json:"enrollments"`
	}
	if err := m.nodeJSON(ctx, http.MethodGet, "/v1/node-enrollments", nil, &response); err != nil {
		return nil, err
	}
	return append([]NodeEnrollmentCandidate(nil), response.Enrollments...), nil
}
func (m *pairingManager) DecideNodeEnrollment(ctx context.Context, id, decision string) error {
	if decision != "accept" && decision != "decline" {
		return errors.New("node enrollment decision is invalid")
	}
	items, err := m.PendingNodeEnrollments(ctx)
	if err != nil {
		return err
	}
	var item NodeEnrollmentCandidate
	for _, candidate := range items {
		if candidate.EnrollmentID == id {
			item = candidate
			break
		}
	}
	if item.EnrollmentID == "" {
		return errors.New("node enrollment was not found")
	}
	binding := enrollment.NodeEnrollmentDecision{Version: "1", EnrollmentID: item.EnrollmentID, OwnerID: m.identity.OwnerID, IssuerNodeID: m.identity.NodeID, CandidateNodeID: item.CandidateNodeID, CandidatePublicKey: item.PublicKey, CredentialHash: item.CredentialHash, Name: item.Name, OS: item.OS, NodeVersion: item.Version, Decision: decision, ExpiresAt: item.ExpiresAt}
	input, err := enrollment.NodeEnrollmentDecisionSigningInput(binding)
	if err != nil {
		return errors.New("node enrollment request is invalid")
	}
	signature, err := m.signer.Sign(ctx, input)
	if err != nil {
		return errors.New("node enrollment signing failed")
	}
	defer clear(signature)
	return m.nodeJSON(ctx, http.MethodPost, "/v1/node-enrollments/"+url.PathEscape(id)+"/decision", map[string]string{"decision": decision, "signature": base64.RawURLEncoding.EncodeToString(signature)}, nil)
}
func (m *pairingManager) Devices(ctx context.Context) ([]DeviceSummary, error) {
	var response struct {
		Nodes []DeviceSummary `json:"nodes"`
	}
	if err := m.nodeJSON(ctx, http.MethodGet, "/v1/nodes", nil, &response); err != nil {
		return nil, err
	}
	return append([]DeviceSummary(nil), response.Nodes...), nil
}
func (m *pairingManager) RevokeNode(ctx context.Context, nodeID string) error {
	issued := m.clock().UTC().Format(time.RFC3339Nano)
	binding := enrollment.NodeRevocation{Version: "1", OwnerID: m.identity.OwnerID, IssuerNodeID: m.identity.NodeID, TargetNodeID: nodeID, IssuedAt: issued}
	input, err := enrollment.NodeRevocationSigningInput(binding)
	if err != nil {
		return errors.New("node revocation is invalid")
	}
	signature, err := m.signer.Sign(ctx, input)
	if err != nil {
		return errors.New("node revocation signing failed")
	}
	defer clear(signature)
	return m.nodeJSON(ctx, http.MethodDelete, "/v1/nodes/"+url.PathEscape(nodeID), map[string]string{"issuedAt": issued, "signature": base64.RawURLEncoding.EncodeToString(signature)}, nil)
}
func (m *pairingManager) SyncTrust(ctx context.Context) error {
	credential := m.credentialCopy()
	defer clear(credential)
	return syncOwnerTrust(ctx, m.httpClient, m.baseURL, m.identity.OwnerID, m.identity.NodeID, credential, m.local)
}

func syncOwnerTrust(ctx context.Context, client *http.Client, baseURL, ownerID, nodeID string, credential []byte, local *store.Store) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/control-clients", nil)
	if err != nil {
		return errors.New("control trust synchronization request failed")
	}
	request.Header.Set("X-Yuanshu-Node-ID", nodeID)
	request.Header.Set("Authorization", "Bearer "+string(credential))
	response, err := client.Do(request)
	if err != nil {
		return errors.New("control trust synchronization transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("control trust synchronization was rejected")
	}
	var wire struct {
		Revision int64                                                 `json:"revision"`
		Clients  []struct{ ClientID, KeyID, PublicKey, Status string } `json:"clients"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil {
		return errors.New("control trust synchronization response failed")
	}
	manifest := store.TrustManifest{OwnerID: ownerID, NodeID: nodeID, Revision: wire.Revision}
	for _, item := range wire.Clients {
		key, err := base64.RawURLEncoding.DecodeString(item.PublicKey)
		status := protocolv1.TrustStatus(item.Status)
		if err != nil || len(key) != ed25519.PublicKeySize || (status != protocolv1.TrustStatusActive && status != protocolv1.TrustStatusRevoked) {
			return errors.New("control trust synchronization manifest failed")
		}
		manifest.Clients = append(manifest.Clients, store.TrustedClientRecord{OwnerID: ownerID, NodeID: nodeID, ClientID: item.ClientID, KeyID: item.KeyID, PublicKey: key, Status: status})
	}
	if err := local.ReconcileTrustManifest(ctx, manifest); err != nil && !errors.Is(err, store.ErrConflict) {
		return errors.New("control trust synchronization storage failed")
	}
	return nil
}

type nodeEnrollmentJoiner struct {
	baseURL       string
	client        *http.Client
	identity      identity.Identity
	signer        *identity.Manager
	local         *store.Store
	secrets       platform.SecureStore
	credentialRef platform.SecretRef
	name, version string
	random        io.Reader
	onComplete    func()
	onError       func(error)
	cancel        context.CancelFunc
	mu            sync.Mutex
}
type enrollmentJoinerOptions struct {
	RelayURL      string
	HTTPClient    *http.Client
	Identity      identity.Identity
	Signer        *identity.Manager
	Local         *store.Store
	Secrets       platform.SecureStore
	CredentialRef platform.SecretRef
	Name, Version string
	Random        io.Reader
	OnComplete    func()
	OnError       func(error)
}

func newNodeEnrollmentJoiner(o enrollmentJoinerOptions) (*nodeEnrollmentJoiner, error) {
	parsed, err := url.Parse(o.RelayURL)
	if err != nil || !validNodeRelayEndpoint(parsed) || o.Identity.OwnerID != "" || o.Identity.NodeID != "" || len(o.Identity.PublicKey) != ed25519.PublicKeySize || o.Signer == nil || o.Local == nil || o.Secrets == nil || o.CredentialRef == "" {
		return nil, errors.New("node enrollment configuration is unavailable")
	}
	if parsed.Scheme == "wss" {
		parsed.Scheme = "https"
	} else {
		parsed.Scheme = "http"
	}
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	client := o.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	random := o.Random
	if random == nil {
		random = rand.Reader
	}
	return &nodeEnrollmentJoiner{baseURL: strings.TrimRight(parsed.String(), "/"), client: client, identity: o.Identity, signer: o.Signer, local: o.Local, secrets: o.Secrets, credentialRef: o.CredentialRef, name: o.Name, version: o.Version, random: random, onComplete: o.OnComplete, onError: o.OnError}, nil
}

func (j *nodeEnrollmentJoiner) Join(ctx context.Context, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.RawQuery != "" || parsed.User != nil || parsed.Fragment == "" {
		return errors.New("node enrollment link is invalid")
	}
	origin := parsed.Scheme + "://" + parsed.Host
	if origin != j.baseURL {
		return errors.New("node enrollment origin is invalid")
	}
	parts := strings.Split(parsed.Fragment, ".")
	if len(parts) != 3 || !validLocalID(parts[0]) {
		return errors.New("node enrollment link is invalid")
	}
	secretBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(secretBytes) != 32 || base64.RawURLEncoding.EncodeToString(secretBytes) != parts[1] {
		return errors.New("node enrollment link is invalid")
	}
	clear(secretBytes)
	secret := []byte(parts[1])
	issuerKey, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(issuerKey) != ed25519.PublicKeySize {
		clear(secret)
		return errors.New("node enrollment link is invalid")
	}
	credential, err := j.secrets.Get(ctx, j.credentialRef)
	created := false
	if errors.Is(err, platform.ErrNotFound) {
		rawCredential := make([]byte, 32)
		if _, err = io.ReadFull(j.random, rawCredential); err == nil {
			credential = []byte(base64.RawURLEncoding.EncodeToString(rawCredential))
			clear(rawCredential)
			err = j.secrets.Put(ctx, j.credentialRef, credential)
			created = err == nil
		}
	}
	if err != nil || len(credential) < 32 {
		clear(secret)
		clear(credential)
		return errors.New("node enrollment credential is unavailable")
	}
	digest := sha256.Sum256(credential)
	var claim struct{ Status, CandidateNodeID, Fingerprint, ExpiresAt string }
	body := map[string]string{"name": j.name, "os": runtime.GOOS, "version": j.version, "publicKey": base64.RawURLEncoding.EncodeToString(j.identity.PublicKey), "credentialHash": base64.RawURLEncoding.EncodeToString(digest[:])}
	if err := j.candidateJSON(ctx, http.MethodPost, "/v1/node-enrollments/"+url.PathEscape(parts[0])+"/claim", string(secret), body, &claim); err != nil {
		if created {
			_ = j.secrets.Delete(context.Background(), j.credentialRef)
		}
		clear(secret)
		clear(credential)
		return err
	}
	monitorCtx, cancel := context.WithCancel(context.Background())
	j.mu.Lock()
	if j.cancel != nil {
		j.cancel()
	}
	j.cancel = cancel
	j.mu.Unlock()
	go j.monitor(monitorCtx, parts[0], secret, issuerKey, credential, claim, body)
	return nil
}
func (j *nodeEnrollmentJoiner) monitor(ctx context.Context, id string, secret, issuerKey, credential []byte, claim struct{ Status, CandidateNodeID, Fingerprint, ExpiresAt string }, body map[string]string) {
	defer clear(secret)
	defer clear(credential)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var status struct{ Status, OwnerID, NodeID, IssuerNodeID, IssuerPublicKey, Proof, ExpiresAt string }
			if j.candidateJSON(ctx, http.MethodGet, "/v1/node-enrollments/"+url.PathEscape(id)+"/status", string(secret), nil, &status) != nil {
				continue
			}
			if status.Status == "declined" || status.Status == "expired" {
				_ = j.secrets.Delete(context.Background(), j.credentialRef)
				return
			}
			if status.Status != "approved" {
				continue
			}
			returnedKey, err := base64.RawURLEncoding.DecodeString(status.IssuerPublicKey)
			proof, proofErr := base64.RawURLEncoding.DecodeString(status.Proof)
			binding := enrollment.NodeEnrollmentDecision{Version: "1", EnrollmentID: id, OwnerID: status.OwnerID, IssuerNodeID: status.IssuerNodeID, CandidateNodeID: claim.CandidateNodeID, CandidatePublicKey: body["publicKey"], CredentialHash: body["credentialHash"], Name: body["name"], OS: body["os"], NodeVersion: body["version"], Decision: "accept", ExpiresAt: claim.ExpiresAt}
			input, inputErr := enrollment.NodeEnrollmentDecisionSigningInput(binding)
			if err != nil || proofErr != nil || inputErr != nil || !bytes.Equal(returnedKey, issuerKey) || !ed25519.Verify(issuerKey, input, proof) {
				j.report(errors.New("node enrollment proof validation failed"))
				return
			}
			if err := syncOwnerTrust(ctx, j.client, j.baseURL, status.OwnerID, status.NodeID, credential, j.local); err != nil {
				j.report(err)
				return
			}
			if _, err := j.signer.Bind(ctx, status.OwnerID, status.NodeID); err != nil {
				j.report(errors.New("node enrollment binding failed"))
				return
			}
			if j.onComplete != nil {
				go j.onComplete()
			}
			return
		}
	}
}
func (j *nodeEnrollmentJoiner) report(err error) {
	if j.onError != nil {
		j.onError(err)
	}
}
func (j *nodeEnrollmentJoiner) candidateJSON(ctx context.Context, method, path, secret string, body, target any) error {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, j.baseURL+path, reader)
	if err != nil {
		return errors.New("node enrollment request failed")
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := j.client.Do(request)
	if err != nil {
		return errors.New("node enrollment service is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("node enrollment request was rejected")
	}
	if target == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return errors.New("node enrollment response is invalid")
	}
	return nil
}
func (j *nodeEnrollmentJoiner) Close() {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cancel != nil {
		j.cancel()
		j.cancel = nil
	}
}
