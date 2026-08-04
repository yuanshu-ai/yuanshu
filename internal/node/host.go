package node

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex"
	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/eventlog"
	"github.com/yuanshu-ai/yuanshu/internal/node/identity"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/node/workspace"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

const autostartID = "yuanshu-node"

type runOptions struct {
	paths              paths
	configPath         string
	background         bool
	platform           platform.Platform
	tray               tray
	setup              bool
	setupURLWriter     io.Writer
	setupWorkspacePath string
	setupRelayCAPath   string
}

type host struct {
	options runOptions
	status  *statusStore
	log     *operationalLog
	runCtx  context.Context

	mu               sync.Mutex
	local            *store.Store
	runtime          adapter.Runtime
	pairing          *pairingManager
	joiner           *nodeEnrollmentJoiner
	trustCancel      context.CancelFunc
	controlSession   *ControlSession
	relaySupervisor  *relaySupervisor
	controlEvents    *eventlog.Manager
	controlValidator *protocolv1.Validator
	controlTarget    protocolv1.Target
	controlName      string
	configController configController
	controlCenter    *controlCenter
	setupController  *nodeSetupController
	activeConfig     config.Config
	workspaceManager *workspace.Manager
	identityManager  *identity.Manager
	nodeIdentity     identity.Identity
}

