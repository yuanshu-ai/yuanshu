package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/identity"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/node/workspace"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

const setupWorkspaceTokenTTL = 5 * time.Minute

type SetupView struct {
	Required             bool   `json:"required"`
	PickerAvailable      bool   `json:"pickerAvailable"`
	WorkspacePreselected bool   `json:"workspacePreselected,omitempty"`
	RelayCAPreselected   bool   `json:"relayCaPreselected,omitempty"`
	Platform             string `json:"platform"`
	DefaultName          string `json:"defaultName"`
	DefaultCodex         string `json:"defaultCodex"`
	Locale               string `json:"locale,omitempty"`
}

type discoveredServer struct {
	Product                string `json:"product"`
	APIVersion             string `json:"apiVersion"`
	DeploymentMode         string `json:"deploymentMode"`
	PublicURL              string `json:"publicUrl"`
	NodeRelayURL           string `json:"nodeRelayUrl"`
	PairingURL             string `json:"pairingUrl"`
	NodeInvitationsAllowed bool   `json:"nodeInvitationsAllowed"`
	CAFingerprint          string `json:"caFingerprint,omitempty"`
}

type setupSelection struct {
	session    string
	path       string
	name       string
	expiresAt  time.Time
	processing bool
}

type nodeSetupController struct {
	mu             sync.Mutex
	platform       platform.Platform
	paths          paths
	configPath     string
	force          bool
	active         bool
	selections     map[string]setupSelection
	clock          func() time.Time
	onComplete     func(string)
	httpClient     *http.Client
	localWorkspace string
	localRelayCA   string
}

// setLocalWorkspace provides a path selected by a person at the local CLI.
// The browser still receives only a session-bound opaque token.
func (s *nodeSetupController) setLocalWorkspace(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		s.mu.Lock()
		s.localWorkspace = ""
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.localWorkspace = filepath.Clean(path)
	s.mu.Unlock()
}

// setLocalRelayCA loads a public CA certificate selected at the local CLI.
// Neither its path nor its PEM contents are exposed to the setup browser.
func (s *nodeSetupController) setLocalRelayCA(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		return platform.ErrInvalidArgument
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil || validateRelayCABundle(raw) != nil {
		clear(raw)
		return errors.New("relay CA bundle is invalid")
	}
	s.mu.Lock()
	s.localRelayCA = string(raw)
	s.mu.Unlock()
	clear(raw)
	return nil
}

func newNodeSetupController(value platform.Platform, paths paths, configPath string, force bool, onComplete func(string)) *nodeSetupController {
	active := force
	if _, err := os.Lstat(configPath); errors.Is(err, os.ErrNotExist) {
		active = true
	}
	client, _ := relayHTTPClient("", 15*time.Second)
	return &nodeSetupController{platform: value, paths: paths, configPath: configPath, force: force, active: active, selections: map[string]setupSelection{}, clock: time.Now, onComplete: onComplete, httpClient: client}
}

func (s *nodeSetupController) view() *SetupView {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return nil
	}
	name, _ := os.Hostname()
	if strings.TrimSpace(name) == "" {
		name = "My Node"
	}
	preselected := s.localWorkspace != ""
	picker := preselected || (s.platform != nil && s.platform.DirectoryPicker() != nil && s.platform.DirectoryPicker().Available())
	return &SetupView{Required: true, PickerAvailable: picker, WorkspacePreselected: preselected, RelayCAPreselected: s.localRelayCA != "", Platform: string(s.platform.Family()), DefaultName: name, DefaultCodex: "codex"}
}

