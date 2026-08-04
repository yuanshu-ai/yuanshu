package codex_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex/probe"
)

const attachmentLiveEnvironment = "YUANSHU_CODEX_ATTACHMENT_LIVE"

// TestAttachmentLive is an evidence probe, not a production attachment path.
// It intentionally records only capability booleans and never logs native IDs,
// endpoint addresses, credentials, prompts, responses, paths, or raw events.
func TestAttachmentLive(t *testing.T) {
	if os.Getenv(attachmentLiveEnvironment) != "1" {
		t.Skip("set YUANSHU_CODEX_ATTACHMENT_LIVE=1 to run the bounded attachment probe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()

	versionOutput, err := exec.CommandContext(ctx, "codex", "--version").Output()
	if err != nil {
		t.Fatal("Codex version is unavailable")
	}
	version := requireKnownCodexVersion(t, versionOutput)
	workspace := liveWorkspace(t)
	server := startAttachmentServer(t, ctx, workspace)
	defer server.close()

	if _, err := dialAttachmentClient(ctx, server.url, "invalid-capability-token"); err == nil {
		t.Fatal("WebSocket endpoint accepted an invalid capability token")
	}
	creator := requireAttachmentClient(t, ctx, server.url, server.token, "creator")
	defer creator.close()
	observer := requireAttachmentClient(t, ctx, server.url, server.token, "observer")
	defer observer.close()

	var account json.RawMessage
	if err := creator.call(ctx, "account/read", map[string]any{"refreshToken": false}, &account); err != nil {
		t.Fatal("Codex authentication is unavailable")
	}
	authMode, err := probe.ClassifyAuth(account)
	if err != nil || authMode == probe.AuthNone || authMode == probe.AuthOther {
		t.Fatal("Codex authentication is unavailable")
	}

	threadID := startAttachmentThread(t, ctx, creator, workspace)
	turnID := startAttachmentTurn(t, ctx, creator, threadID)
	archived := false
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cleanupCancel()
		_ = creator.call(cleanupCtx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil)
		if !archived {
			_ = creator.call(cleanupCtx, "thread/archive", map[string]any{"threadId": threadID}, nil)
		}
	}()

	creatorApproval, creatorMethods := waitForAttachmentApproval(ctx, creator, threadID, turnID, 2*time.Minute)
	observerApproval, observerMethods := waitForAttachmentApproval(ctx, observer, threadID, turnID, 3*time.Second)
	if len(creatorApproval.id) == 0 {
		t.Fatal("the bounded Turn did not reach a harmless pending approval")
	}

	liveRead := readAttachmentThread(ctx, observer, threadID)
	steer := callAttachmentShort(ctx, observer, "turn/steer", map[string]any{
		"threadId": threadID, "expectedTurnId": turnID,
		"input": []map[string]any{{"type": "text", "text": "Do not perform another tool call."}},
	}, nil) == nil

	approval := false
	if len(observerApproval.id) > 0 {
		approval = respondAttachmentShort(ctx, observer, observerApproval.id, map[string]any{"decision": "decline"}) == nil
	} else {
		// A server-request ID may be endpoint-scoped rather than connection-scoped.
		// Trying the creator's opaque ID from the observer is the bounded evidence
		// for cross-client approval ownership; the value is never logged.
		approval = respondAttachmentShort(ctx, observer, creatorApproval.id, map[string]any{"decision": "decline"}) == nil
	}
	interrupt := callAttachmentShort(ctx, observer, "turn/interrupt", map[string]any{
		"threadId": threadID, "turnId": turnID,
	}, nil) == nil
	if !approval {
		_ = respondAttachmentShort(ctx, creator, creatorApproval.id, map[string]any{"decision": "decline"})
	}
	if !interrupt {
		_ = callAttachmentShort(ctx, creator, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil)
	}

	if err := callAttachmentShort(ctx, creator, "thread/archive", map[string]any{"threadId": threadID}, nil); err != nil {
		t.Fatal("synthetic attachment thread could not be archived")
	}
	archived = true

	creator.close()
	endpointSurvivedClientClose := listAttachmentThread(ctx, observer, threadID, true)
	observer.close()

	reconnected := requireAttachmentClient(t, ctx, server.url, server.token, "reconnected")
	l2SameEndpoint := listAttachmentThread(ctx, reconnected, threadID, true)
	l3SameEndpoint := readAttachmentThread(ctx, reconnected, threadID)
	reconnected.close()

	l2Independent, l3Independent := verifyIndependentHistory(ctx, workspace, threadID)
	l5 := containsAttachmentMethod(observerMethods, "turn/started") || len(observerApproval.id) > 0
	l6 := steer && interrupt && approval
	t.Logf("PF-084 attachment evidence: codex=%s os=%s explicit_endpoint=true auth=capability-token l2_independent=%t l3_independent=%t l2_endpoint=%t l3_endpoint=%t l5=%t l6=%t client_close_safe=%t ordinary_process_endpoint=false", version, runtime.GOOS, l2Independent, l3Independent, l2SameEndpoint, l3SameEndpoint, l5, l6, endpointSurvivedClientClose)

	if !l2Independent || !l3Independent {
		t.Fatal("an independent app-server could not discover and read the persisted synthetic session")
	}
	if !l2SameEndpoint || !l3SameEndpoint || !liveRead || !endpointSurvivedClientClose {
		t.Fatal("the explicit authenticated endpoint did not preserve its bounded history/lifecycle contract")
	}
	if !containsAttachmentMethod(creatorMethods, "turn/started") {
		t.Fatal("the creator connection did not receive an ordered active-turn event")
	}

}

type attachmentServer struct {
	command *exec.Cmd
	url     string
	token   string
	done    chan error
}

func startAttachmentServer(t *testing.T, ctx context.Context, workspace string) *attachmentServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve loopback endpoint")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	tokenBytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, tokenBytes); err != nil {
		t.Fatal("create capability token")
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenFile := filepath.Join(t.TempDir(), "capability-token")
	if err := os.WriteFile(tokenFile, []byte(token), 0o600); err != nil {
		t.Fatal("write capability token")
	}
	url := "ws://127.0.0.1:" + strconv.Itoa(port)
	command := exec.CommandContext(ctx, "codex", "app-server", "--listen", url,
		"--ws-auth", "capability-token", "--ws-token-file", tokenFile)
	command.Dir = workspace
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal("start explicit Codex WebSocket endpoint")
	}
	server := &attachmentServer{command: command, url: url, token: token, done: make(chan error, 1)}
	go func() { server.done <- command.Wait() }()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		client, dialErr := dialAttachmentClient(probeCtx, url, token)
		cancel()
		if dialErr == nil {
			client.close()
			return server
		}
		select {
		case <-server.done:
			t.Fatal("explicit Codex WebSocket endpoint exited before readiness")
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}
	server.close()
	t.Fatal("explicit Codex WebSocket endpoint did not become ready")
	return nil
}

