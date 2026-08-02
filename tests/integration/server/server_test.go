package server_integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/server"
	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
)

const serverStartupTimeout = 30 * time.Second

type captureWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	ready  chan struct{}
}

func newCaptureWriter() *captureWriter { return &captureWriter{ready: make(chan struct{}, 1)} }

func (w *captureWriter) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written, err := w.buffer.Write(value)
	select {
	case w.ready <- struct{}{}:
	default:
	}
	return written, err
}

func (w *captureWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func TestFormalServerBootstrapPersistsAcrossRestart(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "server-data")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	output := newCaptureWriter()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- server.Run(ctx, server.Options{DataDir: dataDir, Listen: listener.Addr().String(), Listener: listener, Stdout: output})
	}()
	select {
	case <-output.ready:
	case <-time.After(serverStartupTimeout):
		cancel()
		t.Fatal("bootstrap secret was not displayed")
	}
	line := strings.TrimSpace(output.String())
	marker := "shown once): "
	index := strings.Index(line, marker)
	if index < 0 {
		cancel()
		t.Fatalf("unexpected bootstrap output")
	}
	secret := line[index+len(marker):]
	if decoded, err := base64.RawURLEncoding.DecodeString(secret); err != nil || len(decoded) != 32 {
		cancel()
		t.Fatal("bootstrap secret format is invalid")
	}
	baseURL := "http://" + listener.Addr().String()
	waitReady(t, baseURL)
	credentialCanary := "node-connection-credential-canary"
	credentialHash := sha256.Sum256([]byte(credentialCanary))
	claim := server.ClaimRequest{
		RequestID: "integration-request", Name: "Integration Node", OS: "windows", Version: "dev",
		PublicKey:      base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x51}, 32)),
		CredentialHash: base64.RawURLEncoding.EncodeToString(credentialHash[:]),
	}
	body, _ := json.Marshal(claim)
	request, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/bootstrap/claim", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusCreated || bytes.Contains(responseBody, []byte(secret)) || bytes.Contains(responseBody, []byte(credentialCanary)) {
		cancel()
		t.Fatalf("claim status=%d", response.StatusCode)
	}

	secondListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	secondErr := server.Run(context.Background(), server.Options{DataDir: dataDir, Listen: secondListener.Addr().String(), Listener: secondListener, Stdout: io.Discard})
	_ = secondListener.Close()
	if secondErr == nil || strings.Contains(secondErr.Error(), dataDir) {
		cancel()
		t.Fatalf("second instance error=%v", secondErr)
	}

	cancel()
	if err := <-result; err != nil {
		t.Fatalf("first server shutdown=%v", err)
	}
	databaseBytes, err := os.ReadFile(filepath.Join(dataDir, "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(databaseBytes, []byte(secret)) || bytes.Contains(databaseBytes, []byte(credentialCanary)) {
		t.Fatal("raw bootstrap or connection credential persisted")
	}

	restartListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	restartOutput := newCaptureWriter()
	restartCtx, restartCancel := context.WithCancel(context.Background())
	restarted := make(chan error, 1)
	go func() {
		restarted <- server.Run(restartCtx, server.Options{DataDir: dataDir, Listen: restartListener.Addr().String(), Listener: restartListener, Stdout: restartOutput})
	}()
	restartURL := "http://" + restartListener.Addr().String()
	waitReady(t, restartURL)
	statusResponse, err := http.Get(restartURL + "/v1/bootstrap/status")
	if err != nil {
		restartCancel()
		t.Fatal(err)
	}
	statusBody, _ := io.ReadAll(statusResponse.Body)
	_ = statusResponse.Body.Close()
	if statusResponse.StatusCode != http.StatusOK || !bytes.Contains(statusBody, []byte("completed")) || restartOutput.String() != "" {
		restartCancel()
		t.Fatalf("restart status=%d output-len=%d", statusResponse.StatusCode, len(restartOutput.String()))
	}
	restartCancel()
	if err := <-restarted; err != nil {
		t.Fatalf("restart shutdown=%v", err)
	}
	local, err := serverstore.Open(context.Background(), filepath.Join(dataDir, "server.db"), serverstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	nodes, err := local.Nodes(context.Background())
	if err != nil || len(nodes) != 1 || nodes[0].Name != "Integration Node" {
		t.Fatalf("persisted nodes=%+v err=%v", nodes, err)
	}
}

func waitReady(t *testing.T, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(serverStartupTimeout)
	for time.Now().Before(deadline) {
		response, err := http.Get(baseURL + "/readyz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not become ready")
}
