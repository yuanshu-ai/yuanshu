package node

import (
	"bytes"
	"context"
	"crypto/rand"
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
	Required        bool   `json:"required"`
	PickerAvailable bool   `json:"pickerAvailable"`
	Platform        string `json:"platform"`
	DefaultName     string `json:"defaultName"`
	DefaultCodex    string `json:"defaultCodex"`
}

type setupSelection struct {
	session   string
	path      string
	name      string
	expiresAt time.Time
}

type nodeSetupController struct {
	mu         sync.Mutex
	platform   platform.Platform
	paths      paths
	configPath string
	force      bool
	active     bool
	selections map[string]setupSelection
	clock      func() time.Time
	onComplete func(string)
	httpClient *http.Client
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
	picker := s.platform != nil && s.platform.DirectoryPicker() != nil && s.platform.DirectoryPicker().Available()
	return &SetupView{Required: true, PickerAvailable: picker, Platform: string(s.platform.Family()), DefaultName: name, DefaultCodex: "codex"}
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
	switch request.Command {
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
		if err != nil || testRelay(ctx, client, request.RelayURL) != nil {
			response.Error = "relay_test_failed"
		} else {
			response.OK, response.Config = true, map[string]any{"relay": "ready"}
		}
	case "setup_complete":
		joinURL, err := s.complete(ctx, request)
		request.BootstrapSecret = ""
		request.JoinURL = ""
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

type pickedWorkspace struct{ Token, Name string }

func (s *nodeSetupController) pickWorkspace(ctx context.Context, session string) (pickedWorkspace, error) {
	if session == "" || s.platform.DirectoryPicker() == nil || !s.platform.DirectoryPicker().Available() {
		return pickedWorkspace{}, platform.ErrUnavailable
	}
	selected, err := s.platform.DirectoryPicker().PickDirectory(ctx)
	if err != nil {
		return pickedWorkspace{}, err
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

func (s *nodeSetupController) consumeSelection(session, token string) (setupSelection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.selections[token]
	delete(s.selections, token)
	if !ok || session == "" || item.session != session || !item.expiresAt.After(s.clock().UTC()) {
		return setupSelection{}, errors.New("workspace token is invalid")
	}
	return item, nil
}

func (s *nodeSetupController) complete(ctx context.Context, request localRequest) (string, error) {
	selection, err := s.consumeSelection(request.localSession, request.WorkspaceToken)
	if err != nil {
		return "", err
	}
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
	if request.BootstrapSecret == "" && request.JoinURL == "" {
		return "", errors.New("setup enrollment is required")
	}
	if request.BootstrapSecret != "" && request.JoinURL != "" {
		return "", errors.New("setup enrollment is ambiguous")
	}
	if request.JoinURL != "" && !validEnrollmentURL(request.JoinURL, request.RelayURL) {
		return "", errors.New("setup enrollment link is invalid")
	}
	facts, err := workspace.ValidateRoot(ctx, s.platform.Workspaces(), selection.path)
	if err != nil {
		return "", err
	}
	identityRef := setupSecretRef("identity", s.configPath)
	credentialRef := setupSecretRef("relay", s.configPath)
	workspaceID := setupWorkspaceID(facts.FileIdentity)
	value := config.Config{
		ConfigVersion: config.CurrentVersion,
		Host:          config.HostConfig{Name: name},
		Transport:     config.TransportConfig{Mode: config.TransportRelay},
		Relay:         config.RelayConfig{URL: request.RelayURL, ConnectTimeoutSeconds: 15, CredentialRef: credentialRef},
		Identity:      config.IdentityConfig{PrivateKeyRef: identityRef},
		Adapters:      config.AdaptersConfig{Codex: config.CodexAdapterConfig{Enabled: true, Binary: codexBinary, RuntimeMode: "stdio"}},
		Events:        config.EventsConfig{MaxAgeHours: 168, MaxSizeMiB: 256},
		Workspaces:    []config.WorkspaceConfig{{ID: workspaceID, DisplayName: workspaceName, Path: facts.CanonicalPath, AllowedAdapters: []string{"codex"}, DefaultAdapter: "codex", PermissionProfile: permission, AllowNetwork: allowNetwork}},
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
	if s.platform.SecureStore() == nil || !s.platform.SecureStore().Available() {
		return "", platform.ErrUnavailable
	}
	if err := os.MkdirAll(s.paths.root, 0o700); err != nil || os.Chmod(s.paths.root, 0o700) != nil {
		return "", errors.New("setup data directory is unavailable")
	}
	local, err := store.Open(ctx, s.paths.database, store.Options{})
	if err != nil {
		return "", errors.New("setup database is unavailable")
	}
	defer local.Close()
	manager, err := identity.NewManager(local, s.platform.SecureStore(), identityRef, identity.Options{})
	if err != nil {
		return "", errors.New("setup identity is unavailable")
	}
	nodeIdentity, err := manager.Ensure(ctx)
	if err != nil {
		return "", errors.New("setup secure storage is unavailable")
	}
	credential, err := ensureSetupCredential(ctx, s.platform.SecureStore(), credentialRef)
	if err != nil {
		return "", err
	}
	defer clear(credential)
	if request.BootstrapSecret != "" {
		client := s.httpClient
		var clientErr error
		if value.Relay.CABundleFile != "" || client == nil {
			client, clientErr = relayHTTPClient("", 15*time.Second, value.Relay.CABundleFile)
		}
		if clientErr != nil {
			return "", errors.New("setup relay CA is invalid")
		}
		ownerID, nodeID, err := claimBootstrap(ctx, client, request.RelayURL, request.BootstrapSecret, name, nodeIdentity.PublicKey, credential)
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
	return request.JoinURL, nil
}

func ensureSetupCredential(ctx context.Context, secrets platform.SecureStore, ref platform.SecretRef) ([]byte, error) {
	credential, err := secrets.Get(ctx, ref)
	if err == nil && len(credential) >= 32 {
		return credential, nil
	}
	clear(credential)
	if err != nil && !errors.Is(err, platform.ErrNotFound) {
		return nil, errors.New("setup secure storage is unavailable")
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return nil, errors.New("setup credential generation failed")
	}
	credential = []byte(base64.RawURLEncoding.EncodeToString(raw))
	clear(raw)
	if err := secrets.Put(ctx, ref, credential); err != nil {
		clear(credential)
		return nil, errors.New("setup secure storage is unavailable")
	}
	return credential, nil
}

func claimBootstrap(ctx context.Context, client *http.Client, relayURL, secret, name string, publicKey, credential []byte) (string, string, error) {
	base, err := relayHTTPSBase(relayURL)
	if err != nil || secret == "" {
		return "", "", errors.New("setup bootstrap request is invalid")
	}
	credentialDigest := sha256.Sum256(credential)
	requestDigest := sha256.Sum256(publicKey)
	body := map[string]string{
		"requestId": "setup_" + base64.RawURLEncoding.EncodeToString(requestDigest[:16]),
		"name":      name, "os": runtime.GOOS, "version": "dev",
		"publicKey":      base64.RawURLEncoding.EncodeToString(publicKey),
		"credentialHash": base64.RawURLEncoding.EncodeToString(credentialDigest[:]),
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

func setupSecretRef(kind, configPath string) platform.SecretRef {
	digest := sha256.Sum256([]byte(filepath.Clean(configPath)))
	return platform.SecretRef("yuanshu.setup." + kind + "." + base64.RawURLEncoding.EncodeToString(digest[:12]))
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
	default:
		return "setup_failed"
	}
}
