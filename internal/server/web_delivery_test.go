package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedWebDeliveryServesWorkbenchRuntimeConfigAndAssets(t *testing.T) {
	api := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/healthz" {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(writer, request)
	})
	handler, err := newWebDeliveryHandler(api, webDeliveryOptions{Enabled: true, PublicURL: "https://192.168.1.20:9527"})
	if err != nil {
		t.Fatal(err)
	}

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusNoContent {
		t.Fatalf("health status=%d", health.Code)
	}

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Yuanshu") || page.Header().Get("Content-Security-Policy") == "" || page.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("page status=%d headers=%v body=%q", page.Code, page.Header(), page.Body.String())
	}

	runtime := httptest.NewRecorder()
	handler.ServeHTTP(runtime, httptest.NewRequest(http.MethodGet, "/yuanshu.config.json", nil))
	var settings map[string]any
	if err := json.Unmarshal(runtime.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if settings["relayUrl"] != "wss://192.168.1.20:9527/web/connect" || settings["pairingUrl"] != "https://192.168.1.20:9527/pair" || runtime.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("runtime settings=%v headers=%v", settings, runtime.Header())
	}

	match := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`).FindStringSubmatch(page.Body.String())
	if len(match) != 2 {
		t.Fatalf("asset link unavailable: %q", page.Body.String())
	}
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, match[1], nil))
	if asset.Code != http.StatusOK || asset.Body.Len() == 0 || !strings.Contains(asset.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset status=%d bytes=%d headers=%v", asset.Code, asset.Body.Len(), asset.Header())
	}

	spa := httptest.NewRecorder()
	handler.ServeHTTP(spa, httptest.NewRequest(http.MethodGet, "/nodes/node-1/threads/thread-1", nil))
	if spa.Code != http.StatusOK || spa.Body.String() != page.Body.String() {
		t.Fatalf("SPA fallback status=%d", spa.Code)
	}
	adminDisabled := httptest.NewRecorder()
	handler.ServeHTTP(adminDisabled, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if adminDisabled.Code != http.StatusNotFound {
		t.Fatalf("disabled admin status=%d", adminDisabled.Code)
	}
}

func TestEmbeddedWebDeliveryPreservesPublicServerDiscovery(t *testing.T) {
	api := withWellKnownDiscovery(http.NotFoundHandler(), wellKnownDiscovery{
		DeploymentMode: "local",
		PublicURL:      "http://127.0.0.1:9527",
		Invitations:    true,
	})
	handler, err := newWebDeliveryHandler(api, webDeliveryOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/.well-known/yuanshu", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"product":"yuanshu"`) {
		t.Fatalf("discovery status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestEmbeddedWebDeliveryServesAdminOnlyWhenEnabled(t *testing.T) {
	handler, err := newWebDeliveryHandler(http.NotFoundHandler(), webDeliveryOptions{Enabled: true, AdminEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Yuanshu") {
		t.Fatalf("admin status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestEmbeddedWebDeliveryCanBeDisabled(t *testing.T) {
	api := http.NotFoundHandler()
	handler, err := newWebDeliveryHandler(api, webDeliveryOptions{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestEmbeddedWebDeliveryRejectsTraversal(t *testing.T) {
	handler, err := newWebDeliveryHandler(http.NotFoundHandler(), webDeliveryOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/settings", nil)
	request.URL.Path = "/assets/../settings"
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestEmbeddedWebRuntimeConfigUsesTLSRequestHost(t *testing.T) {
	handler, err := newWebDeliveryHandler(http.NotFoundHandler(), webDeliveryOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://[fd00::20]:9527/yuanshu.config.json", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var settings map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if settings["relayUrl"] != "wss://[fd00::20]:9527/web/connect" || settings["pairingUrl"] != "https://[fd00::20]:9527/pair" {
		t.Fatalf("settings=%v", settings)
	}
}

func TestEmbeddedWebRuntimeConfigAllowsOnlyLiteralLoopbackPlaintext(t *testing.T) {
	handler, err := newWebDeliveryHandler(http.NotFoundHandler(), webDeliveryOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ host, relay string }{{"127.0.0.1:9527", "ws://127.0.0.1:9527/web/connect"}, {"[::1]:9527", "ws://[::1]:9527/web/connect"}, {"192.168.1.20:9527", ""}} {
		request := httptest.NewRequest(http.MethodGet, "http://"+test.host+"/yuanshu.config.json", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var settings map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &settings); err != nil || settings["relayUrl"] != test.relay {
			t.Fatalf("host=%s settings=%v err=%v", test.host, settings, err)
		}
	}
}

func TestManagedTrustPageExportsOnlyPublicRoot(t *testing.T) {
	root := t.TempDir()
	provider, err := newManagedCertificateProvider(context.Background(), Options{DataDir: root, DeploymentMode: DeploymentLANManaged, PublicURL: "https://192.168.20.31:9527"})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	handler, err := newWebDeliveryHandler(http.NotFoundHandler(), webDeliveryOptions{Enabled: true, Certificate: provider})
	if err != nil {
		t.Fatal(err)
	}
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/trust", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "短指纹") || strings.Contains(page.Body.String(), "PRIVATE KEY") {
		t.Fatalf("trust page status=%d body=%q", page.Code, page.Body.String())
	}
	certificate := httptest.NewRecorder()
	handler.ServeHTTP(certificate, httptest.NewRequest(http.MethodGet, "/v1/trust/ca.crt", nil))
	if certificate.Code != http.StatusOK || !strings.Contains(certificate.Body.String(), "CERTIFICATE") || strings.Contains(certificate.Body.String(), "PRIVATE KEY") {
		t.Fatalf("CA response status=%d body=%q", certificate.Code, certificate.Body.String())
	}
}

func TestWebAccessURLUsesPublicURLAndLoopbackForWildcard(t *testing.T) {
	if got := webAccessURL("https://192.168.1.20:9527/", &net.TCPAddr{IP: net.IPv4zero, Port: 9527}); got != "https://192.168.1.20:9527/" {
		t.Fatalf("public URL=%q", got)
	}
	if got := webAccessURL("", &net.TCPAddr{IP: net.IPv4zero, Port: 7555}); got != "http://127.0.0.1:7555/" {
		t.Fatalf("local URL=%q", got)
	}
}