func (s *nodeSetupController) handle(ctx context.Context, request localRequest) localResponse {
	response := localResponse{Protocol: localProtocol}
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if request.Command == "setup_status" {
		response.OK, response.Setup = true, s.view()
		return response
	}
	if !active {
		response.Error = "setup_not_required"
		return response
	}
	if request.RelayCABundle == "" {
		s.mu.Lock()
		request.RelayCABundle = s.localRelayCA
		s.mu.Unlock()
	}
	switch request.Command {
	case "setup_discover":
		server, err := s.discoverLocalServer(ctx)
		if err != nil {
			response.Error = "server_not_discovered"
			return response
		}
		response.OK, response.Config = true, map[string]any{"server": server}
	case "setup_pick":
		selection, err := s.pickWorkspace(ctx, request.localSession)
		if err != nil {
			response.Error = setupErrorCode(err)
			return response
		}
		response.OK, response.WorkspaceToken, response.WorkspaceName = true, selection.Token, selection.Name
	case "setup_test":
		client, err := setupRelayClient(request.RelayCABundle)
		if request.RelayCABundle == "" && s.httpClient != nil {
			client = s.httpClient
		}
		server, discoverErr := discoverServer(ctx, client, request.ServerURL)
		if request.ServerURL == "" {
			server = discoveredServer{NodeRelayURL: request.RelayURL}
			discoverErr = testRelay(ctx, client, request.RelayURL)
		}
		if err != nil || discoverErr != nil {
			response.Error = "relay_test_failed"
		} else {
			response.OK, response.Config = true, map[string]any{"relay": "ready", "server": server}
		}
	case "setup_complete":
		joinURL, err := s.complete(ctx, request)
		request.BootstrapSecret = ""
		request.JoinURL = ""
		request.Invitation = ""
		request.InvitationCode = ""
		if err != nil {
			response.Error = setupErrorCode(err)
			return response
		}
		response.OK = true
		if s.onComplete != nil {
			go s.onComplete(joinURL)
		}
	default:
		response.Error = "unsupported_command"
	}
	return response
}

func (s *nodeSetupController) discoverLocalServer(ctx context.Context) (discoveredServer, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	type result struct {
		value discoveredServer
		err   error
	}
	results := make(chan result, 2)
	for _, target := range []string{"http://127.0.0.1:9527", "http://[::1]:9527"} {
		go func(raw string) {
			value, err := discoverServer(ctx, s.httpClient, raw)
			results <- result{value: value, err: err}
		}(target)
	}
	for range 2 {
		select {
		case item := <-results:
			if item.err == nil && item.value.DeploymentMode == "local" {
				return item.value, nil
			}
		case <-ctx.Done():
			return discoveredServer{}, ctx.Err()
		}
	}
	return discoveredServer{}, errors.New("local server was not found")
}

func discoverServer(ctx context.Context, client *http.Client, raw string) (discoveredServer, error) {
	parsed, err := url.Parse(strings.TrimSuffix(strings.TrimSpace(raw), "/"))
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return discoveredServer{}, errors.New("server URL is invalid")
	}
	loopback := parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback) {
		return discoveredServer{}, errors.New("server URL must use HTTPS")
	}
	if client == nil {
		client, _ = relayHTTPClient("", time.Second)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String()+"/.well-known/yuanshu", nil)
	response, err := client.Do(request)
	if err != nil {
		return discoveredServer{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return discoveredServer{}, errors.New("server discovery failed")
	}
	var value discoveredServer
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || value.Product != "yuanshu" || value.APIVersion != "1" || value.NodeRelayURL == "" || value.PublicURL == "" {
		return discoveredServer{}, errors.New("server identity is invalid")
	}
	relay, relayErr := url.Parse(value.NodeRelayURL)
	if relayErr != nil || !validNodeRelayEndpoint(relay) || relay.Host != parsed.Host {
		return discoveredServer{}, errors.New("server relay endpoint is invalid")
	}
	if loopback && (value.DeploymentMode != "local" || value.PublicURL != parsed.String()) {
		return discoveredServer{}, errors.New("local server identity is invalid")
	}
	return value, nil
}

type pickedWorkspace struct{ Token, Name string }

