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
	Protocol string `json:"protocol"`
	Command  string `json:"command"`
}

type localResponse struct {
	Protocol string  `json:"protocol"`
	OK       bool    `json:"ok"`
	Error    string  `json:"error,omitempty"`
	Status   *Status `json:"status,omitempty"`
}

type localServer struct {
	listener net.Listener
	status   func() Status
	stop     context.CancelFunc
	done     chan struct{}
	once     sync.Once
}

func startLocalServer(ctx context.Context, ipc platform.LocalIPC, status func() Status, stop context.CancelFunc) (*localServer, error) {
	if ipc == nil || !ipc.Available() || status == nil || stop == nil {
		return nil, platform.ErrUnavailable
	}
	listener, err := ipc.Listen(ctx, localIPCName)
	if err != nil {
		return nil, err
	}
	server := &localServer{listener: listener, status: status, stop: stop, done: make(chan struct{})}
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
		_ = writeLocalResponse(connection, localResponse{Protocol: localProtocol, Error: "unsupported_command"})
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
	request, _ := json.Marshal(localRequest{Protocol: localProtocol, Command: command})
	if _, err := connection.Write(append(request, '\n')); err != nil {
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
