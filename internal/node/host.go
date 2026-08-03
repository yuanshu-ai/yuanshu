package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	paths      paths
	configPath string
	background bool
	platform   platform.Platform
	tray       tray
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
	h := &host{options: options, status: newStatusStore(string(options.platform.Family())), log: newOperationalLog(options.paths.log), runCtx: runCtx}
	h.refreshAutostart(runCtx)
	server, err := startLocalServer(runCtx, options.platform.IPC(), h.status.snapshot, cancel, h.handleLocalManagement)
	if err != nil {
		return errors.New("node local management is unavailable")
	}
	defer server.Close()
	h.log.write("node_starting", "starting", 0)
	_ = h.reload(runCtx)
	if options.tray == nil {
		options.tray = newPlatformTray(options.background)
		h.options.tray = options.tray
	}
	trayErrors := make(chan error, 1)
	go func() { trayErrors <- options.tray.Run(runCtx, h.trayCallbacks(cancel)) }()
	options.tray.Update(h.status.snapshot())
	trayReturned := false
	var trayErr error
	select {
	case <-runCtx.Done():
	case trayErr = <-trayErrors:
		trayReturned = true
		if trayErr != nil {
			cancel()
			_ = h.close()
			return errors.New("node tray is unavailable")
		}
	}
	cancel()
	closeErr := h.close()
	if !trayReturned {
		select {
		case trayErr = <-trayErrors:
			if trayErr != nil && !errors.Is(trayErr, context.Canceled) {
				return errors.New("node tray shutdown failed")
			}
		case <-time.After(5 * time.Second):
			return errors.New("node tray shutdown timed out")
		}
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
	workspaceManager, err := workspace.NewManager(h.options.platform.Workspaces(), local)
	if err != nil || workspaceManager.Reconcile(ctx, loaded.Config.Workspaces) != nil {
		return h.fail("workspace_unavailable")
	}
	h.status.update(func(value *Status) { value.Workspaces = len(loaded.Config.Workspaces) })
	identityManager, err := identity.NewManager(local, h.options.platform.SecureStore(), loaded.Config.Identity.PrivateKeyRef, identity.Options{})
	if err != nil {
		return h.fail("identity_invalid")
	}
	nodeIdentity, err := identityManager.Ensure(ctx)
	if err != nil {
		return h.fail("identity_unavailable")
	}
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
			manager, managerErr := newPairingManager(pairingManagerOptions{
				RelayURL: loaded.Config.Relay.URL, Timeout: time.Duration(loaded.Config.Relay.ConnectTimeoutSeconds) * time.Second,
				Identity: nodeIdentity, Signer: identityManager, Local: local, Secrets: h.options.platform.SecureStore(),
				CredentialRef: loaded.Config.Relay.CredentialRef, Credential: credential,
			})
			clear(credential)
			if managerErr == nil {
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
	h.options.trayUpdate(h.status.snapshot())
	h.log.write("node_ready", state, len(loaded.Config.Workspaces))
	return nil
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
	h.mu.Lock()
	defer h.mu.Unlock()
	response := localResponse{Protocol: localProtocol}
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
		response.Error = "remote_not_available"
		return response
	}
	switch request.Command {
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
	var supervisor *relaySupervisor
	session, err := NewControlSession(ControlSessionOptions{
		Validator: h.controlValidator, Target: h.controlTarget,
		Events: h.controlEvents, Store: h.local, Runtime: h.runtime, DeviceName: h.controlName, RefreshTrust: h.pairing.SyncTrust,
		EventFailure: func(error) {
			if supervisor != nil {
				supervisor.Close()
			}
			h.status.update(func(value *Status) { value.RemoteControl, value.RelayLastError = "unavailable", "eventlog_failure" })
			h.options.trayUpdate(h.status.snapshot())
			h.log.write("node_error", "eventlog_failure", 0)
		},
	})
	if err != nil {
		return err
	}
	if err := session.Start(h.runCtx); err != nil {
		return err
	}
	supervisor, err = newRelaySupervisor(h.runCtx, relaySupervisorOptions{
		Connect: h.pairing.Connect,
		Serve:   session.Serve,
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
	if err != nil {
		_ = session.Close()
		return err
	}
	h.controlSession = session
	h.relaySupervisor = supervisor
	supervisor.Start()
	return nil
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
