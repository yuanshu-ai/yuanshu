package node

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/node/identity"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

type StandaloneManagementOptions struct {
	IPC              platform.LocalIPC
	RelayURL         string
	Identity         identity.Identity
	Signer           *identity.Manager
	Local            *store.Store
	SessionToken     []byte
	SessionExpiresAt time.Time
	Stop             context.CancelFunc
}

type StandaloneManagement struct {
	server  *localServer
	pairing *pairingManager
	once    sync.Once
}

func StartStandaloneManagement(ctx context.Context, options StandaloneManagementOptions) (*StandaloneManagement, error) {
	if ctx == nil || options.Stop == nil || options.IPC == nil || !options.IPC.Available() || options.Identity.OwnerID == "" || options.Identity.NodeID == "" || options.Signer == nil || options.Local == nil || len(options.SessionToken) != 32 || !options.SessionExpiresAt.After(time.Now().UTC()) {
		return nil, errors.New("standalone local management is unavailable")
	}
	pairing, err := newPairingManager(pairingManagerOptions{
		RelayURL: options.RelayURL, Identity: options.Identity, Signer: options.Signer, Local: options.Local,
		SessionToken: options.SessionToken, SessionExpiresAt: options.SessionExpiresAt,
	})
	if err != nil {
		return nil, err
	}
	management := &StandaloneManagement{pairing: pairing}
	status := func() Status {
		return Status{Version: LocalStatusVersion, State: "ready", Platform: string(platform.FamilyLinux), Config: "ready", Identity: "bound", IdentityStorage: "local_file", Database: "ready", Codex: "ready", Authentication: "available", NodeAuthentication: "device_signature", Recovery: "not_required", RemoteControl: "local", Autostart: "not_available"}
	}
	server, err := startLocalServer(ctx, options.IPC, status, options.Stop, management.handle)
	if err != nil {
		pairing.Close()
		return nil, err
	}
	management.server = server
	return management, nil
}

func (m *StandaloneManagement) handle(ctx context.Context, request localRequest) localResponse {
	response := localResponse{Protocol: localProtocol}
	switch request.Command {
	case "pairing_create":
		value, err := m.pairing.Create(ctx)
		response.OK, response.PairingURL = err == nil, value
		if err != nil {
			response.Error = "pairing_failed"
		}
	case "pairing_list":
		value, err := m.pairing.Pending(ctx)
		response.OK, response.Pairings = err == nil, value
		if err != nil {
			response.Error = "pairing_failed"
		}
	case "pairing_accept", "pairing_decline":
		decision := "accept"
		if request.Command == "pairing_decline" {
			decision = "decline"
		}
		response.OK = m.pairing.Decide(ctx, request.PairingID, decision) == nil
		if !response.OK {
			response.Error = "pairing_failed"
		}
	case "client_list":
		value, err := m.pairing.Clients(ctx)
		response.OK, response.Clients = err == nil, value
		if err != nil {
			response.Error = "client_failed"
		}
	case "client_revoke":
		response.OK = m.pairing.Revoke(ctx, request.ClientID, request.KeyID) == nil
		if !response.OK {
			response.Error = "client_failed"
		}
	case "session_refresh":
		response.OK = m.pairing.RotateCredential(ctx) == nil
		if !response.OK {
			response.Error = "rotation_failed"
		}
	case "enrollment_create":
		value, err := m.pairing.CreateNodeEnrollment(ctx)
		response.OK, response.EnrollmentURL = err == nil, value
		if err != nil {
			response.Error = "enrollment_failed"
		}
	case "enrollment_list":
		value, err := m.pairing.PendingNodeEnrollments(ctx)
		response.OK, response.Enrollments = err == nil, value
		if err != nil {
			response.Error = "enrollment_failed"
		}
	case "enrollment_accept", "enrollment_decline":
		decision := "accept"
		if request.Command == "enrollment_decline" {
			decision = "decline"
		}
		response.OK = m.pairing.DecideNodeEnrollment(ctx, request.EnrollmentID, decision) == nil
		if !response.OK {
			response.Error = "enrollment_failed"
		}
	case "device_list":
		value, err := m.pairing.Devices(ctx)
		response.OK, response.Devices = err == nil, value
		if err != nil {
			response.Error = "device_failed"
		}
	case "device_revoke":
		response.OK = m.pairing.RevokeNode(ctx, request.NodeID) == nil
		if !response.OK {
			response.Error = "device_failed"
		}
	default:
		response.Error = "unsupported_command"
	}
	return response
}

func (m *StandaloneManagement) Close() error {
	var result error
	m.once.Do(func() {
		if m.server != nil {
			result = m.server.Close()
		}
		if m.pairing != nil {
			m.pairing.Close()
		}
	})
	return result
}
