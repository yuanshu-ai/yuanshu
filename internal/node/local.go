package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

const (
	localProtocol = "node-local-v1"
	localMaxBytes = 64 << 10
	localMaxPeers = 8
	localDeadline = 5 * time.Second
	localIPCName  = platform.IPCName("node-control-v1")
)

type localRequest struct {
	Protocol     string         `json:"protocol"`
	Command      string         `json:"command"`
	PairingID    string         `json:"pairingId,omitempty"`
	ClientID     string         `json:"clientId,omitempty"`
	KeyID        string         `json:"keyId,omitempty"`
	EnrollmentID string         `json:"enrollmentId,omitempty"`
	NodeID       string         `json:"nodeId,omitempty"`
	JoinURL      string         `json:"joinUrl,omitempty"`
	ChangeID     string         `json:"changeId,omitempty"`
	Enabled      *bool          `json:"enabled,omitempty"`
	BaseRevision string         `json:"baseRevision,omitempty"`
	Changes      map[string]any `json:"changes,omitempty"`
}

type localResponse struct {
	Protocol      string                    `json:"protocol"`
	OK            bool                      `json:"ok"`
	Error         string                    `json:"error,omitempty"`
	Status        *Status                   `json:"status,omitempty"`
	PairingURL    string                    `json:"pairingUrl,omitempty"`
	Pairings      []PairingCandidate        `json:"pairings,omitempty"`
	Clients       []TrustedClientSummary    `json:"clients,omitempty"`
	EnrollmentURL string                    `json:"enrollmentUrl,omitempty"`
	Enrollments   []NodeEnrollmentCandidate `json:"enrollments,omitempty"`
	Devices       []DeviceSummary           `json:"devices,omitempty"`
	Config        map[string]any            `json:"config,omitempty"`
	ConfigChanges []ConfigChangeSummary     `json:"configChanges,omitempty"`
}

type localServer struct {
	listener net.Listener
	status   func() Status
	stop     context.CancelFunc
	manage   func(context.Context, localRequest) localResponse
	done     chan struct{}
	once     sync.Once
}

func startLocalServer(ctx context.Context, ipc platform.LocalIPC, status func() Status, stop context.CancelFunc, management ...func(context.Context, localRequest) localResponse) (*localServer, error) {
	if ipc == nil || !ipc.Available() || status == nil || stop == nil {
		return nil, platform.ErrUnavailable
	}
	listener, err := ipc.Listen(ctx, localIPCName)
	if err != nil {
		return nil, err
	}
	server := &localServer{listener: listener, status: status, stop: stop, done: make(chan struct{})}
	if len(management) > 0 {
		server.manage = management[0]
	}
	go server.serve(ctx)
	return server, nil
}

func (s *localServer) serve(ctx context.Context) {
	defer close(s.done)
	peers := make(chan struct{}, localMaxPeers)
	var workers sync.WaitGroup
	defer workers.Wait()
	go func() {
		<-ctx.Done()
		_ = s.listener.Close()
	}()
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		select {
		case peers <- struct{}{}:
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { <-peers }()
				s.handle(connection)
			}()
		default:
			_ = writeLocalResponse(connection, localResponse{Protocol: localProtocol, Error: "busy"})
			_ = connection.Close()
		}
	}
}

func (s *localServer) handle(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(localDeadline))
	reader := bufio.NewReader(io.LimitReader(connection, localMaxBytes+1))
	line, err := reader.ReadBytes('\n')
	if err != nil || len(line) > localMaxBytes {
		_ = writeLocalResponse(connection, localResponse{Protocol: localProtocol, Error: "invalid_request"})
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var request localRequest
	if err := decoder.Decode(&request); err != nil || request.Protocol != localProtocol {
		_ = writeLocalResponse(connection, localResponse{Protocol: localProtocol, Error: "invalid_request"})
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		_ = writeLocalResponse(connection, localResponse{Protocol: localProtocol, Error: "invalid_request"})
		return
	}
	switch request.Command {
	case "status":
		status := s.status()
		_ = writeLocalResponse(connection, localResponse{Protocol: localProtocol, OK: true, Status: &status})
	case "stop":
		_ = writeLocalResponse(connection, localResponse{Protocol: localProtocol, OK: true})
		s.stop()
	default:
		if s.manage == nil {
			_ = writeLocalResponse(connection, localResponse{Protocol: localProtocol, Error: "unsupported_command"})
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), localDeadline)
		defer cancel()
		_ = writeLocalResponse(connection, s.manage(ctx, request))
	}
}

func writeLocalResponse(writer io.Writer, response localResponse) error {
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) > localMaxBytes {
		return errors.New("local response unavailable")
	}
	_, err = writer.Write(append(encoded, '\n'))
	return err
}

func callLocal(ctx context.Context, ipc platform.LocalIPC, command string) (localResponse, error) {
	return callLocalRequest(ctx, ipc, localRequest{Protocol: localProtocol, Command: command})
}

func callLocalRequest(ctx context.Context, ipc platform.LocalIPC, request localRequest) (localResponse, error) {
	if ipc == nil || !ipc.Available() {
		return localResponse{}, platform.ErrUnavailable
	}
	connection, err := ipc.Dial(ctx, localIPCName)
	if err != nil {
		return localResponse{}, err
	}
	defer connection.Close()
	deadline := time.Now().Add(localDeadline)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	_ = connection.SetDeadline(deadline)
	request.Protocol = localProtocol
	encoded, _ := json.Marshal(request)
	if _, err := connection.Write(append(encoded, '\n')); err != nil {
		return localResponse{}, errors.New("local node is unavailable")
	}
	reader := bufio.NewReader(io.LimitReader(connection, localMaxBytes+1))
	line, err := reader.ReadBytes('\n')
	if err != nil || len(line) > localMaxBytes {
		return localResponse{}, errors.New("local node response is unavailable")
	}
	var response localResponse
	if json.Unmarshal(line, &response) != nil || response.Protocol != localProtocol {
		return localResponse{}, errors.New("local node response is invalid")
	}
	return response, nil
}

func (s *localServer) Close() error {
	s.once.Do(func() { _ = s.listener.Close() })
	<-s.done
	return nil
}
