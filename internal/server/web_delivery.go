package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/yuanshu-ai/yuanshu/internal/server/webassets"
)

type webDeliveryOptions struct {
	Enabled   bool
	PublicURL string
}

type webDeliveryHandler struct {
	api       http.Handler
	assets    fs.FS
	files     http.Handler
	indexHTML []byte
	publicURL string
}

func newWebDeliveryHandler(api http.Handler, options webDeliveryOptions) (http.Handler, error) {
	if api == nil {
		return nil, ErrInvalid
	}
	if !options.Enabled {
		return api, nil
	}
	assets, err := webassets.FS()
	if err != nil {
		return nil, errors.New("embedded Web assets are unavailable")
	}
	indexHTML, err := fs.ReadFile(assets, "index.html")
	if err != nil || len(indexHTML) == 0 {
		return nil, errors.New("embedded Web entry is unavailable")
	}
	return &webDeliveryHandler{
		api: api, assets: assets, files: http.FileServer(http.FS(assets)),
		indexHTML: indexHTML, publicURL: strings.TrimSuffix(options.PublicURL, "/"),
	}, nil
}

func (h *webDeliveryHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if serverAPIPath(request.URL.Path) {
		h.api.ServeHTTP(writer, request)
		return
	}
	setWebSecurityHeaders(writer)
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !safeWebPath(request.URL.Path) {
		h.api.ServeHTTP(writer, request)
		return
	}
	if request.URL.Path == "/yuanshu.config.json" {
		h.serveRuntimeConfig(writer, request)
		return
	}
	if request.URL.Path == "/" || spaRoute(request.URL.Path) {
		h.serveIndex(writer, request)
		return
	}
	if _, err := fs.Stat(h.assets, strings.TrimPrefix(path.Clean(request.URL.Path), "/")); err != nil {
		h.api.ServeHTTP(writer, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/assets/") {
		writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		writer.Header().Set("Cache-Control", "no-cache")
	}
	h.files.ServeHTTP(writer, request)
}

func (h *webDeliveryHandler) serveIndex(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Content-Length", strconv.Itoa(len(h.indexHTML)))
	writer.WriteHeader(http.StatusOK)
	if request.Method != http.MethodHead {
		_, _ = writer.Write(h.indexHTML)
	}
}

func (h *webDeliveryHandler) serveRuntimeConfig(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	baseURL := h.publicURL
	if baseURL == "" && request.TLS != nil && request.Host != "" {
		baseURL = "https://" + request.Host
	}
	relayURL, pairingURL := "", "/pair"
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Scheme == "https" && parsed.Host != "" {
		parsed.Path, parsed.RawPath, parsed.RawQuery, parsed.Fragment = "/web/connect", "", "", ""
		parsed.Scheme = "wss"
		relayURL = parsed.String()
		parsed.Scheme, parsed.Path = "https", "/pair"
		pairingURL = parsed.String()
	}
	writer.WriteHeader(http.StatusOK)
	if request.Method != http.MethodHead {
		_ = json.NewEncoder(writer).Encode(map[string]string{"relayUrl": relayURL, "pairingUrl": pairingURL})
	}
}

func serverAPIPath(value string) bool {
	switch value {
	case "/healthz", "/readyz", "/node/connect", "/web/connect":
		return true
	}
	return value == "/v1" || strings.HasPrefix(value, "/v1/") || value == "/pair" || strings.HasPrefix(value, "/pair/")
}

func spaRoute(value string) bool {
	cleaned := path.Clean(value)
	return strings.HasPrefix(cleaned, "/") && path.Ext(cleaned) == ""
}

func safeWebPath(value string) bool {
	if !strings.HasPrefix(value, "/") || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func setWebSecurityHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' https: wss:; img-src 'self' data:; style-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	writer.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
}