func (s *attachmentServer) close() {
	if s == nil || s.command == nil || s.command.Process == nil {
		return
	}
	_ = s.command.Process.Kill()
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
	}
}

type attachmentMessage struct {
	id     json.RawMessage
	method string
	params json.RawMessage
}

type attachmentReply struct {
	result json.RawMessage
	err    error
}

type attachmentClient struct {
	conn    *websocket.Conn
	nextID  atomic.Int64
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[int64]chan attachmentReply
	methods chan attachmentMessage
	done    chan struct{}
	once    sync.Once
}

func requireAttachmentClient(t *testing.T, ctx context.Context, url, token, name string) *attachmentClient {
	t.Helper()
	client, err := dialAttachmentClient(ctx, url, token)
	if err != nil {
		t.Fatal("connect authenticated Codex WebSocket client")
	}
	if err := client.call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": "yuanshu_pf084_" + name, "version": "0.0.0"},
	}, nil); err != nil {
		client.close()
		t.Fatal("initialize authenticated Codex WebSocket client")
	}
	if err := client.notify(ctx, "initialized", map[string]any{}); err != nil {
		client.close()
		t.Fatal("complete Codex WebSocket initialization")
	}
	return client
}

func dialAttachmentClient(ctx context.Context, url, token string) (*attachmentClient, error) {
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+token)
	conn, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, errors.New("attachment endpoint unavailable")
	}
	conn.SetReadLimit(16 << 20)
	client := &attachmentClient{
		conn: conn, pending: make(map[int64]chan attachmentReply),
		methods: make(chan attachmentMessage, 256), done: make(chan struct{}),
	}
	go client.readLoop()
	return client, nil
}

