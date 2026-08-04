package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
)

var fixedServerNow = time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)

func openServerService(t *testing.T) (*BootstrapService, *serverstore.Store, string) {
	t.Helper()
	local, err := serverstore.Open(context.Background(), filepath.Join(t.TempDir(), "server.db"), serverstore.Options{Clock: func() time.Time { return fixedServerNow }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	service, err := NewBootstrapService(local, BootstrapOptions{Random: bytes.NewReader(bytes.Repeat([]byte{0x31}, 512)), Clock: func() time.Time { return fixedServerNow }})
	if err != nil {
		t.Fatal(err)
	}
	secret, issued, err := service.Rotate(context.Background())
	if err != nil || !issued {
		t.Fatalf("Rotate() secret=%q issued=%v err=%v", secret, issued, err)
	}
	return service, local, secret
}

func validClaimRequest() ClaimRequest {
	credential := sha256.Sum256([]byte("synthetic-node-credential"))
	return ClaimRequest{
		RequestID: "request-1", Name: "Office PC", OS: "windows", Version: "dev",
		PublicKey:      base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x21}, 32)),
		CredentialHash: base64.RawURLEncoding.EncodeToString(credential[:]),
	}
}

func TestBootstrapServiceClaimsAndRotatesOnlyWhilePending(t *testing.T) {
	service, _, secret := openServerService(t)
	response, replayed, err := service.Claim(context.Background(), secret, validClaimRequest())
	if err != nil || replayed || !strings.HasPrefix(response.OwnerID, "own_") || !strings.HasPrefix(response.NodeID, "nod_") {
		t.Fatalf("Claim()=%+v replayed=%v err=%v", response, replayed, err)
	}
	again, replayed, err := service.Claim(context.Background(), secret, validClaimRequest())
	if err != nil || !replayed || again != response {
		t.Fatalf("replay=%+v replayed=%v err=%v", again, replayed, err)
	}
	if next, issued, err := service.Rotate(context.Background()); err != nil || issued || next != "" {
		t.Fatalf("completed Rotate()=%q issued=%v err=%v", next, issued, err)
	}
}

func TestBootstrapServiceValidatesCanonicalInputs(t *testing.T) {
	service, _, secret := openServerService(t)
	tests := []ClaimRequest{
		{},
		func() ClaimRequest { value := validClaimRequest(); value.Name = " padded "; return value }(),
		func() ClaimRequest { value := validClaimRequest(); value.OS = "other"; return value }(),
		func() ClaimRequest { value := validClaimRequest(); value.PublicKey += "="; return value }(),
		func() ClaimRequest { value := validClaimRequest(); value.CredentialHash = "short"; return value }(),
	}
	for _, request := range tests {
		if _, _, err := service.Claim(context.Background(), secret, request); !errors.Is(err, ErrInvalid) {
			t.Fatalf("request=%+v error=%v", request, err)
		}
	}
	if _, _, err := service.Claim(context.Background(), "wrong", validClaimRequest()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong secret error=%v", err)
	}
}

func TestHTTPBootstrapLifecycleAndBoundaries(t *testing.T) {
	service, local, secret := openServerService(t)
	handler, err := NewHandler(service, local)
	if err != nil {
		t.Fatal(err)
	}
	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/v1/bootstrap/status", nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), "pending") || status.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d body=%q headers=%v", status.Code, status.Body.String(), status.Header())
	}
	body, _ := json.Marshal(validClaimRequest())
	claim := httptest.NewRequest(http.MethodPost, "/v1/bootstrap/claim", bytes.NewReader(body))
	claim.Header.Set("Content-Type", "application/json")
	claim.Header.Set("Authorization", "Bearer "+secret)
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, claim)
	if created.Code != http.StatusCreated || strings.Contains(created.Body.String(), secret) {
		t.Fatalf("claim status=%d body=%q", created.Code, created.Body.String())
	}
	replay := httptest.NewRequest(http.MethodPost, "/v1/bootstrap/claim", bytes.NewReader(body))
	replay.Header.Set("Content-Type", "application/json")
	replay.Header.Set("Authorization", "Bearer "+secret)
	replayed := httptest.NewRecorder()
	handler.ServeHTTP(replayed, replay)
	if replayed.Code != http.StatusOK || replayed.Body.String() != created.Body.String() {
		t.Fatalf("replay status=%d body=%q", replayed.Code, replayed.Body.String())
	}
}

