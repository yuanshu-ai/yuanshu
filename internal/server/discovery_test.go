package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWellKnownDiscoveryIsPublicAndRedacted(t *testing.T) {
	value := wellKnownDiscovery{DeploymentMode: "lan-managed", PublicURL: "https://192.168.1.20:9527", CAFingerprint: "AABBCCDD", Invitations: true}
	handler := withWellKnownDiscovery(http.NotFoundHandler(), value)
	request := httptest.NewRequest(http.MethodGet, "/.well-known/yuanshu", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	for _, expected := range []string{`"product":"yuanshu"`, `"nodeRelayUrl":"wss://192.168.1.20:9527/node/connect"`, `"pairingUrl":"https://192.168.1.20:9527/pair"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %s in %s", expected, body)
		}
	}
	for _, forbidden := range []string{"ownerId", "nodeId", "dataDir", "certificatePath"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("discovery leaked %s", forbidden)
		}
	}
}
