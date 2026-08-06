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
	Enabled         bool
	PublicURL       string
	AdminEnabled    bool
	Certificate     certificateProvider
	AllowLoopbackWS bool
}

type webDeliveryHandler struct {
	api             http.Handler
	assets          fs.FS
	files           http.Handler
	indexHTML       []byte
	publicURL       string
	adminEnabled    bool
	certificate     certificateProvider
	allowLoopbackWS bool
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
		indexHTML: indexHTML, publicURL: strings.TrimSuffix(options.PublicURL, "/"), adminEnabled: options.AdminEnabled, certificate: options.Certificate,
		allowLoopbackWS: options.AllowLoopbackWS,
	}, nil
}

func (h *webDeliveryHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if serverAPIPath(request.URL.Path) {
		h.api.ServeHTTP(writer, request)
		return
	}
	setWebSecurityHeaders(writer, h.allowLoopbackWS)
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
	if request.URL.Path == "/trust" {
		h.serveTrust(writer, request)
		return
	}
	if request.URL.Path == "/v1/trust/ca.crt" {
		h.serveRootCA(writer, request)
		return
	}
	if (request.URL.Path == "/admin" || strings.HasPrefix(request.URL.Path, "/admin/")) && !h.adminEnabled {
		h.api.ServeHTTP(writer, request)
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

func (h *webDeliveryHandler) serveRootCA(writer http.ResponseWriter, request *http.Request) {
	if h.certificate == nil {
		http.NotFound(writer, request)
		return
	}
	raw, ok := h.certificate.PublicCACertificate()
	if !ok {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/pkix-cert")
	writer.Header().Set("Content-Disposition", `attachment; filename="yuanshu-local-ca.crt"`)
	writer.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	writer.WriteHeader(http.StatusOK)
	if request.Method != http.MethodHead {
		_, _ = writer.Write(raw)
	}
}

func (h *webDeliveryHandler) serveTrust(writer http.ResponseWriter, request *http.Request) {
	if h.certificate == nil {
		http.NotFound(writer, request)
		return
	}
	status := h.certificate.Status()
	if _, ok := h.certificate.PublicCACertificate(); !ok {
		http.NotFound(writer, request)
		return
	}
	short := status.CAFingerprint
	if len(short) > 23 {
		short = short[:23]
	}
	body := `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>信任 Yuanshu Server</title><style>body{max-width:760px;margin:48px auto;padding:0 20px;font:16px/1.6 system-ui;background:#0d1117;color:#e6edf3}a{color:#7dd3fc}code{word-break:break-all}section{border:1px solid #30363d;border-radius:12px;padding:20px;margin:16px 0}</style><main><h1>安装局域网根证书</h1><p>这张公开根证书只用于让当前设备信任这台 Yuanshu Server。请先在 Server 本机核对指纹，再安装。</p><section><strong>短指纹</strong><p><code>` + short + `</code></p><a href="/v1/trust/ca.crt">下载根 CA 证书</a></section><section><h2>安装提示</h2><p>iPhone/iPad：下载并安装描述文件后，还需在“设置 → 通用 → 关于本机 → 证书信任设置”中启用完整信任。</p><p>Android：在系统安全设置中安装为 CA 证书；不同厂商菜单名称可能不同。</p><p>macOS/Windows：导入到当前用户的受信任根证书存储。只信任你核对过指纹的证书。</p></section></main></html>`
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
	writer.WriteHeader(http.StatusOK)
	if request.Method != http.MethodHead {
		_, _ = writer.Write([]byte(body))
	}
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
	if baseURL == "" && request.Host != "" {
		scheme := "http"
		if request.TLS != nil {
			scheme = "https"
		}
		baseURL = scheme + "://" + request.Host
	}
	relayURL, pairingURL := "", "/pair"
	if parsed, err := url.Parse(baseURL); err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http" && loopbackURLHost(parsed.Hostname())) && parsed.Host != "" {
		parsed.Path, parsed.RawPath, parsed.RawQuery, parsed.Fragment = "/web/connect", "", "", ""
		if parsed.Scheme == "https" {
			parsed.Scheme = "wss"
		} else {
			parsed.Scheme = "ws"
		}
		relayURL = parsed.String()
		if parsed.Scheme == "wss" {
			parsed.Scheme = "https"
		} else {
			parsed.Scheme = "http"
		}
		parsed.Path = "/pair"
		pairingURL = parsed.String()
	}
	writer.WriteHeader(http.StatusOK)
	if request.Method != http.MethodHead {
		_ = json.NewEncoder(writer).Encode(map[string]any{"relayUrl": relayURL, "pairingUrl": pairingURL, "adminEnabled": h.adminEnabled, "adminUrl": "/admin"})
	}
}

func loopbackURLHost(value string) bool {
	return value == "127.0.0.1" || value == "::1"
}

func serverAPIPath(value string) bool {
	switch value {
	case "/.well-known/yuanshu", "/healthz", "/readyz", "/node/connect", "/web/connect":
		return true
	}
	if value == "/v1/trust/ca.crt" {
		return false
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

func setWebSecurityHeaders(writer http.ResponseWriter, allowLoopbackWS bool) {
	connectSources := "'self' https: wss:"
	if allowLoopbackWS {
		connectSources += " ws:"
	}
	writer.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src "+connectSources+"; img-src 'self' data:; style-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	writer.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
}
