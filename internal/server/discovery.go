package server

import (
	"net/http"
	"net/url"
	"strings"
)

type wellKnownDiscovery struct {
	DeploymentMode string
	PublicURL      string
	CAFingerprint  string
	Invitations    bool
}

func withWellKnownDiscovery(next http.Handler, value wellKnownDiscovery) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/.well-known/yuanshu" {
			next.ServeHTTP(writer, request)
			return
		}
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		base := strings.TrimSuffix(value.PublicURL, "/")
		parsed, err := url.Parse(base)
		if err != nil || parsed.Host == "" {
			writeError(writer, http.StatusServiceUnavailable, "discovery_unavailable")
			return
		}
		relay := *parsed
		if relay.Scheme == "https" {
			relay.Scheme = "wss"
		} else {
			relay.Scheme = "ws"
		}
		relay.Path, relay.RawPath, relay.RawQuery, relay.Fragment = "/node/connect", "", "", ""
		writeJSON(writer, http.StatusOK, map[string]any{
			"product": "yuanshu", "apiVersion": "1", "deploymentMode": value.DeploymentMode,
			"publicUrl": base, "nodeRelayUrl": relay.String(), "pairingUrl": base + "/pair",
			"nodeInvitationsAllowed": value.Invitations, "caFingerprint": value.CAFingerprint,
		})
	})
}

func shortCertificateFingerprint(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), ":", "")
	if len(value) > 16 {
		return value[:16]
	}
	return value
}
