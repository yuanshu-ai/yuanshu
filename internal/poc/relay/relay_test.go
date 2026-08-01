package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yuanshu-ai/yuanshu/internal/poc/protocol"
	"github.com/yuanshu-ai/yuanshu/internal/poc/transport"
)

const testToken = "0123456789abcdef0123456789abcdef"

func newTLSServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	hub, err := New(testToken)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(hub.Handler())
	t.Cleanup(srv.Close)
	return hub, srv
}
func sessionCookieFor(t *testing.T, srv *httptest.Server, origin string) *http.Cookie {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/poc/session", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("X-Yuanshu-Session", "create")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("session status=%d", resp.StatusCode)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	if !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("insecure session cookie")
	}
	return cookies[0]
}
func dialWeb(t *testing.T, srv *httptest.Server, origin string, cookie *http.Cookie) (transport.Endpoint, *http.Response, error) {
	t.Helper()
	header := make(http.Header)
	header.Set("Origin", origin)
	header.Set("Cookie", cookie.String())
	conn, resp, err := websocket.Dial(context.Background(), "wss"+strings.TrimPrefix(srv.URL, "https")+"/poc/web", &websocket.DialOptions{HTTPClient: srv.Client(), HTTPHeader: header})
	if err != nil {
		return nil, resp, err
	}
	return transport.WebSocketEndpoint(conn, protocol.MaxEventBytes, protocol.MaxControlBytes), resp, nil
}

func TestStandaloneRelayRoutesOpaqueFramesAndReconnects(t *testing.T) {
	hub, srv := newTLSServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverSide, nodeSide := transport.StandalonePair(16)
	if err := hub.AttachNode(ctx, serverSide); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookieFor(t, srv, srv.URL)
	web, _, err := dialWeb(t, srv, srv.URL, cookie)
	if err != nil {
		t.Fatal(err)
	}
	control, _ := protocol.New(protocol.TaskStart, "request", "poc-node", protocol.TaskStartPayload{WorkspaceID: protocol.WorkspaceID, Prompt: "synthetic"})
	if err := web.Send(ctx, control); err != nil {
		t.Fatal(err)
	}
	got, err := nodeSide.Receive(ctx)
	if err != nil || got.Type != protocol.TaskStart {
		t.Fatalf("Node receive=%s err=%v", got.Type, err)
	}
	event, _ := protocol.New(protocol.AgentDelta, "request", "poc-node", map[string]string{"delta": "synthetic"})
	event.Sequence = 7
	if err := nodeSide.Send(ctx, event); err != nil {
		t.Fatal(err)
	}
	got, err = web.Receive(ctx)
	if err != nil || got.Sequence != 7 {
		t.Fatalf("Web receive=%+v err=%v", got, err)
	}
	_ = web.Close()
	cookie = sessionCookieFor(t, srv, srv.URL)
	web, _, err = dialWeb(t, srv, srv.URL, cookie)
	if err != nil {
		t.Fatal(err)
	}
	defer web.Close()
	resume, _ := protocol.New(protocol.EventsResume, "resume", "poc-node", protocol.ResumePayload{LastSequence: 7})
	if err := web.Send(ctx, resume); err != nil {
		t.Fatal(err)
	}
	got, err = nodeSide.Receive(ctx)
	if err != nil || got.Type != protocol.EventsResume {
		t.Fatalf("resume route=%s err=%v", got.Type, err)
	}
}

func TestRelayRejectsWrongOriginTokenAndNonLoopback(t *testing.T) {
	if err := ValidateLoopbackListen("0.0.0.0:7443"); err == nil {
		t.Fatal("wildcard listen accepted")
	}
	if err := ValidateLoopbackListen("192.168.1.4:7443"); err == nil {
		t.Fatal("LAN listen accepted")
	}
	if err := ValidateLoopbackListen("127.0.0.1:7443"); err != nil {
		t.Fatal(err)
	}
	_, srv := newTLSServer(t)
	cookie := sessionCookieFor(t, srv, srv.URL)
	_, resp, err := dialWeb(t, srv, "https://evil.invalid", cookie)
	if err == nil {
		t.Fatal("bad Origin accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad Origin status=%v", resp)
	}
	header := make(http.Header)
	header.Set("Authorization", "Bearer wrong-token")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, resp, err = websocket.Dial(ctx, "wss"+strings.TrimPrefix(srv.URL, "https")+"/poc/node", &websocket.DialOptions{HTTPClient: srv.Client(), HTTPHeader: header})
	if err == nil {
		t.Fatal("bad Node token accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token status=%v", resp)
	}
}

func TestEmbeddedUIUsesTextContentAndStrictCSP(t *testing.T) {
	_, srv := newTLSServer(t)
	resp, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("weak CSP: %s", csp)
	}
	jsResp, err := srv.Client().Get(srv.URL + "/poc/app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer jsResp.Body.Close()
	buf := make([]byte, 64<<10)
	n, _ := jsResp.Body.Read(buf)
	js := string(buf[:n])
	if !strings.Contains(js, "textContent") || strings.Contains(js, "innerHTML") {
		t.Fatal("UI does not enforce text-only rendering")
	}
	if !strings.Contains(js, `f.type==="snapshot"`) || !strings.Contains(js, "pendingApprovals") {
		t.Fatal("UI does not restore Node and pending approval state from snapshot")
	}
}
