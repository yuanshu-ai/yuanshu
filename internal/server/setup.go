package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
)

const serverSetupHTML = `<!doctype html>
<html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>设置 Yuanshu Server</title>
<style>body{max-width:800px;margin:40px auto;padding:0 20px;font:15px/1.5 system-ui;background:#0d1117;color:#e6edf3}.brand{display:flex;align-items:center;gap:14px;margin-bottom:8px}.brand svg{width:54px;height:54px;flex:none}.brand h1{margin:0}form{display:grid;gap:16px}label{display:grid;gap:6px}input,select,button{font:inherit;padding:11px;border-radius:8px;border:1px solid #30363d;background:#161b22;color:inherit}button{background:#238636;cursor:pointer}small{color:#8b949e}.card{padding:20px;border:1px solid #30363d;border-radius:12px}#message,#summary{white-space:pre-wrap}#result[hidden]{display:none}#qr{width:192px;height:192px;background:white;padding:8px;border-radius:8px}</style>
<main><div class="brand"><svg viewBox="80 80 352 352" role="img" aria-label="远枢"><path fill="#E7EEE9" d="M113 164h71v31c0 7 3 13 8 18l51 51c4 4 6 9 6 15v31c0 6 3 9 7 9s7-3 7-9v-31c0-6 2-11 6-15l51-51c5-5 8-11 8-18v-31h71c5 0 9 4 9 9v34c0 8-4 15-10 20l-97 83c-7 6-10 14-10 23v64c0 8-6 13-14 13h-42c-8 0-14-5-14-13v-64c0-9-3-17-10-23l-97-83c-6-5-10-12-10-20v-34c0-5 4-9 9-9Z"/><g fill="#72D7A2"><rect width="55" height="55" x="104" y="102" rx="9"/><rect width="55" height="55" x="353" y="102" rx="9"/><rect width="48" height="48" x="232" y="179" rx="5" transform="rotate(45 256 203)"/><path d="M169 290l32 26c3 2 4 5 4 9v56c0 5-5 7-9 4l-25-21c-3-3-5-7-5-11v-58c0-3 1-5 3-5Z"/><path d="m343 290-32 26c-3 2-4 5-4 9v56c0 5 5 7 9 4l25-21c3-3 5-7 5-11v-58c0-3-1-5-3-5Z"/></g></svg><h1>Yuanshu Server 本机设置</h1></div><p>部署模式、监听与证书只在当前电脑修改。远程 Admin 不能执行这些操作。</p>
<form id="form"><div class="card"><label>部署模式<select name="mode"><option value="local">本机（HTTP/WS）</option><option value="lan-managed">局域网托管 CA（推荐）</option><option value="public-ip-acme">公网 IP ACME</option><option value="external">外部证书/反向代理</option></select></label><label>数据目录<input name="dataDir" required></label><label>监听地址<input name="listen" list="listen-addresses" required><datalist id="listen-addresses"></datalist></label><label>浏览器公开 URL<input name="publicURL" placeholder="https://192.168.1.20:7444"></label></div>
<div class="card"><label>TLS 终止<select name="tlsTermination"><option value="">由模式决定</option><option value="server">Server 证书文件</option><option value="proxy">同机反向代理</option></select></label><label>证书绝对路径<input name="certFile"></label><label>私钥绝对路径<input name="keyFile" type="password" autocomplete="off"></label><label>ACME 环境<select name="acmeEnvironment"><option value="production">production</option><option value="staging">staging</option></select></label><label>ACME 联系邮箱（可留空）<input name="acmeEmail" type="email"></label><label><input name="acceptTerms" type="checkbox"> 接受 ACME CA 条款</label></div>
<small>local 仅允许字面量 loopback。lan-managed 会生成本地 CA。public-ip-acme 需要公网 443 转发。external/proxy 后端也只能监听 loopback。</small><button id="submit" type="submit">预检配置</button><div id="summary" class="card" hidden></div><p id="message" role="status"></p></form>
<section id="result" class="card" hidden><h2>设置已保存</h2><p><a id="access" rel="noreferrer"></a></p><p id="fingerprint"></p><img id="qr" alt="根证书安装地址二维码" hidden></section></main>
<script type="module">const token=location.hash.slice(1);history.replaceState(null,"",location.pathname);const form=document.querySelector("#form"),message=document.querySelector("#message"),summary=document.querySelector("#summary"),submit=document.querySelector("#submit");let session="",reviewed=false;const authorization=()=>session?"YuanshuSetup "+session:"";const post=async(path,body)=>{const response=await fetch(path,{method:"POST",headers:{"Content-Type":"application/json",Authorization:authorization()},body:JSON.stringify(body)});const value=await response.json();if(!response.ok)throw new Error(value.error||"请求失败");return value};const payload=()=>{const data=Object.fromEntries(new FormData(form));data.acceptTerms=document.querySelector('[name="acceptTerms"]').checked;return data};form.addEventListener("input",()=>{reviewed=false;summary.hidden=true;submit.textContent="预检配置"});try{session=(await post("/api/session",{token})).session;const state=await fetch("/api/state",{headers:{Authorization:authorization()}}).then(r=>r.json());for(const [key,value] of Object.entries(state.config||{})){const field=document.querySelector('[name="'+key+'"]');if(field&&typeof value==="string")field.value=value;if(field&&typeof value==="boolean")field.checked=value}for(const address of state.interfaces||[]){const option=document.createElement("option");option.value=address;document.querySelector("#listen-addresses").append(option)}if(state.readOnly){message.textContent="Server 正在运行：当前页面只读，请停止 Server 后重新打开设置。";submit.disabled=true}}catch(error){message.textContent=error.message}form.addEventListener("submit",async event=>{event.preventDefault();message.textContent="正在验证配置和证书…";try{if(!reviewed){const result=await post("/api/preflight",payload());summary.textContent=result.summary;summary.hidden=false;reviewed=true;submit.textContent="确认并应用";message.textContent="请核对安全摘要后再次确认。";return}const result=await post("/api/apply",payload());message.textContent="设置已保存。现在可以关闭此页面并启动 Server。";document.querySelector("#result").hidden=false;const access=document.querySelector("#access");access.href=result.accessUrl;access.textContent=result.accessUrl;if(result.caFingerprint)document.querySelector("#fingerprint").textContent="根 CA 指纹："+result.caFingerprint;if(result.qrDataUrl){const qr=document.querySelector("#qr");qr.src=result.qrDataUrl;qr.hidden=false}submit.disabled=true}catch(error){message.textContent=error.message}});</script></html>`