func runHost(ctx context.Context, options runOptions) error {
	if ctx == nil {
		return context.Canceled
	}
	if options.platform == nil || options.platform.IPC() == nil || !options.platform.IPC().Available() ||
		options.platform.SecureStore() == nil || !options.platform.SecureStore().Available() ||
		options.platform.Processes() == nil || !options.platform.Processes().Available() ||
		options.platform.Workspaces() == nil || !options.platform.Workspaces().Available() {
		return platform.ErrUnavailable
	}
	if options.configPath == "" {
		options.configPath = options.paths.config
	}
	if !filepath.IsAbs(options.configPath) {
		return platform.ErrInvalidArgument
	}
	if options.platform.Family() == platform.FamilyDarwin {
		if err := prepareDarwinNodeRoot(options.paths.root); err != nil {
			return errors.New("node data directory is unavailable")
		}
	} else if err := os.MkdirAll(options.paths.root, 0o700); err != nil {
		return errors.New("node data directory is unavailable")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if options.tray == nil {
		options.tray = newPlatformTray(options.background)
	}
	h := &host{options: options, status: newStatusStore(string(options.platform.Family())), log: newOperationalLog(options.paths.log), runCtx: runCtx}
	h.setupController = newNodeSetupController(options.platform, options.paths, options.configPath, options.setup, func(joinURL string) {
		if h.reload(h.runCtx) == nil && joinURL != "" {
			_ = h.handleLocalManagement(h.runCtx, localRequest{Protocol: localProtocol, Command: "enrollment_join", JoinURL: joinURL})
		}
	})
	h.setupController.setLocalWorkspace(options.setupWorkspacePath)
	if err := h.setupController.setLocalRelayCA(options.setupRelayCAPath); err != nil {
		return errors.New("node setup relay CA is invalid")
	}
	center, err := newControlCenter(h.status.snapshot, h.handleLocalManagement)
	if err != nil {
		return err
	}
	h.controlCenter = center
	defer center.Close()
	h.refreshAutostart(runCtx)
	server, err := startLocalServer(runCtx, options.platform.IPC(), h.status.snapshot, cancel, h.handleLocalManagement)
	if err != nil {
		return errors.New("node local management is unavailable")
	}
	defer server.Close()
	h.log.write("node_starting", "starting", 0)
	if h.setupController.view() == nil {
		_ = h.reload(runCtx)
	} else {
		h.status.update(func(value *Status) {
			value.State = "needs_attention"
			value.Config = "setup_required"
			value.RemoteControl = "not_available"
		})
	}
	if h.setupController.view() != nil && !options.background {
		if options.setupURLWriter != nil {
			value, openErr := h.controlCenter.Open(runCtx)
			if openErr != nil {
				return errors.New("node setup page is unavailable")
			}
			if _, writeErr := fmt.Fprintf(options.setupURLWriter, "Yuanshu Node setup (expires in 1 minute): %s\n", value); writeErr != nil {
				return errors.New("node setup output failed")
			}
		} else {
			_ = h.openControlCenter(runCtx)
		}
	}
	options.tray.Update(h.status.snapshot())
	trayErr := options.tray.Run(runCtx, h.trayCallbacks(cancel))
	cancel()
	closeErr := h.close()
	if trayErr != nil && !errors.Is(trayErr, context.Canceled) {
		return errors.New("node tray is unavailable")
	}
	if closeErr != nil {
		return errors.New("node shutdown failed")
	}
	h.log.write("node_stopped", "stopped", 0)
	return nil
}

func (h *host) reload(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.closeResourcesLocked(); err != nil {
		return h.fail("shutdown_unavailable")
	}
	h.status.update(func(value *Status) {
		value.State, value.Config, value.Identity, value.Database = "recovering", "checking", "unchecked", "unchecked"
		value.Workspaces, value.Codex, value.Authentication, value.Recovery = 0, "unchecked", "unchecked", "not_required"
		value.RemoteControl = "not_available"
	})
	h.options.trayUpdate(h.status.snapshot())
	configurationStore, err := config.NewFileStore(h.options.configPath)
	if err != nil {
		return h.fail("config_invalid")
	}
	loaded, err := configurationStore.Load(ctx)
	if err != nil {
		return h.fail("config_unavailable")
	}
	h.status.update(func(value *Status) {
		if loaded.RecoveredFromBackup {
			value.Config = "recovered"
		} else {
			value.Config = "ready"
		}
	})
	local, err := store.Open(ctx, h.options.paths.database, store.Options{})
	if err != nil {
		return h.fail("database_unavailable")
	}
	h.local = local
	h.status.update(func(value *Status) { value.Database = "ready" })
	configurationController, err := newNodeConfigController(h.options.configPath, local, time.Now)
	if err != nil {
		return h.fail("config_invalid")
	}
	h.configController = configurationController
	workspaceManager, err := workspace.NewManager(h.options.platform.Workspaces(), local)
	if err != nil || workspaceManager.Reconcile(ctx, loaded.Config.Workspaces) != nil {
		return h.fail("workspace_unavailable")
	}
	h.status.update(func(value *Status) { value.Workspaces = len(loaded.Config.Workspaces) })
	h.workspaceManager = workspaceManager
	identityManager, err := identity.NewManager(local, h.options.platform.SecureStore(), loaded.Config.Identity.PrivateKeyRef, identity.Options{})
	if err != nil {
		return h.fail("identity_invalid")
	}
	nodeIdentity, err := identityManager.Ensure(ctx)
	if err != nil {
		return h.fail("identity_unavailable")
	}
	h.identityManager, h.nodeIdentity = identityManager, nodeIdentity
	bound := nodeIdentity.OwnerID != "" && nodeIdentity.NodeID != ""
	h.status.update(func(value *Status) {
		if bound {
			value.Identity = "bound"
		} else {
			value.Identity = "unpaired"
		}
	})
	if !bound && loaded.Config.Transport.Mode == config.TransportRelay {
		joiner, joinErr := newNodeEnrollmentJoiner(enrollmentJoinerOptions{RelayURL: loaded.Config.Relay.URL, Identity: nodeIdentity, Signer: identityManager, Local: local, Secrets: h.options.platform.SecureStore(), CredentialRef: loaded.Config.Relay.CredentialRef, Name: loaded.Config.Host.Name, Version: "dev", OnComplete: func() { _ = h.reload(h.runCtx) }})
		if joinErr == nil {
			h.joiner = joiner
		}
	}
	if bound && loaded.Config.Transport.Mode == config.TransportRelay {
		credential, secretErr := h.options.platform.SecureStore().Get(ctx, loaded.Config.Relay.CredentialRef)
		if secretErr == nil {
			httpClient, clientErr := relayHTTPClient(loaded.Config.Relay.ProxyURL, time.Duration(loaded.Config.Relay.ConnectTimeoutSeconds)*time.Second, loaded.Config.Relay.CABundleFile)
			manager, managerErr := newPairingManager(pairingManagerOptions{
				RelayURL: loaded.Config.Relay.URL, Timeout: time.Duration(loaded.Config.Relay.ConnectTimeoutSeconds) * time.Second,
				HTTPClient: httpClient,
				Identity:   nodeIdentity, Signer: identityManager, Local: local, Secrets: h.options.platform.SecureStore(),
				CredentialRef: loaded.Config.Relay.CredentialRef, Credential: credential,
			})
			clear(credential)
			if clientErr == nil && managerErr == nil {
				h.pairing = manager
				_ = manager.SyncTrust(ctx)
				h.status.update(func(value *Status) { value.RemoteControl = "connecting" })
				trustCtx, trustCancel := context.WithCancel(h.runCtx)
				h.trustCancel = trustCancel
				go func(value *pairingManager) {
					ticker := time.NewTicker(30 * time.Second)
					defer ticker.Stop()
					for {
						select {
						case <-trustCtx.Done():
							return
						case <-ticker.C:
							_ = value.SyncTrust(trustCtx)
						}
					}
				}(manager)
			}
		}
	}
	adapterInstance, err := codex.New(codex.Options{
		Config: loaded.Config.Adapters.Codex, Processes: h.options.platform.Processes(),
		Workspaces: workspaceManager, Threads: local,
	})
	if err != nil {
		return h.fail("codex_unavailable")
	}
	if _, err := adapterInstance.Detect(ctx); err != nil {
		if errors.Is(err, adapter.ErrUnsupported) {
			return h.fail("codex_unsupported")
		}
		return h.fail("codex_unavailable")
	}
	h.status.update(func(value *Status) { value.Codex = "ready" })
	threads, err := local.RuntimeThreads(ctx)
	if err != nil {
		return h.fail("recovery_unavailable")
	}
	needsRecovery := false
	for _, thread := range threads {
		if thread.State == store.RuntimeThreadNeedsReconcile {
			needsRecovery = true
			break
		}
	}
	if needsRecovery && bound {
		h.status.update(func(value *Status) { value.Recovery = "recovering" })
		runtime, err := adapterInstance.StartRuntime(ctx)
		if err != nil {
			return h.fail("recovery_unavailable")
		}
		h.runtime = runtime
		manager, err := eventlog.NewManager(local, eventlog.Options{
			OwnerID: nodeIdentity.OwnerID, NodeID: nodeIdentity.NodeID,
			MaxAge:   time.Duration(loaded.Config.Events.MaxAgeHours) * time.Hour,
			MaxBytes: int64(loaded.Config.Events.MaxSizeMiB) << 20,
		})
		if err != nil {
			return h.fail("recovery_unavailable")
		}
		report, err := manager.Reconcile(ctx, runtime)
		if err != nil {
			return h.fail("recovery_unavailable")
		}
		if err := runtime.Close(ctx); err != nil {
			return h.fail("recovery_unavailable")
		}
		h.runtime = nil
		h.status.update(func(value *Status) {
			if report.Ambiguous > 0 || report.Deferred > 0 {
				value.Recovery = "ambiguous"
			} else {
				value.Recovery = "reconciled"
			}
		})
	} else if needsRecovery {
		h.status.update(func(value *Status) { value.Recovery = "deferred_unpaired" })
	}
	if bound && loaded.Config.Transport.Mode == config.TransportRelay {
		runtime, err := adapterInstance.StartRuntime(ctx)
		if err != nil {
			return h.fail("codex_unavailable")
		}
		manager, err := eventlog.NewManager(local, eventlog.Options{
			OwnerID: nodeIdentity.OwnerID, NodeID: nodeIdentity.NodeID,
			MaxAge:   time.Duration(loaded.Config.Events.MaxAgeHours) * time.Hour,
			MaxBytes: int64(loaded.Config.Events.MaxSizeMiB) << 20,
		})
		if err != nil {
			_ = runtime.Close(context.Background())
			return h.fail("recovery_unavailable")
		}
		validator, err := protocolv1.NewValidator(protocolv1.Options{TrustStore: local, ReplayStore: local})
		if err != nil {
			_ = runtime.Close(context.Background())
			return h.fail("recovery_unavailable")
		}
		h.runtime = runtime
		h.controlEvents, h.controlValidator = manager, validator
		h.controlTarget = protocolv1.Target{OwnerID: nodeIdentity.OwnerID, NodeID: nodeIdentity.NodeID}
		h.controlName = loaded.Config.Host.Name
		if h.pairing != nil {
			if err := h.startControlSessionLocked(); err != nil {
				return h.fail("recovery_unavailable")
			}
		} else {
			h.status.update(func(value *Status) { value.RemoteControl = "unavailable" })
		}
	}
	state := "ready"
	if !bound {
		state = "unpaired"
	}
	h.status.update(func(value *Status) { value.State = state })
	h.activeConfig = loaded.Config
	h.options.trayUpdate(h.status.snapshot())
	h.log.write("node_ready", state, len(loaded.Config.Workspaces))
	return nil
}

func (h *host) reloadConfiguration(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	configurationStore, err := config.NewFileStore(h.options.configPath)
	if err != nil {
		return errors.New("node configuration is unavailable")
	}
	loaded, err := configurationStore.Load(ctx)
	if err != nil {
		return errors.New("node configuration is unavailable")
	}

	h.mu.Lock()
	if h.runtime == nil || h.controlSession == nil || h.workspaceManager == nil || h.controlEvents == nil || h.local == nil ||
		!sameRuntimeBoundary(h.activeConfig, loaded.Config) {
		h.mu.Unlock()
		return h.reload(ctx)
	}
	previous := h.activeConfig
	rollback := func() {
		_ = configurationStore.Save(context.Background(), previous)
		_ = h.workspaceManager.Reconcile(context.Background(), previous.Workspaces)
		_ = h.controlEvents.UpdateRetention(time.Duration(previous.Events.MaxAgeHours)*time.Hour, int64(previous.Events.MaxSizeMiB)<<20)
	}
	if err := h.workspaceManager.Reconcile(ctx, loaded.Config.Workspaces); err != nil {
		rollback()
		h.mu.Unlock()
		return errors.New("workspace configuration could not be applied")
	}
	if err := h.controlEvents.UpdateRetention(time.Duration(loaded.Config.Events.MaxAgeHours)*time.Hour, int64(loaded.Config.Events.MaxSizeMiB)<<20); err != nil {
		rollback()
		h.mu.Unlock()
		return errors.New("event retention could not be applied")
	}
	if !reflect.DeepEqual(previous.Relay, loaded.Config.Relay) {
		if err := h.replaceRelayLocked(ctx, loaded.Config); err != nil {
			rollback()
			h.mu.Unlock()
			return errors.New("relay configuration could not be applied")
		}
	}
	h.activeConfig = loaded.Config
	h.controlName = loaded.Config.Host.Name
	refreshTrust := func(context.Context) error { return errors.New("relay trust is unavailable") }
	if h.pairing != nil {
		refreshTrust = h.pairing.SyncTrust
	}
	h.controlSession.Reconfigure(h.controlName, refreshTrust, h.configController)
	h.status.update(func(value *Status) {
		if loaded.RecoveredFromBackup {
			value.Config = "recovered"
		} else {
			value.Config = "ready"
		}
		value.Workspaces = len(loaded.Config.Workspaces)
	})
	status := h.status.snapshot()
	h.mu.Unlock()
	h.options.trayUpdate(status)
	h.log.write("config_reloaded", "ready", len(loaded.Config.Workspaces))
	return nil
}

func sameRuntimeBoundary(left, right config.Config) bool {
	left.Workspaces = append([]config.WorkspaceConfig(nil), left.Workspaces...)
	right.Workspaces = append([]config.WorkspaceConfig(nil), right.Workspaces...)
	left.Host, right.Host = config.HostConfig{}, config.HostConfig{}
	left.Relay, right.Relay = config.RelayConfig{}, config.RelayConfig{}
	left.Events, right.Events = config.EventsConfig{}, config.EventsConfig{}
	if len(left.Workspaces) != len(right.Workspaces) {
		return false
	}
	for index := range left.Workspaces {
		left.Workspaces[index].DisplayName, right.Workspaces[index].DisplayName = "", ""
		left.Workspaces[index].PermissionProfile, right.Workspaces[index].PermissionProfile = "", ""
		left.Workspaces[index].AllowNetwork, right.Workspaces[index].AllowNetwork = false, false
	}
	return reflect.DeepEqual(left, right)
}

func relayHTTPClient(proxyValue string, timeout time.Duration, caBundleFiles ...string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxyValue != "" {
		proxyURL, err := url.Parse(proxyValue)
		if err != nil || proxyURL.Host == "" || proxyURL.User != nil {
			return nil, errors.New("relay proxy is invalid")
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	if len(caBundleFiles) > 0 && caBundleFiles[0] != "" {
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		path := filepath.Clean(caBundleFiles[0])
		info, err := os.Lstat(path)
		if err != nil || !filepath.IsAbs(path) || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 64<<10 {
			return nil, errors.New("relay CA bundle is unavailable")
		}
		raw, err := os.ReadFile(path)
		if err != nil || !roots.AppendCertsFromPEM(raw) {
			return nil, errors.New("relay CA bundle is invalid")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

func (h *host) fail(code string) error {
	h.status.update(func(value *Status) {
		value.State = "needs_attention"
		switch code {
		case "config_invalid":
			value.Config = "invalid"
		case "config_unavailable":
			value.Config = "unavailable"
		case "database_unavailable":
			value.Database = "unavailable"
		case "workspace_unavailable":
			value.Config = "workspace_invalid"
		case "identity_invalid":
			value.Identity = "invalid"
		case "identity_unavailable":
			value.Identity = "unavailable"
		case "codex_unsupported":
			value.Codex = "unsupported"
		case "codex_unavailable":
			value.Codex = "unavailable"
		case "recovery_unavailable":
			value.Recovery = "unavailable"
		case "shutdown_unavailable":
			value.State = "needs_attention"
		}
	})
	h.options.trayUpdate(h.status.snapshot())
	h.log.write("node_error", code, 0)
	return errors.New("node configuration requires attention")
}

func (h *host) close() error {
	h.mu.Lock()
	err := h.closeResourcesLocked()
	h.mu.Unlock()
	return err
}

func (h *host) closeResourcesLocked() error {
	var result error
	if h.relaySupervisor != nil {
		h.relaySupervisor.Close()
		h.relaySupervisor = nil
	}
	if h.controlSession != nil {
		if err := h.controlSession.Close(); err != nil {
			result = errors.New("control session close failed")
		}
		h.controlSession = nil
	}
	h.controlEvents, h.controlValidator = nil, nil
	h.controlTarget, h.controlName = protocolv1.Target{}, ""
	h.configController = nil
	h.activeConfig = config.Config{}
	h.workspaceManager = nil
	h.identityManager = nil
	h.nodeIdentity = identity.Identity{}
	if h.trustCancel != nil {
		h.trustCancel()
		h.trustCancel = nil
	}
	if h.pairing != nil {
		h.pairing.Close()
		h.pairing = nil
	}
	if h.joiner != nil {
		h.joiner.Close()
		h.joiner = nil
	}
	if h.runtime != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := h.runtime.Close(ctx); err != nil {
			result = errors.New("runtime close failed")
		}
		cancel()
		h.runtime = nil
	}
	if h.local != nil {
		if err := h.local.Close(); err != nil {
			result = errors.Join(result, errors.New("database close failed"))
		}
		h.local = nil
	}
	return result
}

func (h *host) handleLocalManagement(ctx context.Context, request localRequest) localResponse {
	if request.Command == "setup_status" || strings.HasPrefix(request.Command, "setup_") {
		if h.setupController == nil {
			return localResponse{Protocol: localProtocol, Error: "setup_unavailable"}
		}
		return h.setupController.handle(ctx, request)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	response := localResponse{Protocol: localProtocol}
	if request.Command == "ui_open" {
		if err := h.openControlCenter(ctx); err != nil {
			response.Error = "ui_unavailable"
		} else {
			response.OK = true
		}
		return response
	}
	if request.Command == "reload" {
		response.OK = true
		go func() { _ = h.reloadConfiguration(h.runCtx) }()
		return response
	}
	if request.Command == "copy_diagnostics" {
		response.OK = true
		return response
	}
	if request.Command == "autostart_set" {
		if request.Enabled == nil {
			response.Error = "invalid_request"
		} else if err := h.setAutostart(ctx, *request.Enabled); err != nil {
			response.Error = "autostart_failed"
		} else {
			response.OK = true
		}
		return response
	}
	if request.Command == "enrollment_join" {
		if h.joiner == nil {
			response.Error = "enrollment_unavailable"
			return response
		}
		if err := h.joiner.Join(ctx, request.JoinURL); err == nil {
			response.OK = true
		} else {
			response.Error = "enrollment_failed"
		}
		return response
	}
	if h.pairing == nil {
		switch request.Command {
		case "config_show", "config_update", "config_pending", "config_approve", "config_reject":
			// Configuration management is local and remains available even if
			// Relay trust has not been established.
		default:
			response.Error = "remote_not_available"
			return response
		}
	}
	switch request.Command {
	case "config_show":
		if h.configController == nil {
			response.Error = "config_unavailable"
			break
		}
		value, err := h.configController.Read(ctx)
		if err != nil {
			response.Error = "config_read_failed"
			break
		}
		response.OK, response.Config = true, value
	case "config_update":
		if h.configController == nil {
			response.Error = "config_unavailable"
			break
		}
		result, err := h.configController.Update(ctx, request.BaseRevision, request.Changes)
		if err != nil {
			response.Error = "config_update_failed"
			break
		}
		response.OK, response.Config = true, result.Payload
		if result.Reload {
			go func() { _ = h.reloadConfiguration(h.runCtx) }()
		}
	case "config_pending":
		if h.configController == nil {
			response.Error = "config_unavailable"
			break
		}
		changes, err := h.configController.Pending(ctx)
		if err != nil {
			response.Error = "config_pending_failed"
			break
		}
		response.OK = true
		for _, change := range changes {
			response.ConfigChanges = append(response.ConfigChanges, configChangeSummary(change, h.activeConfig))
		}
	case "config_approve":
		if h.configController == nil {
			response.Error = "config_unavailable"
			break
		}
		result, err := h.configController.Approve(ctx, request.ChangeID)
		if err != nil {
			response.Error = "config_approve_failed"
			break
		}
		response.OK = true
		if result.Reload {
			go func() { _ = h.reloadConfiguration(h.runCtx) }()
		}
	case "config_reject":
		if h.configController == nil {
			response.Error = "config_unavailable"
			break
		}
		if err := h.configController.Reject(ctx, request.ChangeID); err != nil {
			response.Error = "config_reject_failed"
			break
		}
		response.OK = true
	case "pairing_create":
		value, err := h.pairing.Create(ctx)
		if err == nil {
			response.OK, response.PairingURL = true, value
		} else {
			response.Error = "pairing_failed"
		}
	case "pairing_list":
		value, err := h.pairing.Pending(ctx)
		if err == nil {
			response.OK, response.Pairings = true, value
		} else {
			response.Error = "pairing_failed"
		}
	case "pairing_accept", "pairing_decline":
		decision := "accept"
		if request.Command == "pairing_decline" {
			decision = "decline"
		}
		if err := h.pairing.Decide(ctx, request.PairingID, decision); err == nil {
			response.OK = true
		} else {
			response.Error = "pairing_failed"
		}
	case "client_list":
		value, err := h.pairing.Clients(ctx)
		if err == nil {
			response.OK, response.Clients = true, value
		} else {
			response.Error = "client_failed"
		}
	case "client_revoke":
		if err := h.pairing.Revoke(ctx, request.ClientID, request.KeyID); err == nil {
			response.OK = true
		} else {
			response.Error = "client_failed"
		}
	case "credential_rotate":
		if err := h.pairing.RotateCredential(ctx); err == nil {
			if h.relaySupervisor != nil {
				h.status.update(func(value *Status) { value.RemoteControl = "reconnecting" })
				h.relaySupervisor.Reconnect()
				response.OK = true
			} else {
				response.Error = "rotation_failed"
			}
		} else {
			response.Error = "rotation_failed"
		}
	case "enrollment_create":
		value, err := h.pairing.CreateNodeEnrollment(ctx)
		if err == nil {
			response.OK, response.EnrollmentURL = true, value
		} else {
			response.Error = "enrollment_failed"
		}
	case "enrollment_list":
		value, err := h.pairing.PendingNodeEnrollments(ctx)
		if err == nil {
			response.OK, response.Enrollments = true, value
		} else {
			response.Error = "enrollment_failed"
		}
	case "enrollment_accept", "enrollment_decline":
		decision := "accept"
		if request.Command == "enrollment_decline" {
			decision = "decline"
		}
		if err := h.pairing.DecideNodeEnrollment(ctx, request.EnrollmentID, decision); err == nil {
			response.OK = true
		} else {
			response.Error = "enrollment_failed"
		}
	case "device_list":
		value, err := h.pairing.Devices(ctx)
		if err == nil {
			response.OK, response.Devices = true, value
		} else {
			response.Error = "device_failed"
		}
	case "device_revoke":
		if err := h.pairing.RevokeNode(ctx, request.NodeID); err == nil {
			response.OK = true
		} else {
			response.Error = "device_failed"
		}
	default:
		response.Error = "unsupported_command"
	}
	return response
}

func (h *host) startControlSessionLocked() error {
	if h.pairing == nil || h.runtime == nil || h.controlEvents == nil || h.controlValidator == nil || h.local == nil || h.runCtx == nil {
		return errors.New("node control session is unavailable")
	}
	if h.controlSession != nil || h.relaySupervisor != nil {
		return nil
	}
	session, err := NewControlSession(ControlSessionOptions{
		Validator: h.controlValidator, Target: h.controlTarget,
		Events: h.controlEvents, Store: h.local, Runtime: h.runtime, DeviceName: h.controlName, RefreshTrust: h.pairing.SyncTrust,
		EventFailure: func(error) { go h.handleEventFailure() },
		Config:       h.configController,
		ConfigReload: func() { _ = h.reloadConfiguration(h.runCtx) },
	})
	if err != nil {
		return err
	}
	if err := session.Start(h.runCtx); err != nil {
		return err
	}
	h.controlSession = session
	supervisor, err := h.newRelaySupervisorLocked(h.pairing)
	if err != nil {
		h.controlSession = nil
		_ = session.Close()
		return err
	}
	h.relaySupervisor = supervisor
	supervisor.Start()
	return nil
}

func (h *host) newRelaySupervisorLocked(manager *pairingManager) (*relaySupervisor, error) {
	if manager == nil || h.controlSession == nil {
		return nil, errors.New("node relay supervisor is unavailable")
	}
	return newRelaySupervisor(h.runCtx, relaySupervisorOptions{
		Connect: manager.Connect,
		Serve:   h.controlSession.Serve,
		OnState: func(value string) {
			h.status.update(func(status *Status) {
				status.RemoteControl = value
				status.RelayLastSeen = time.Now().UTC().Format(time.RFC3339Nano)
				if value == relayStateRevoked {
					status.RelayLastError = "credential_revoked"
				}
			})
			h.options.trayUpdate(h.status.snapshot())
		},
	})
}

func (h *host) replaceRelayLocked(ctx context.Context, configuration config.Config) error {
	if h.identityManager == nil || h.local == nil || h.controlSession == nil || h.nodeIdentity.OwnerID == "" || h.nodeIdentity.NodeID == "" {
		return errors.New("node relay identity is unavailable")
	}
	credential, err := h.options.platform.SecureStore().Get(ctx, configuration.Relay.CredentialRef)
	if err != nil {
		return err
	}
	httpClient, err := relayHTTPClient(configuration.Relay.ProxyURL, time.Duration(configuration.Relay.ConnectTimeoutSeconds)*time.Second, configuration.Relay.CABundleFile)
	if err != nil {
		clear(credential)
		return err
	}
	manager, err := newPairingManager(pairingManagerOptions{
		RelayURL: configuration.Relay.URL, Timeout: time.Duration(configuration.Relay.ConnectTimeoutSeconds) * time.Second,
		HTTPClient: httpClient, Identity: h.nodeIdentity, Signer: h.identityManager, Local: h.local,
		Secrets: h.options.platform.SecureStore(), CredentialRef: configuration.Relay.CredentialRef, Credential: credential,
	})
	clear(credential)
	if err != nil {
		return err
	}
	_ = manager.SyncTrust(ctx)
	h.controlSession.Reconfigure(configuration.Host.Name, manager.SyncTrust, h.configController)
	supervisor, err := h.newRelaySupervisorLocked(manager)
	if err != nil {
		manager.Close()
		return err
	}
	if h.relaySupervisor != nil {
		h.relaySupervisor.Close()
	}
	if h.trustCancel != nil {
		h.trustCancel()
	}
	if h.pairing != nil {
		h.pairing.Close()
	}
	h.pairing = manager
	h.relaySupervisor = supervisor
	h.startTrustSyncLocked(manager)
	supervisor.Start()
	return nil
}

func (h *host) startTrustSyncLocked(manager *pairingManager) {
	trustCtx, trustCancel := context.WithCancel(h.runCtx)
	h.trustCancel = trustCancel
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-trustCtx.Done():
				return
			case <-ticker.C:
				_ = manager.SyncTrust(trustCtx)
			}
		}
	}()
}

func (h *host) handleEventFailure() {
	h.mu.Lock()
	if h.relaySupervisor != nil {
		h.relaySupervisor.Close()
		h.relaySupervisor = nil
	}
	h.mu.Unlock()
	h.status.update(func(value *Status) { value.RemoteControl, value.RelayLastError = "unavailable", "eventlog_failure" })
	h.options.trayUpdate(h.status.snapshot())
	h.log.write("node_error", "eventlog_failure", 0)
}

func (o runOptions) trayUpdate(status Status) {
	if o.tray != nil {
		o.tray.Update(status)
	}
}

func (h *host) refreshAutostart(ctx context.Context) {
	state := "unavailable"
	if manager := h.options.platform.Autostart(); manager != nil && manager.Available() {
		if status, err := manager.Status(ctx, autostartID); err == nil {
			if status.Installed {
				state = "enabled"
			} else {
				state = "disabled"
			}
		}
	}
	h.status.update(func(value *Status) { value.Autostart = state })
}

func (h *host) setAutostart(ctx context.Context, enabled bool) error {
	manager := h.options.platform.Autostart()
	if manager == nil || !manager.Available() {
		return platform.ErrUnavailable
	}
	if !enabled {
		err := manager.Remove(ctx, autostartID)
		if errors.Is(err, platform.ErrNotFound) {
			err = nil
		}
		h.refreshAutostart(ctx)
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return platform.ErrUnavailable
	}
	arguments := []string{"node", "--background"}
	if h.options.configPath != h.options.paths.config {
		arguments = append(arguments, "--config", h.options.configPath)
	}
	err = manager.Install(ctx, platform.AutostartEntry{ID: autostartID, Executable: executable, Args: arguments})
	h.refreshAutostart(ctx)
	return err
}
