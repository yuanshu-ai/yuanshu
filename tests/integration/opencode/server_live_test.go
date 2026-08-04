package opencode_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	opencodeLiveEnvironment   = "YUANSHU_OPENCODE_SPIKE_LIVE"
	opencodeBinaryEnvironment = "YUANSHU_OPENCODE_BIN"
	opencodePinnedVersion     = "1.18.13"
)

// TestOpenCodeServerLiveWithoutModel is the bounded PF-087 live gate. It
// verifies only OpenCode's authenticated local structured server surface. It
// neither creates a session nor invokes a provider or model.
func TestOpenCodeServerLiveWithoutModel(t *testing.T) {
	if os.Getenv(opencodeLiveEnvironment) != "1" {
		t.Skip("set YUANSHU_OPENCODE_SPIKE_LIVE=1 to run the zero-model OpenCode server probe")
	}
	binary := os.Getenv(opencodeBinaryEnvironment)
	if binary == "" {
		t.Fatal("YUANSHU_OPENCODE_BIN is required")
	}
	absoluteBinary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal("OpenCode binary path is invalid")
	}
	if info, err := os.Stat(absoluteBinary); err != nil || !info.Mode().IsRegular() {
		t.Fatal("OpenCode binary is unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	versionCommand := exec.CommandContext(ctx, absoluteBinary, "--version")
	versionCommand.Env = isolatedEnvironment(t.TempDir(), "unused")
	versionOutput, err := versionCommand.Output()
	if err != nil || strings.TrimSpace(string(versionOutput)) != opencodePinnedVersion {
		t.Fatal("OpenCode version is not the PF-087 pinned baseline")
	}

	address := reserveLoopbackAddress(t)
	password := randomPassword(t)
	root := t.TempDir()
	command := exec.CommandContext(ctx, absoluteBinary,
		"serve", "--hostname", "127.0.0.1", "--port", portOf(address),
		"--pure", "--log-level", "ERROR",
	)
	command.Dir = root
	command.Env = isolatedEnvironment(root, password)
	if err := command.Start(); err != nil {
		t.Fatal("OpenCode server did not start")
	}
	defer stopProcess(t, command)

	client := &http.Client{Timeout: 3 * time.Second}
	baseURL := "http://" + address
	waitForServer(t, client, baseURL)

	badRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/global/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	badRequest.SetBasicAuth("opencode", "wrong-password")
	badResponse, err := client.Do(badRequest)
	if err != nil {
		t.Fatal("OpenCode authentication check failed")
	}
	closeBounded(badResponse.Body)
	if badResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong credentials status=%d", badResponse.StatusCode)
	}

	health := authenticatedGet(t, ctx, client, baseURL+"/global/health", password)
	if health.StatusCode != http.StatusOK {
		closeBounded(health.Body)
		t.Fatalf("health status=%d", health.StatusCode)
	}
	var healthBody struct {
		Healthy bool   `json:"healthy"`
		Version string `json:"version"`
	}
	decodeBoundedJSON(t, health.Body, &healthBody)
	if !healthBody.Healthy || healthBody.Version != opencodePinnedVersion {
		t.Fatal("OpenCode health response does not match the pinned server")
	}

	document := authenticatedGet(t, ctx, client, baseURL+"/doc", password)
	if document.StatusCode != http.StatusOK {
		closeBounded(document.Body)
		t.Fatalf("OpenAPI document status=%d", document.StatusCode)
	}
	documentBody, err := io.ReadAll(io.LimitReader(document.Body, 1<<20))
	closeBounded(document.Body)
	if err != nil || len(documentBody) == 0 || !bytesContainFold(documentBody, "openapi") {
		t.Fatal("OpenCode OpenAPI document is unavailable")
	}

	sessions := authenticatedGet(t, ctx, client, baseURL+"/session", password)
	if sessions.StatusCode != http.StatusOK {
		closeBounded(sessions.Body)
		t.Fatalf("session list status=%d", sessions.StatusCode)
	}
	var sessionList []json.RawMessage
	decodeBoundedJSON(t, sessions.Body, &sessionList)
	if len(sessionList) != 0 {
		t.Fatal("isolated OpenCode server unexpectedly contains sessions")
	}

	sseClient := &http.Client{}
	sseContext, stopSSE := context.WithTimeout(ctx, 5*time.Second)
	sseRequest, err := http.NewRequestWithContext(sseContext, http.MethodGet, baseURL+"/global/event", nil)
	if err != nil {
		stopSSE()
		t.Fatal(err)
	}
	sseRequest.SetBasicAuth("opencode", password)
	sseResponse, err := sseClient.Do(sseRequest)
	if err != nil {
		stopSSE()
		t.Fatal("OpenCode SSE connection failed")
	}
	if sseResponse.StatusCode != http.StatusOK || !strings.HasPrefix(sseResponse.Header.Get("Content-Type"), "text/event-stream") {
		closeBounded(sseResponse.Body)
		stopSSE()
		t.Fatal("OpenCode SSE surface is unavailable")
	}
	line, err := readBoundedSSELine(sseResponse.Body)
	_ = sseResponse.Body.Close()
	stopSSE()
	if err != nil || line == "" {
		t.Fatal("OpenCode SSE did not produce a bounded structured event")
	}

	afterDisconnect := authenticatedGet(t, ctx, client, baseURL+"/global/health", password)
	closeBounded(afterDisconnect.Body)
	if afterDisconnect.StatusCode != http.StatusOK {
		t.Fatal("disconnecting an SSE client terminated the OpenCode server")
	}
	t.Log("PF-087 zero-model surface: opencode=1.18.13 auth=true openapi=true sessions=0 sse=true client_disconnect_safe=true")
}

