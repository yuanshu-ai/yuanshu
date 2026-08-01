package poc_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	pocnode "github.com/yuanshu-ai/yuanshu/internal/poc/node"
	"github.com/yuanshu-ai/yuanshu/internal/poc/protocol"
	"github.com/yuanshu-ai/yuanshu/internal/poc/relay"
	"github.com/yuanshu-ai/yuanshu/internal/poc/transport"
)

const nodeToken = "abcdef0123456789abcdef0123456789"

type fakeRuntime struct {
	events    chan pocnode.RuntimeEvent
	mu        sync.Mutex
	decisions []string
}

func (f *fakeRuntime) Start(context.Context, string) (<-chan pocnode.RuntimeEvent, error) {
	return f.events, nil
}
func (f *fakeRuntime) Resolve(_ context.Context, _ string, d string) error {
	f.mu.Lock()
	f.decisions = append(f.decisions, d)
	f.mu.Unlock()
	return nil
}
func (f *fakeRuntime) Close() error { return nil }

func frame(kind string, p any) protocol.Frame {
	f, _ := protocol.New(kind, "contract", "poc-node", p)
	return f
}
func receive(t *testing.T, ep transport.Endpoint, kind string) protocol.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		f, err := ep.Receive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if f.Type == kind {
			return f
		}
	}
}
func session(t *testing.T, srv *httptest.Server) *http.Cookie {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/poc/session", nil)
	req.Header.Set("Origin", srv.URL)
	req.Header.Set("X-Yuanshu-Session", "create")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("session status=%d", resp.StatusCode)
	}
	return resp.Cookies()[0]
}
func web(t *testing.T, srv *httptest.Server) transport.Endpoint {
	t.Helper()
	h := make(http.Header)
	h.Set("Origin", srv.URL)
	h.Set("Cookie", session(t, srv).String())
	conn, _, err := websocket.Dial(context.Background(), "wss"+strings.TrimPrefix(srv.URL, "https")+"/poc/web", &websocket.DialOptions{HTTPClient: srv.Client(), HTTPHeader: h})
	if err != nil {
		t.Fatal(err)
	}
	return transport.WebSocketEndpoint(conn, protocol.MaxEventBytes, protocol.MaxControlBytes)
}

func TestRelayDisconnectReplayApprovalFlow(t *testing.T) {
	hub, err := relay.New(nodeToken)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(hub.Handler())
	defer srv.Close()
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+nodeToken)
	conn, _, err := websocket.Dial(context.Background(), "wss"+strings.TrimPrefix(srv.URL, "https")+"/poc/node", &websocket.DialOptions{HTTPClient: srv.Client(), HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	nodeEP := transport.WebSocketEndpoint(conn, protocol.MaxControlBytes, protocol.MaxEventBytes)
	runtime := &fakeRuntime{events: make(chan pocnode.RuntimeEvent, 16)}
	controller := pocnode.New("poc-node", runtime)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = controller.Run(ctx, nodeEP) }()
	client := web(t, srv)
	if err := client.Send(ctx, frame(protocol.EventsResume, protocol.ResumePayload{LastSequence: 0})); err != nil {
		t.Fatal(err)
	}
	receive(t, client, protocol.NodeStatus)
	if err := client.Send(ctx, frame(protocol.TaskStart, protocol.TaskStartPayload{WorkspaceID: protocol.WorkspaceID, Prompt: "synthetic prompt"})); err != nil {
		t.Fatal(err)
	}
	receive(t, client, protocol.TurnStarted)
	runtime.events <- pocnode.RuntimeEvent{Type: protocol.AgentDelta, Payload: map[string]string{"delta": "synthetic agent body"}}
	receive(t, client, protocol.AgentDelta)
	runtime.events <- pocnode.RuntimeEvent{Approval: &pocnode.RuntimeApproval{Handle: "raw-request-id", Kind: "file-change", Summary: "synthetic"}}
	approval := receive(t, client, protocol.ApprovalRequested)
	last := approval.Sequence - 1
	_ = client.Close()
	runtime.events <- pocnode.RuntimeEvent{Type: protocol.DiffUpdated, Payload: map[string]string{"diff": "synthetic diff"}}
	time.Sleep(20 * time.Millisecond)
	client = web(t, srv)
	defer client.Close()
	if err := client.Send(ctx, frame(protocol.EventsResume, protocol.ResumePayload{LastSequence: last})); err != nil {
		t.Fatal(err)
	}
	approval = receive(t, client, protocol.ApprovalRequested)
	receive(t, client, protocol.DiffUpdated)
	var p struct {
		ApprovalID string `json:"approvalId"`
	}
	if err := json.Unmarshal(approval.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.ApprovalID == "" || p.ApprovalID == "raw-request-id" {
		t.Fatal("raw approval id escaped Node")
	}
	if err := client.Send(ctx, frame(protocol.ApprovalResolve, protocol.ApprovalResolvePayload{ApprovalID: p.ApprovalID, Decision: "accept"})); err != nil {
		t.Fatal(err)
	}
	receive(t, client, protocol.ApprovalResolved)
	runtime.events <- pocnode.RuntimeEvent{Type: protocol.TurnCompleted, Payload: map[string]string{"status": "completed"}, Terminal: true}
	receive(t, client, protocol.TurnCompleted)
}

func TestRelayAndStandaloneShareControlFrames(t *testing.T) {
	runtime := &fakeRuntime{events: make(chan pocnode.RuntimeEvent, 4)}
	client, nodeEP := transport.StandalonePair(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controller := pocnode.New("poc-node", runtime)
	go func() { _ = controller.Run(ctx, nodeEP) }()
	receive(t, client, protocol.NodeStatus)
	if err := client.Send(ctx, frame(protocol.TaskStart, protocol.TaskStartPayload{WorkspaceID: protocol.WorkspaceID, Prompt: "synthetic"})); err != nil {
		t.Fatal(err)
	}
	receive(t, client, protocol.TurnStarted)
	runtime.events <- pocnode.RuntimeEvent{Type: protocol.TurnCompleted, Payload: map[string]string{"status": "completed"}, Terminal: true}
	receive(t, client, protocol.TurnCompleted)
}

func TestServerPackageHasNoRuntimeDependency(t *testing.T) {
	cmd := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, "github.com/yuanshu-ai/yuanshu/internal/poc/relay")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	imports := string(out)
	for _, forbidden := range []string{"internal/adapter/codex", "internal/poc/codex", "internal/poc/node"} {
		if strings.Contains(imports, forbidden) {
			t.Fatalf("Server imports Runtime boundary: %s", forbidden)
		}
	}
}

func TestDiagnosticSnapshotDoesNotLeakCanaries(t *testing.T) {
	secret := "TOKEN_CANARY_" + time.Now().Format("150405.000000")
	runtime := &fakeRuntime{events: make(chan pocnode.RuntimeEvent, 1)}
	controller := pocnode.New("poc-node", runtime)
	snapshot := string(controller.SnapshotJSON())
	for _, canary := range []string{secret, "COOKIE_CANARY", "C:\\private\\workspace", "synthetic prompt", "synthetic agent body", "raw shell command"} {
		if strings.Contains(snapshot, canary) {
			t.Fatalf("diagnostic leaked canary category")
		}
	}
}
