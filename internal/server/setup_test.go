package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestServerSetupUsesOneTimeLoopbackSessionAndWritesValidatedConfig(t *testing.T) {
	root := t.TempDir()
	service := &serverSetupService{
		configPath: filepath.Join(root, "config", "server.toml"), host: "127.0.0.1:49152",
		bootstrap: "bootstrap-token", bootstrapExpires: time.Now().Add(time.Minute), done: make(chan error, 1),
	}
	exchangeBody, _ := json.Marshal(map[string]string{"token": "bootstrap-token"})
	exchange := httptest.NewRecorder()
	exchangeRequest := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:49152/api/session", bytes.NewReader(exchangeBody))
	exchangeRequest.Host = service.host
	exchangeRequest.Header.Set("Origin", "http://"+service.host)
	exchangeRequest.Header.Set("Content-Type", "application/json")
	service.ServeHTTP(exchange, exchangeRequest)
	if exchange.Code != http.StatusOK {
		t.Fatalf("exchange status=%d body=%s", exchange.Code, exchange.Body.String())
	}
	var session map[string]string
	if json.Unmarshal(exchange.Body.Bytes(), &session) != nil || session["session"] == "" {
		t.Fatalf("session=%v", session)
	}
	replay := httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:49152/api/session", bytes.NewReader(exchangeBody))
	replayRequest.Host = service.host
	replayRequest.Header.Set("Origin", "http://"+service.host)
	replayRequest.Header.Set("Content-Type", "application/json")
	service.ServeHTTP(replay, replayRequest)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("bootstrap replay status=%d", replay.Code)
	}
	payload, _ := json.Marshal(serverSetupPayload{Mode: "local", DataDir: filepath.Join(root, "data"), Listen: "127.0.0.1:7555"})
	preflight := httptest.NewRecorder()
	preflightRequest := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:49152/api/preflight", bytes.NewReader(payload))
	preflightRequest.Host = service.host
	preflightRequest.Header.Set("Origin", "http://"+service.host)
	preflightRequest.Header.Set("Content-Type", "application/json")
	preflightRequest.Header.Set("Authorization", "YuanshuSetup "+session["session"])
	service.ServeHTTP(preflight, preflightRequest)
	if preflight.Code != http.StatusOK || !bytes.Contains(preflight.Body.Bytes(), []byte("local")) {
		t.Fatalf("preflight status=%d body=%s", preflight.Code, preflight.Body.String())
	}
	apply := httptest.NewRecorder()
	applyRequest := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:49152/api/apply", bytes.NewReader(payload))
	applyRequest.Host = service.host
	applyRequest.Header.Set("Origin", "http://"+service.host)
	applyRequest.Header.Set("Content-Type", "application/json")
	applyRequest.Header.Set("Authorization", "YuanshuSetup "+session["session"])
	service.ServeHTTP(apply, applyRequest)
	if apply.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", apply.Code, apply.Body.String())
	}
	value, err := LoadConfigFile(service.configPath)
	if err != nil || value.DeploymentMode != DeploymentLocal || value.Listen != "127.0.0.1:7555" {
		t.Fatalf("config=%+v err=%v", value, err)
	}
}

func TestServerSetupManagedLANReturnsTrustFingerprintAndQRCode(t *testing.T) {
	root := t.TempDir()
	service := &serverSetupService{
		configPath: filepath.Join(root, "config", "server.toml"), host: "127.0.0.1:49154",
		session: "session-token", sessionExpires: time.Now().Add(time.Minute), done: make(chan error, 1),
	}
	payload, _ := json.Marshal(serverSetupPayload{Mode: "lan-managed", DataDir: filepath.Join(root, "data"), Listen: "0.0.0.0:7444", PublicURL: "https://192.168.20.30:7444"})
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:49154/api/apply", bytes.NewReader(payload))
	request.Host = service.host
	request.Header.Set("Origin", "http://"+service.host)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "YuanshuSetup session-token")
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", response.Code, response.Body.String())
	}
	var result map[string]any
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || result["caFingerprint"] == "" || result["qrDataUrl"] == "" {
		t.Fatalf("result=%v", result)
	}
}

func TestServerSetupRejectsCrossOriginAndExpiredSession(t *testing.T) {
	service := &serverSetupService{host: "127.0.0.1:49153", bootstrap: "token", bootstrapExpires: time.Now().Add(-time.Second), done: make(chan error, 1)}
	body, _ := json.Marshal(map[string]string{"token": "token"})
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:49153/api/session", bytes.NewReader(body))
	request.Host = service.host
	request.Header.Set("Origin", "http://evil.example")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:49153/api/session", bytes.NewReader(body))
	request.Host = service.host
	request.Header.Set("Origin", "http://"+service.host)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	service.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expired session status=%d", response.Code)
	}
}