func (s *nodeSetupController) pickWorkspace(ctx context.Context, session string) (pickedWorkspace, error) {
	if session == "" || s.platform == nil {
		return pickedWorkspace{}, platform.ErrUnavailable
	}
	s.mu.Lock()
	localWorkspace := s.localWorkspace
	s.mu.Unlock()
	selected := platform.DirectorySelection{Path: localWorkspace, DisplayName: filepath.Base(localWorkspace)}
	if localWorkspace == "" {
		if s.platform.DirectoryPicker() == nil || !s.platform.DirectoryPicker().Available() {
			return pickedWorkspace{}, platform.ErrUnavailable
		}
		var err error
		selected, err = s.platform.DirectoryPicker().PickDirectory(ctx)
		if err != nil {
			return pickedWorkspace{}, err
		}
	}
	facts, err := workspace.ValidateRoot(ctx, s.platform.Workspaces(), selected.Path)
	if err != nil {
		return pickedWorkspace{}, err
	}
	token, err := randomControlCenterToken()
	if err != nil {
		return pickedWorkspace{}, err
	}
	name := strings.TrimSpace(selected.DisplayName)
	if name == "" {
		name = filepath.Base(facts.CanonicalPath)
	}
	s.mu.Lock()
	now := s.clock().UTC()
	for key, item := range s.selections {
		if !item.expiresAt.After(now) {
			delete(s.selections, key)
		}
	}
	s.selections[token] = setupSelection{session: session, path: facts.CanonicalPath, name: name, expiresAt: now.Add(setupWorkspaceTokenTTL)}
	s.mu.Unlock()
	return pickedWorkspace{Token: token, Name: name}, nil
}

func (s *nodeSetupController) beginSelection(session, token string) (setupSelection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.selections[token]
	if !ok || item.processing || session == "" || item.session != session || !item.expiresAt.After(s.clock().UTC()) {
		return setupSelection{}, errors.New("workspace token is invalid")
	}
	item.processing = true
	s.selections[token] = item
	return item, nil
}

func (s *nodeSetupController) finishSelection(session, token string, succeeded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.selections[token]
	if !ok || item.session != session {
		return
	}
	if succeeded {
		delete(s.selections, token)
		return
	}
	item.processing = false
	s.selections[token] = item
}