type serverSetupService struct {
	mu               sync.Mutex
	configPath       string
	host             string
	bootstrap        string
	bootstrapExpires time.Time
	session          string
	sessionExpires   time.Time
	readOnly         bool
	done             chan error
}

type serverSetupPayload struct {
	Mode            string `json:"mode"`
	DataDir         string `json:"dataDir"`
	Listen          string `json:"listen"`
	PublicURL       string `json:"publicURL"`
	TLSTermination  string `json:"tlsTermination"`
	CertFile        string `json:"certFile"`
	KeyFile         string `json:"keyFile"`
	ACMEEnvironment string `json:"acmeEnvironment"`
	ACMEEmail       string `json:"acmeEmail"`
	AcceptTerms     bool   `json:"acceptTerms"`
}

func setupServer(ctx context.Context, args []string, output io.Writer) error {
	if len(args) != 2 || args[0] != "--config" || !filepath.IsAbs(args[1]) {
		return ErrUsage
	}
	configPath := filepath.Clean(args[1])
	token, err := randomSetupToken()
	if err != nil {
		return errors.New("server setup session is unavailable")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("server setup loopback service is unavailable")
	}
	defer listener.Close()
	service := &serverSetupService{configPath: configPath, host: listener.Addr().String(), bootstrap: token, bootstrapExpires: time.Now().UTC().Add(2 * time.Minute), done: make(chan error, 1)}
	if value, loadErr := LoadConfigFile(configPath); loadErr == nil {
		lock, lockErr := acquireDataLock(filepath.Join(value.DataDir, "server.lock"))
		if lockErr != nil {
			service.readOnly = true
		} else {
			_ = lock.Close()
		}
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return errors.New("server setup configuration is unavailable")
	}
	httpServer := &http.Server{Handler: service, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	go func() { _ = httpServer.Serve(listener) }()
	setupURL := "http://" + listener.Addr().String() + "/#" + token
	_, _ = fmt.Fprintf(output, "Yuanshu Server setup: %s\n", setupURL)
	if err := openLocalSetupURL(ctx, setupURL); err != nil {
		_, port, _ := net.SplitHostPort(listener.Addr().String())
		_, _ = fmt.Fprintf(output, "Headless host: create an SSH tunnel with -L %s:127.0.0.1:%s, then open the same URL locally.\n", port, port)
	}
	select {
	case err := <-service.done:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = httpServer.Shutdown(shutdownCtx)
		cancel()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *serverSetupService) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setWebSecurityHeaders(writer)
	if request.Host != s.host || request.Header.Get("Origin") != "" && request.Header.Get("Origin") != "http://"+s.host {
		writeError(writer, http.StatusForbidden, "forbidden")
		return
	}
	switch request.URL.Path {
	case "/":
		if request.Method != http.MethodGet {
			writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Security-Policy", "default-src 'none'; connect-src 'self'; img-src data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		_, _ = io.WriteString(writer, serverSetupHTML)
	case "/api/session":
		s.exchange(writer, request)
	case "/api/state":
		if !s.authorized(request) || request.Method != http.MethodGet {
			writeError(writer, http.StatusUnauthorized, "session_required")
			return
		}
		s.state(writer)
	case "/api/preflight":
		if !s.authorized(request) || request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
			writeError(writer, http.StatusUnauthorized, "session_required")
			return
		}
		s.preflight(writer, request)
	case "/api/apply":
		if !s.authorized(request) || request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
			writeError(writer, http.StatusUnauthorized, "session_required")
			return
		}
		s.apply(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (s *serverSetupService) exchange(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
		writeError(writer, http.StatusForbidden, "session_rejected")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 4096)
	if json.NewDecoder(request.Body).Decode(&body) != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if body.Token == "" || body.Token != s.bootstrap || !s.bootstrapExpires.After(time.Now().UTC()) {
		writeError(writer, http.StatusUnauthorized, "session_expired")
		return
	}
	s.bootstrap = ""
	s.session, _ = randomSetupToken()
	s.sessionExpires = time.Now().UTC().Add(15 * time.Minute)
	writeJSON(writer, http.StatusOK, map[string]string{"session": s.session})
}

func (s *serverSetupService) authorized(request *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session != "" && request.Header.Get("Authorization") == "YuanshuSetup "+s.session && s.sessionExpires.After(time.Now().UTC())
}

func (s *serverSetupService) state(writer http.ResponseWriter) {
	result := map[string]any{"readOnly": s.readOnly, "interfaces": localInterfaceAddresses(), "config": map[string]any{"mode": "local", "dataDir": filepath.Join(filepath.Dir(s.configPath), "server-data"), "listen": "127.0.0.1:7444", "acmeEnvironment": "production"}}
	if value, err := LoadConfigFile(s.configPath); err == nil {
		result["config"] = map[string]any{"mode": string(value.DeploymentMode), "dataDir": value.DataDir, "listen": value.Listen, "publicURL": value.PublicURL, "tlsTermination": value.TLS.Termination, "certFile": value.TLS.CertFile, "keyFile": value.TLS.KeyFile, "acmeEnvironment": value.ACME.Environment, "acmeEmail": value.ACME.Email, "acceptTerms": value.ACME.AcceptTerms}
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *serverSetupService) preflight(writer http.ResponseWriter, request *http.Request) {
	body, ok := decodeServerSetupPayload(writer, request)
	if !ok {
		return
	}
	value, err := validateServerSetupPayload(body)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "configuration_rejected")
		return
	}
	provider := "none"
	switch value.DeploymentMode {
	case DeploymentLANManaged:
		provider = "managed-ca"
	case DeploymentPublicIPACME:
		provider = "acme-ip (shortlived, TLS-ALPN-01)"
	case DeploymentExternal:
		provider = "external-" + value.TLS.Termination
	}
	summary := fmt.Sprintf("模式：%s\n监听：%s\n浏览器地址：%s\n证书来源：%s\n远程明文：禁止", value.DeploymentMode, value.Listen, serverPublicBase(value), provider)
	if value.DeploymentMode == DeploymentPublicIPACME {
		summary += "\n前置条件：公网 TCP 443 必须转发到当前监听端口。"
	}
	writeJSON(writer, http.StatusOK, map[string]string{"summary": summary})
}

func (s *serverSetupService) apply(writer http.ResponseWriter, request *http.Request) {
	if s.readOnly {
		writeError(writer, http.StatusConflict, "server_running")
		return
	}
	body, ok := decodeServerSetupPayload(writer, request)
	if !ok {
		return
	}
	value, err := validateServerSetupPayload(body)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "configuration_rejected")
		return
	}
	if err := os.MkdirAll(body.DataDir, 0o700); err != nil {
		writeError(writer, http.StatusBadRequest, "configuration_rejected")
		return
	}
	arguments := []string{"--config", s.configPath, "--mode", body.Mode, "--data-dir", body.DataDir, "--listen", body.Listen, "--non-interactive"}
	if body.PublicURL != "" {
		arguments = append(arguments, "--public-url", body.PublicURL)
	}
	if body.TLSTermination != "" {
		arguments = append(arguments, "--tls-termination", body.TLSTermination)
	}
	if body.CertFile != "" {
		arguments = append(arguments, "--tls-cert", body.CertFile)
	}
	if body.KeyFile != "" {
		arguments = append(arguments, "--tls-key", body.KeyFile)
	}
	if body.ACMEEnvironment != "" {
		arguments = append(arguments, "--acme-environment", body.ACMEEnvironment)
	}
	if body.ACMEEmail != "" {
		arguments = append(arguments, "--acme-email", body.ACMEEmail)
	}
	if body.AcceptTerms {
		arguments = append(arguments, "--acme-accept-terms")
	}
	if _, err := os.Lstat(s.configPath); err == nil {
		arguments = append(arguments, "--replace")
	}
	if err := initializeServer(request.Context(), arguments, strings.NewReader(""), io.Discard); err != nil {
		writeError(writer, http.StatusBadRequest, "configuration_rejected")
		return
	}
	result := map[string]any{"ok": true, "accessUrl": serverPublicBase(value)}
	if value.DeploymentMode == DeploymentLANManaged {
		if raw, readErr := readManagedCAPublic(value.DataDir); readErr == nil {
			if block, _ := pem.Decode(raw); block != nil {
				result["caFingerprint"] = certificateFingerprint(block.Bytes)
			}
		}
		trustURL := strings.TrimSuffix(serverPublicBase(value), "/") + "/trust"
		if png, qrErr := qrcode.Encode(trustURL, qrcode.Medium, 192); qrErr == nil {
			result["qrDataUrl"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		}
	}
	writeJSON(writer, http.StatusOK, result)
	select {
	case s.done <- nil:
	default:
	}
}