func isolatedEnvironment(root, password string) []string {
	drop := []string{
		"ANTHROPIC", "OPENAI", "GEMINI", "GOOGLE_API", "AZURE", "AWS_", "BEDROCK", "VERTEX",
		"OPENCODE", "XDG_", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA",
		"API_KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL",
	}
	environment := make([]string, 0, len(os.Environ())+10)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		rejected := false
		for _, prefix := range drop {
			if strings.Contains(upper, prefix) {
				rejected = true
				break
			}
		}
		if !rejected {
			environment = append(environment, entry)
		}
	}
	config := filepath.Join(root, "config")
	data := filepath.Join(root, "data")
	cache := filepath.Join(root, "cache")
	state := filepath.Join(root, "state")
	home := filepath.Join(root, "home")
	for _, directory := range []string{config, data, cache, state, home} {
		_ = os.MkdirAll(directory, 0o700)
	}
	return append(environment,
		"XDG_CONFIG_HOME="+config,
		"XDG_DATA_HOME="+data,
		"XDG_CACHE_HOME="+cache,
		"XDG_STATE_HOME="+state,
		"HOME="+home,
		"USERPROFILE="+home,
		"APPDATA="+data,
		"LOCALAPPDATA="+data,
		"OPENCODE_SERVER_USERNAME=opencode",
		"OPENCODE_SERVER_PASSWORD="+password,
	)
}

func reserveLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("cannot reserve a loopback port")
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal("cannot release the loopback port")
	}
	return address
}

func portOf(address string) string {
	_, port, _ := net.SplitHostPort(address)
	return port
}

func randomPassword(t *testing.T) string {
	t.Helper()
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		t.Fatal("cannot generate the local server password")
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

func waitForServer(t *testing.T, client *http.Client, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequest(http.MethodGet, baseURL+"/global/health", nil)
		response, err := client.Do(request)
		if err == nil {
			closeBounded(response.Body)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("OpenCode server did not become ready")
}

func authenticatedGet(t *testing.T, ctx context.Context, client *http.Client, target, password string) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("opencode", password)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("OpenCode request failed")
	}
	return response
}

func decodeBoundedJSON(t *testing.T, body io.ReadCloser, target any) {
	t.Helper()
	defer closeBounded(body)
	decoder := json.NewDecoder(io.LimitReader(body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		t.Fatal("OpenCode returned invalid bounded JSON")
	}
}

func readBoundedSSELine(body io.Reader) (string, error) {
	scanner := bufio.NewScanner(io.LimitReader(body, 1<<20))
	scanner.Buffer(make([]byte, 1024), 64<<10)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			return line, nil
		}
	}
	return "", scanner.Err()
}

func bytesContainFold(value []byte, substring string) bool {
	return strings.Contains(strings.ToLower(string(value)), strings.ToLower(substring))
}

func closeBounded(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4<<10))
	_ = body.Close()
}

func stopProcess(t *testing.T, command *exec.Cmd) {
	t.Helper()
	if command.Process == nil {
		return
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	if runtime.GOOS != "windows" {
		_ = command.Process.Signal(os.Interrupt)
		select {
		case <-done:
			return
		case <-time.After(5 * time.Second):
		}
	}
	_ = command.Process.Kill()
	err := <-done
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			t.Errorf("OpenCode process cleanup failed: %v", err)
		}
	}
}
