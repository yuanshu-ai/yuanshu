package node

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestControlCenterUsesLoopbackOneTimeBootstrapAndAuthenticatedAPI(t *testing.T) {
	commands := make(chan string, 8)
	center, err := newControlCenter(func() Status {
		return Status{Version: LocalStatusVersion, State: "ready", Platform: "darwin"}
	}, func(_ context.Context, request localRequest) localResponse {
		commands <- request.Command
		return localResponse{Protocol: localProtocol, OK: true, Config: map[string]any{"revision": "redacted"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = center.Close() })

	opened, err := center.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(opened)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Hostname() != "127.0.0.1" || parsed.Scheme != "http" || parsed.Fragment == "" {
		t.Fatalf("unexpected control center URL %q", opened)
	}
	origin := parsed.Scheme + "://" + parsed.Host
	token := parsed.Fragment
	parsed.Fragment = ""

	response := controlCenterRequest(t, http.MethodGet, parsed.String(), "", "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("asset status = %d", response.StatusCode)
	}
	if response.Header.Get("Content-Security-Policy") == "" || response.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatal("security headers are missing")
	}
	_ = response.Body.Close()

	sessionURL := origin + "/api/v1/session"
	body, _ := json.Marshal(map[string]string{"token": token})
	response = controlCenterRequest(t, http.MethodPost, sessionURL, origin, "application/json", body)
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("session status = %d: %s", response.StatusCode, data)
	}
	var session struct {
		Session string `json:"session"`
	}
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil || session.Session == "" {
		t.Fatalf("session response = %#v, %v", session, err)
	}
	_ = response.Body.Close()

	response = controlCenterRequest(t, http.MethodPost, sessionURL, origin, "application/json", body)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused bootstrap status = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	overviewRequest, err := http.NewRequest(http.MethodGet, origin+"/api/v1/overview", nil)
	if err != nil {
		t.Fatal(err)
	}
	overviewRequest.Header.Set("Authorization", "YuanshuLocal "+session.Session)
	response, err = http.DefaultClient.Do(overviewRequest)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("overview status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	if command := <-commands; command != "config_show" {
		t.Fatalf("first overview command = %q", command)
	}
	if command := <-commands; command != "config_pending" {
		t.Fatalf("second overview command = %q", command)
	}
}

func TestControlCenterRejectsCrossOriginHostAndUnknownActions(t *testing.T) {
	center, err := newControlCenter(func() Status { return Status{} }, func(context.Context, localRequest) localResponse {
		return localResponse{Protocol: localProtocol, OK: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = center.Close() })
	opened, err := center.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(opened)
	origin := parsed.Scheme + "://" + parsed.Host
	body, _ := json.Marshal(map[string]string{"token": parsed.Fragment})
	response := controlCenterRequest(t, http.MethodPost, origin+"/api/v1/session", "http://attacker.invalid", "application/json", body)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin session status = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	request, err := http.NewRequest(http.MethodGet, origin+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "attacker.invalid"
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("invalid host status = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	if controlCenterCommand("turn.start") || controlCenterCommand("arbitrary") {
		t.Fatal("remote task controls must not be exposed by the local control center")
	}
	if controlCenterCommand("config_approve") || controlCenterCommand("client_revoke") {
		t.Fatal("sensitive controls must require native or CLI confirmation")
	}
	if !controlCenterCommand("config_update") || !controlCenterCommand("pairing_create") || !controlCenterCommand("setup_discover") {
		t.Fatal("expected safe local management controls are missing")
	}
}

func controlCenterRequest(t *testing.T, method, target, origin, contentType string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response.Request.URL.String(), "token=") {
		t.Fatal("bootstrap token leaked into query parameters")
	}
	return response
}