func decodeServerSetupPayload(writer http.ResponseWriter, request *http.Request) (serverSetupPayload, bool) {
	request.Body = http.MaxBytesReader(writer, request.Body, 32<<10)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var body serverSetupPayload
	if decoder.Decode(&body) != nil || ensureJSONEnd(decoder) != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return serverSetupPayload{}, false
	}
	return body, true
}

func validateServerSetupPayload(body serverSetupPayload) (ConfigFile, error) {
	if !filepath.IsAbs(body.DataDir) {
		return ConfigFile{}, ErrInvalid
	}
	value := ConfigFile{
		ConfigVersion: CurrentConfigVersion, DeploymentMode: DeploymentMode(body.Mode), DataDir: filepath.Clean(body.DataDir),
		Listen: body.Listen, PublicURL: strings.TrimSuffix(body.PublicURL, "/"),
	}
	switch value.DeploymentMode {
	case DeploymentExternal:
		value.TLS = TLSFileConfig{Termination: body.TLSTermination, CertFile: body.CertFile, KeyFile: body.KeyFile}
	case DeploymentPublicIPACME:
		value.ACME = ACMEConfig{Environment: body.ACMEEnvironment, Email: body.ACMEEmail, AcceptTerms: body.AcceptTerms}
	}
	if value.PublicURL != "" {
		value.AllowedControlOrigins = []string{controlOrigin(value.PublicURL)}
	}
	if err := ValidateConfigFile(value); err != nil {
		return ConfigFile{}, err
	}
	if value.DeploymentMode == DeploymentExternal && value.TLS.Termination == "server" {
		if _, err := loadTLSConfig(optionsFromConfig(value)); err != nil {
			return ConfigFile{}, err
		}
	}
	return value, nil
}

func localInterfaceAddresses() []string {
	interfaces, _ := net.Interfaces()
	seen := map[string]bool{"127.0.0.1:7444": true, "[::1]:7444": true}
	result := []string{"127.0.0.1:7444", "[::1]:7444"}
	for _, item := range interfaces {
		addresses, _ := item.Addrs()
		for _, address := range addresses {
			host, _, err := net.ParseCIDR(address.String())
			if err != nil || host == nil || host.IsLoopback() || host.IsUnspecified() || host.IsMulticast() || host.IsLinkLocalUnicast() {
				continue
			}
			value := net.JoinHostPort(host.String(), "7444")
			if !seen[value] {
				seen[value] = true
				result = append(result, value)
			}
		}
	}
	return result
}

func randomSetupToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func openLocalSetupURL(ctx context.Context, target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.CommandContext(ctx, "/usr/bin/open", target)
	case "windows":
		command = exec.CommandContext(ctx, "rundll32.exe", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.CommandContext(ctx, "xdg-open", target)
	}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	return command.Start()
}