func (c *attachmentClient) call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)
	reply := make(chan attachmentReply, 1)
	c.mu.Lock()
	c.pending[id] = reply
	c.mu.Unlock()
	if err := c.write(ctx, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		c.removePending(id)
		return errors.New("attachment request unavailable")
	}
	select {
	case value := <-reply:
		if value.err != nil {
			return value.err
		}
		if result != nil && len(value.result) > 0 && !bytes.Equal(bytes.TrimSpace(value.result), []byte("null")) {
			if err := json.Unmarshal(value.result, result); err != nil {
				return errors.New("attachment response invalid")
			}
		}
		return nil
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	case <-c.done:
		return errors.New("attachment endpoint closed")
	}
}

func (c *attachmentClient) notify(ctx context.Context, method string, params any) error {
	return c.write(ctx, map[string]any{"method": method, "params": params})
}

func (c *attachmentClient) respond(ctx context.Context, id json.RawMessage, result any) error {
	if len(id) == 0 {
		return errors.New("attachment request unavailable")
	}
	return c.write(ctx, struct {
		ID     json.RawMessage `json:"id"`
		Result any             `json:"result"`
	}{ID: id, Result: result})
}

func (c *attachmentClient) write(ctx context.Context, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.Write(ctx, websocket.MessageText, encoded)
}

func (c *attachmentClient) readLoop() {
	defer c.closeDone()
	for {
		_, encoded, err := c.conn.Read(context.Background())
		if err != nil {
			return
		}
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(encoded, &envelope) != nil {
			continue
		}
		if envelope.Method != "" {
			message := attachmentMessage{id: append(json.RawMessage(nil), envelope.ID...), method: envelope.Method, params: append(json.RawMessage(nil), envelope.Params...)}
			select {
			case c.methods <- message:
			default:
				return
			}
			continue
		}
		var id int64
		if json.Unmarshal(envelope.ID, &id) != nil {
			continue
		}
		c.mu.Lock()
		reply := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if reply == nil {
			continue
		}
		value := attachmentReply{result: envelope.Result}
		if len(envelope.Error) > 0 && !bytes.Equal(bytes.TrimSpace(envelope.Error), []byte("null")) {
			value.err = errors.New("attachment request rejected")
		}
		reply <- value
	}
}

func (c *attachmentClient) removePending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *attachmentClient) close() {
	if c == nil {
		return
	}
	c.once.Do(func() {
		_ = c.conn.Close(websocket.StatusNormalClosure, "closed")
		<-c.done
	})
}

func (c *attachmentClient) closeDone() {
	select {
	case <-c.done:
		return
	default:
		close(c.done)
	}
}

type attachmentApproval struct{ id json.RawMessage }

func waitForAttachmentApproval(ctx context.Context, client *attachmentClient, threadID, turnID string, timeout time.Duration) (attachmentApproval, []string) {
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	methods := make([]string, 0, 16)
	for {
		select {
		case message := <-client.methods:
			methods = append(methods, message.method)
			if message.method != "item/commandExecution/requestApproval" && message.method != "item/fileChange/requestApproval" {
				continue
			}
			var scope struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
			}
			if json.Unmarshal(message.params, &scope) == nil && scope.ThreadID == threadID && scope.TurnID == turnID {
				return attachmentApproval{id: message.id}, methods
			}
		case <-deadline.Done():
			return attachmentApproval{}, methods
		}
	}
}