func (s *nodeSetupController) complete(ctx context.Context, request localRequest) (string, error) {
	selection, err := s.beginSelection(request.localSession, request.WorkspaceToken)
	if err != nil {
		return "", err
	}
	selectionSucceeded := false
	defer func() { s.finishSelection(request.localSession, request.WorkspaceToken, selectionSucceeded) }()
	permission := config.PermissionProfile(request.PermissionProfile)
	if permission == "" {
		permission = config.PermissionReadOnly
	}
	allowNetwork := request.AllowNetwork != nil && *request.AllowNetwork
	name := strings.TrimSpace(request.Name)
	workspaceName := strings.TrimSpace(request.WorkspaceName)
	if workspaceName == "" {
		workspaceName = selection.name
	}
	codexBinary := strings.TrimSpace(request.CodexBinary)
	if codexBinary == "" {
		codexBinary = "codex"
	}
	methods := 0
	for _, value := range []string{request.BootstrapSecret, request.Invitation, request.InvitationCode, request.JoinURL} {
		if strings.TrimSpace(value) != "" {
			methods++
		}
	}
	if methods == 0 {
		return "", errors.New("setup enrollment is required")
	}
	if methods != 1 {
		return "", errors.New("setup enrollment is ambiguous")
	}
	if request.JoinURL != "" && !validEnrollmentURL(request.JoinURL, request.RelayURL) {
		return "", errors.New("setup enrollment link is invalid")
	}
	facts, err := workspace.ValidateRoot(ctx, s.platform.Workspaces(), selection.path)
	if err != nil {
		return "", err
	}
	workspaceID := setupWorkspaceID(facts.FileIdentity)
	value := config.Config{
		ConfigVersion:  config.CurrentVersion,
		Host:           config.HostConfig{Name: name, Locale: setupLocale(request.Locale)},
		Transport:      config.TransportConfig{Mode: config.TransportRelay},
		Relay:          config.RelayConfig{URL: request.RelayURL, ConnectTimeoutSeconds: 15},
		Identity:       config.IdentityConfig{KeyFile: config.DefaultIdentityKeyFile},
		AgentInstances: []config.AgentInstanceConfig{{ID: config.DefaultCodexInstanceID, AdapterType: "codex", DisplayName: "Codex", Enabled: true, IsDefault: true, RuntimeMode: config.AgentRuntimeManaged, Codex: &config.CodexAdapterConfig{Enabled: true, Binary: codexBinary, RuntimeMode: "stdio"}}},
		Events:         config.EventsConfig{MaxAgeHours: 168, MaxSizeMiB: 256},
		Workspaces:     []config.WorkspaceConfig{{ID: workspaceID, DisplayName: workspaceName, Path: facts.CanonicalPath, AllowedAgentInstances: []string{config.DefaultCodexInstanceID}, DefaultAgentInstance: config.DefaultCodexInstanceID, PermissionProfile: permission, AllowNetwork: allowNetwork}},
	}
	setupSaved := false
	var previousCA []byte
	previousCAExists := false
	if request.RelayCABundle != "" {
		caPath := filepath.Join(s.paths.root, "relay-ca.pem")
		if raw, readErr := os.ReadFile(caPath); readErr == nil {
			previousCA, previousCAExists = raw, true
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return "", errors.New("existing relay CA is unavailable")
		}
		if err := writeRelayCABundle(caPath, []byte(request.RelayCABundle)); err != nil {
			return "", err
		}
		defer func() {
			if setupSaved {
				return
			}
			if previousCAExists {
				_ = writeRelayCABundle(caPath, previousCA)
			} else {
				_ = os.Remove(caPath)
			}
		}()
		value.Relay.CABundleFile = caPath
	}
	if err := config.Validate(value); err != nil {
		return "", errors.New("setup configuration is invalid")
	}
	if err := os.MkdirAll(s.paths.root, 0o700); err != nil || os.Chmod(s.paths.root, 0o700) != nil {
		return "", errors.New("setup data directory is unavailable")
	}
	if existingStore, storeErr := config.NewFileStore(s.configPath); storeErr == nil {
		if existing, loadErr := existingStore.Load(ctx); loadErr == nil && existing.Config.Identity.PrivateKeyRef != "" {
			return "", errIdentityRepairRequired
		}
	}
	local, err := store.Open(ctx, s.paths.database, store.Options{})
	if err != nil {
		return "", errors.New("setup database is unavailable")
	}
	defer local.Close()
	identityStore, err := identity.NewFileKeyStore(filepath.Join(s.paths.root, config.DefaultIdentityKeyFile))
	if err != nil {
		return "", errors.New("setup identity path is invalid")
	}
	manager, err := identity.NewManager(local, identityStore, identity.Options{})
	if err != nil {
		return "", errors.New("setup identity is unavailable")
	}
	defer manager.Close()
	nodeIdentity, err := manager.Ensure(ctx)
	if err != nil {
		return "", errors.New("setup identity storage is unavailable")
	}
	if request.BootstrapSecret != "" {
		client := s.httpClient
		var clientErr error
		if value.Relay.CABundleFile != "" || client == nil {
			client, clientErr = relayHTTPClient("", 15*time.Second, value.Relay.CABundleFile)
		}
		if clientErr != nil {
			return "", errors.New("setup relay CA is invalid")
		}
		ownerID, nodeID, err := claimBootstrap(ctx, client, request.RelayURL, request.BootstrapSecret, name, nodeIdentity.PublicKey)
		if err != nil {
			return "", err
		}
		if _, err := manager.Bind(ctx, ownerID, nodeID); err != nil {
			return "", errors.New("setup identity binding failed")
		}
	} else if request.JoinURL == "" {
		client := s.httpClient
		var clientErr error
		if value.Relay.CABundleFile != "" || client == nil {
			client, clientErr = relayHTTPClient("", 15*time.Second, value.Relay.CABundleFile)
		}
		if clientErr != nil {
			return "", errors.New("setup relay CA is invalid")
		}
		ownerID, nodeID, err := claimNodeInvitation(ctx, client, request.ServerURL, request.RelayURL, request.Invitation, request.InvitationCode, name, nodeIdentity.PublicKey)
		if err != nil {
			return "", err
		}
		if _, err := manager.Bind(ctx, ownerID, nodeID); err != nil {
			return "", errors.New("setup identity binding failed")
		}
	}
	configurationStore, err := config.NewFileStore(s.configPath)
	if err != nil {
		return "", errors.New("setup configuration path is invalid")
	}
	if !s.force {
		if _, err := os.Lstat(s.configPath); err == nil {
			return "", errors.New("setup configuration already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", errors.New("setup configuration path is unavailable")
		}
	}
	if err := configurationStore.Save(ctx, value); err != nil {
		return "", errors.New("setup configuration could not be saved")
	}
	s.mu.Lock()
	s.active = false
	s.mu.Unlock()
	setupSaved = true
	selectionSucceeded = true
	return request.JoinURL, nil
}

func setupLocale(value string) string {
	if value == "en-US" {
		return value
	}
	return "zh-CN"
}

func claimBootstrap(ctx context.Context, client *http.Client, relayURL, secret, name string, publicKey []byte) (string, string, error) {
	base, err := relayHTTPSBase(relayURL)
	if err != nil || secret == "" {
		return "", "", errors.New("setup bootstrap request is invalid")
	}
	requestDigest := sha256.Sum256(publicKey)
	body := map[string]string{
		"requestId": "setup_" + base64.RawURLEncoding.EncodeToString(requestDigest[:16]),
		"name":      name, "os": runtime.GOOS, "version": "dev",
		"publicKey": base64.RawURLEncoding.EncodeToString(publicKey),
	}
	raw, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/bootstrap/claim", bytes.NewReader(raw))
	if err != nil {
		return "", "", errors.New("setup bootstrap request failed")
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set("Content-Type", "application/json")
	if client == nil {
		client, _ = relayHTTPClient("", 15*time.Second)
	}
	response, err := client.Do(request)
	if err != nil {
		return "", "", errors.New("setup bootstrap service is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", errors.New("setup bootstrap request was rejected")
	}
	var result struct{ OwnerID, NodeID, Status string }
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || result.OwnerID == "" || result.NodeID == "" || result.Status != "enrolled" {
		return "", "", errors.New("setup bootstrap response is invalid")
	}
	return result.OwnerID, result.NodeID, nil
}

type nodeInvitationWire struct {
	Version       int    `json:"version"`
	ServerURL     string `json:"serverUrl"`
	InvitationID  string `json:"invitationId"`
	Secret        string `json:"secret"`
	ExpiresAt     string `json:"expiresAt"`
	CACertificate string `json:"caCertificate,omitempty"`
	CAFingerprint string `json:"caFingerprint,omitempty"`
}

func claimNodeInvitation(ctx context.Context, client *http.Client, serverURL, relayURL, invitation, code, name string, publicKey []byte) (string, string, error) {
	base, err := relayHTTPSBase(relayURL)
	if err != nil {
		return "", "", errors.New("setup invitation request is invalid")
	}
	wire := nodeInvitationWire{}
	if invitation != "" {
		raw := []byte(strings.TrimSpace(invitation))
		if parsed, parseErr := url.Parse(string(raw)); parseErr == nil && parsed.Fragment != "" {
			if decoded, decodeErr := base64.RawURLEncoding.DecodeString(parsed.Fragment); decodeErr == nil {
				raw = decoded
			}
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&wire) != nil || wire.Version != 1 || wire.InvitationID == "" || wire.Secret == "" {
			return "", "", errors.New("setup invitation is invalid")
		}
		serverURL = wire.ServerURL
	}
	serverURL = strings.TrimRight(serverURL, "/")
	if serverURL == "" {
		serverURL = base
	}
	parsed, parseErr := url.Parse(serverURL)
	if parseErr != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Scheme+"://"+parsed.Host != base {
		return "", "", errors.New("setup invitation origin is invalid")
	}
	body := map[string]string{"name": name, "os": runtime.GOOS, "arch": runtime.GOARCH, "version": "dev", "publicKey": base64.RawURLEncoding.EncodeToString(publicKey)}
	if code != "" {
		body["shortCode"] = code
	} else {
		body["invitationId"], body["secret"] = wire.InvitationID, wire.Secret
	}
	raw, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/node-invitations/claim", bytes.NewReader(raw))
	if err != nil {
		return "", "", errors.New("setup invitation request failed")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", "", errors.New("setup invitation service is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return "", "", errors.New("setup invitation was rejected")
	}
	var result struct{ OwnerID, NodeID, Status string }
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || result.OwnerID == "" || result.NodeID == "" || result.Status != "active" {
		return "", "", errors.New("setup invitation response is invalid")
	}
	return result.OwnerID, result.NodeID, nil
}

func testRelay(ctx context.Context, client *http.Client, relayURL string) error {
	base, err := relayHTTPSBase(relayURL)
	if err != nil {
		return err
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if client == nil {
		client, _ = relayHTTPClient("", 15*time.Second)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("relay health check failed")
	}
	return nil
}

func relayHTTPSBase(relayURL string) (string, error) {
	parsed, err := url.Parse(relayURL)
	if err != nil || !validNodeRelayEndpoint(parsed) {
		return "", errors.New("relay URL is invalid")
	}
	if parsed.Scheme == "wss" {
		parsed.Scheme = "https"
	} else {
		parsed.Scheme = "http"
	}
	parsed.Path, parsed.RawPath = "", ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func validEnrollmentURL(raw, relayURL string) bool {
	parsed, err := url.Parse(raw)
	base, baseErr := relayHTTPSBase(relayURL)
	return err == nil && baseErr == nil && (parsed.Scheme == "https" || parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1")) && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment != "" && parsed.Scheme+"://"+parsed.Host == base
}

func setupRelayClient(caBundle string) (*http.Client, error) {
	client, err := relayHTTPClient("", 15*time.Second)
	if err != nil || caBundle == "" {
		return client, err
	}
	if len(caBundle) > 64<<10 {
		return nil, errors.New("relay CA bundle is too large")
	}
	if err := validateRelayCABundle([]byte(caBundle)); err != nil {
		return nil, err
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM([]byte(caBundle)) {
		return nil, errors.New("relay CA bundle is invalid")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}
	return &http.Client{Transport: transport, Timeout: 15 * time.Second}, nil
}

func validateRelayCABundle(raw []byte) error {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return errors.New("relay CA bundle is invalid")
	}
	rest := raw
	found := false
	for len(rest) > 0 {
		block, next := pem.Decode(rest)
		if block == nil {
			if len(bytes.TrimSpace(rest)) != 0 {
				return errors.New("relay CA bundle is invalid")
			}
			break
		}
		if block.Type != "CERTIFICATE" {
			return errors.New("relay CA bundle contains unsupported material")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid {
			return errors.New("relay CA bundle is invalid")
		}
		found, rest = true, next
	}
	if !found {
		return errors.New("relay CA bundle is invalid")
	}
	return nil
}

func writeRelayCABundle(path string, raw []byte) error {
	if err := validateRelayCABundle(raw); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil || os.Chmod(filepath.Dir(path), 0o700) != nil {
		return errors.New("relay CA directory is unavailable")
	}
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return errors.New("relay CA could not be stored")
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(raw); err != nil || file.Sync() != nil || file.Close() != nil || os.Rename(temporary, path) != nil {
		return errors.New("relay CA could not be stored")
	}
	ok = true
	return nil
}

func setupWorkspaceID(identity string) string {
	digest := sha256.Sum256([]byte(identity))
	return "ws_" + base64.RawURLEncoding.EncodeToString(digest[:12])
}

func setupErrorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "setup_canceled"
	case errors.Is(err, platform.ErrUnavailable):
		return "native_picker_unavailable"
	case errors.Is(err, workspace.ErrDenied):
		return "workspace_denied"
	case errors.Is(err, workspace.ErrUnavailable):
		return "workspace_unavailable"
	case errors.Is(err, errIdentityRepairRequired):
		return "identity_repair_required"
	case strings.Contains(err.Error(), "identity storage"):
		return "setup_identity_storage_unavailable"
	case strings.Contains(err.Error(), "secure storage"):
		return "setup_secure_store_unavailable"
	case strings.Contains(err.Error(), "bootstrap request was rejected"):
		return "setup_bootstrap_rejected"
	case strings.Contains(err.Error(), "bootstrap service is unavailable"):
		return "setup_relay_unavailable"
	case strings.Contains(err.Error(), "database"):
		return "setup_database_unavailable"
	case strings.Contains(err.Error(), "configuration"):
		return "setup_configuration_failed"
	default:
		return "setup_failed"
	}
}