func TestHTTPRejectsOriginUnknownFieldsOversizeAndRateLimit(t *testing.T) {
	service, local, secret := openServerService(t)
	handler, _ := NewHandler(service, local)
	tests := []struct {
		name   string
		body   string
		origin string
		code   int
	}{
		{"origin", `{}`, "https://example.test", http.StatusForbidden},
		{"unknown", `{"unknown":true}`, "", http.StatusBadRequest},
		{"oversize", `{"requestId":"` + strings.Repeat("x", maxClaimBytes) + `"}`, "", http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/bootstrap/claim", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+secret)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.code || strings.Contains(response.Body.String(), test.body) || strings.Contains(response.Body.String(), secret) {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
	limitedService, limitedStore, _ := openServerService(t)
	limited, _ := NewHandler(limitedService, limitedStore)
	for attempt := 0; attempt < 6; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/v1/bootstrap/claim", strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		limited.ServeHTTP(response, request)
		want := http.StatusUnauthorized
		if attempt == 5 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("attempt=%d status=%d want=%d", attempt, response.Code, want)
		}
	}
}

func TestHTTPRejectsWrongMethodWithStableError(t *testing.T) {
	service, local, _ := openServerService(t)
	handler, _ := NewHandler(service, local)
	request := httptest.NewRequest(http.MethodGet, "/v1/bootstrap/claim", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodPost || !strings.Contains(response.Body.String(), "method_not_allowed") {
		t.Fatalf("status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
}

func TestServerArgumentValidation(t *testing.T) {
	absolute := filepath.Join(t.TempDir(), "data")
	_, listen, err := parseServerArguments([]string{"--data-dir", absolute})
	if err != nil || listen != DefaultListenAddress {
		t.Fatalf("default listen = %q, %v", listen, err)
	}
	valid := [][]string{{"--data-dir", absolute}, {"run", "--data-dir", absolute, "--listen", "[::1]:9527"}}
	for _, args := range valid {
		if _, _, err := parseServerArguments(args); err != nil {
			t.Fatalf("valid args %q: %v", args, err)
		}
	}
	invalid := [][]string{nil, {"unknown"}, {"--data-dir", "relative"}, {"--data-dir", absolute, "--listen", "0.0.0.0:9527"}, {"--data-dir", absolute, "--listen", "127.0.0.2:9527"}, {"--data-dir", absolute, "--listen", "localhost:9527"}, {"--data-dir", absolute, "--listen", "127.0.0.1:0"}}
	for _, args := range invalid {
		if _, _, err := parseServerArguments(args); !errors.Is(err, ErrUsage) {
			t.Fatalf("invalid args %q: %v", args, err)
		}
	}
}

func TestRunRejectsInjectedNonLoopbackListener(t *testing.T) {
	listener := nonLoopbackTestListener{}
	err := Run(context.Background(), Options{
		DataDir: filepath.Join(t.TempDir(), "data"), Listen: "127.0.0.1:9527", Listener: listener,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Run() error=%v", err)
	}
}

// nonLoopbackTestListener proves the listener-address boundary without
// opening a wildcard socket. A real 0.0.0.0 test listener causes Windows
// Firewall to prompt for the ephemeral server.test.exe on every test build.
type nonLoopbackTestListener struct{}

func (nonLoopbackTestListener) Accept() (net.Conn, error) { return nil, errors.New("not used") }
func (nonLoopbackTestListener) Close() error              { return nil }
func (nonLoopbackTestListener) Addr() net.Addr            { return nonLoopbackTestAddr("0.0.0.0:9527") }

type nonLoopbackTestAddr string

func (nonLoopbackTestAddr) Network() string  { return "tcp" }
func (a nonLoopbackTestAddr) String() string { return string(a) }