func startAttachmentThread(t *testing.T, ctx context.Context, client *attachmentClient, workspace string) string {
	t.Helper()
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := client.call(ctx, "thread/start", map[string]any{
		"cwd": workspace, "approvalPolicy": "untrusted", "sandbox": "workspace-write",
		"serviceName": "yuanshu_pf084_probe",
	}, &result); err != nil || result.Thread.ID == "" {
		t.Fatal("start bounded attachment thread")
	}
	return result.Thread.ID
}

func startAttachmentTurn(t *testing.T, ctx context.Context, client *attachmentClient, threadID string) string {
	t.Helper()
	command := `powershell -NoProfile -Command "Write-Output YUANSHU_PF084_APPROVAL"`
	if runtime.GOOS != "windows" {
		command = `printf YUANSHU_PF084_APPROVAL`
	}
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := client.call(ctx, "turn/start", map[string]any{
		"threadId":       threadID,
		"approvalPolicy": "untrusted",
		"sandboxPolicy":  map[string]any{"type": "readOnly"},
		"input":          []map[string]any{{"type": "text", "text": "Use the shell exactly once to run: " + command + ". Do not perform any other action."}},
	}, &result); err != nil || result.Turn.ID == "" {
		t.Fatal("start bounded attachment Turn")
	}
	return result.Turn.ID
}

func listAttachmentThread(ctx context.Context, client *attachmentClient, threadID string, archived bool) bool {
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if client.call(callCtx, "thread/list", map[string]any{
		"limit": 100, "archived": archived,
		"sourceKinds": attachmentSourceKinds(),
	}, &result) != nil {
		return false
	}
	for _, thread := range result.Data {
		if thread.ID == threadID {
			return true
		}
	}
	return false
}

func readAttachmentThread(ctx context.Context, client *attachmentClient, threadID string) bool {
	var result struct {
		Thread struct {
			ID    string `json:"id"`
			Turns []any  `json:"turns"`
		} `json:"thread"`
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return client.call(callCtx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &result) == nil &&
		result.Thread.ID == threadID && result.Thread.Turns != nil
}

func verifyIndependentHistory(ctx context.Context, workspace, threadID string) (bool, bool) {
	client, err := probe.Start(ctx, probe.Options{Dir: workspace, Env: probe.Environment()})
	if err != nil {
		return false, false
	}
	defer client.Close()
	if _, err := client.Initialize(ctx, probe.ClientInfo{Name: "yuanshu_pf084_history", Version: "0.0.0"}); err != nil {
		return false, false
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if client.Call(ctx, "thread/list", map[string]any{"limit": 100, "archived": true, "sourceKinds": attachmentSourceKinds()}, &list) != nil {
		return false, false
	}
	found := false
	for _, thread := range list.Data {
		found = found || thread.ID == threadID
	}
	var read struct {
		Thread struct {
			ID    string `json:"id"`
			Turns []any  `json:"turns"`
		} `json:"thread"`
	}
	readOK := client.Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &read) == nil &&
		read.Thread.ID == threadID && read.Thread.Turns != nil
	return found, readOK
}

func containsAttachmentMethod(methods []string, expected string) bool {
	for _, method := range methods {
		if method == expected {
			return true
		}
	}
	return false
}

func attachmentSourceKinds() []string {
	return []string{
		"cli", "vscode", "exec", "appServer", "subAgent", "subAgentReview",
		"subAgentCompact", "subAgentThreadSpawn", "subAgentOther", "unknown",
	}
}

func callAttachmentShort(parent context.Context, client *attachmentClient, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	return client.call(ctx, method, params, result)
}

func respondAttachmentShort(parent context.Context, client *attachmentClient, id json.RawMessage, result any) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	return client.respond(ctx, id, result)
}
