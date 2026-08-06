package codex_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const sharedRuntimeLiveEnvironment = "YUANSHU_CODEX_SHARED_RUNTIME_LIVE"

// TestSharedRuntimeLive is a bounded evidence gate for one explicitly owned
// app-server endpoint. It never attempts to discover or attach an arbitrary
// Codex process, and it records only capability booleans.
func TestSharedRuntimeLive(t *testing.T) {
	if os.Getenv(sharedRuntimeLiveEnvironment) != "1" {
		t.Skip("set YUANSHU_CODEX_SHARED_RUNTIME_LIVE=1 to run the bounded shared-runtime probe")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("PF-091 currently verifies the native macOS Unix-socket path")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()

	binary := sharedRuntimeCodexBinary(t)
	version := sharedRuntimeCodexVersion(t, ctx, binary)
	workspace := liveWorkspace(t)
	runDirectory := sharedRuntimeDirectory(t)
	server := startSharedRuntimeServer(t, ctx, binary, workspace, runDirectory)
	defer server.close()

	terminal := startSharedRuntimeCLI(t, ctx, binary, server.endpoint, workspace,
		"Use the shell exactly once to run: sh -c 'sleep 20; printf YUANSHU_PF091_SHARED'. Do not perform any other action.")
	defer terminal.close()

	// Detect the CLI-owned active Turn without relying on event fanout, close
	// that discovery connection, then connect a fresh Node-like client.
	detector := requireSharedRuntimeClient(t, ctx, server.socketPath, "detector")
	threadID, discovered := discoverSharedRuntimeThread(ctx, detector, 30*time.Second)
	initialRead := discovered && readSharedRuntimeSummary(ctx, detector, threadID)
	turnID, turnCount, active := waitForSharedRuntimeActiveTurn(ctx, detector, threadID, 60*time.Second)
	detector.close()
	if !discovered || !initialRead || !active {
		t.Logf("PF-091 CLI-owned turn preflight: discovered=%t read=%t active=%t process_running=%t failure_code=%s", discovered, initialRead, active, terminal.running(), terminal.failureCode())
		t.Logf("PF-091 shared runtime evidence: codex=%s os=%s unix_socket=true cli_owned=true discovered=%t live_read=false subscribed=false live_fanout=false approval=false steer=false interrupt=false restart_read=false duplicate_turn=false", version, runtime.GOOS, discovered)
		t.Fatal("the real Codex CLI did not start the bounded Turn")
	}

	node := requireSharedRuntimeClient(t, ctx, server.socketPath, "node_after_turn_start")
	defer node.close()
	liveRead, liveTurns := readSharedRuntimeTurns(ctx, node, threadID)
	subscribed := resumeSharedRuntimeThread(ctx, node, threadID, workspace)
	archived := false
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = callAttachmentShort(cleanupCtx, node, "turn/interrupt", map[string]any{"threadId": threadID}, nil)
		if !archived {
			_ = callAttachmentShort(cleanupCtx, node, "thread/archive", map[string]any{"threadId": threadID}, nil)
		}
	}()

	approval, methods := waitForSharedRuntimeApproval(ctx, node, threadID, turnID, 2*time.Minute)
	liveFanout := len(methods) > 0
	approvalObserved := len(approval.id) > 0
	approvalAccepted := false
	steerAccepted := false
	interruptAccepted := false
	turnInterrupted := false
	cliStayedConnected := terminal.running()
	cliOutputBeforeControl := terminal.output.Len()

	if approvalObserved {
		approvalAccepted = respondAttachmentShort(ctx, node, approval.id, map[string]any{"decision": "accept"}) == nil
	}
	if approvalAccepted {
		commandSeen, commandMethods := waitForSharedRuntimeCommand(ctx, node, threadID, turnID, 30*time.Second)
		methods = append(methods, commandMethods...)
		if commandSeen {
			var steerResult struct {
				TurnID string `json:"turnId"`
			}
			steerAccepted = callAttachmentShort(ctx, node, "turn/steer", map[string]any{
				"threadId": threadID, "expectedTurnId": turnID,
				"input": []map[string]any{{"type": "text", "text": "After the command, reply briefly. Do not invoke another tool."}},
			}, &steerResult) == nil && steerResult.TurnID == turnID
			interruptAccepted = callAttachmentShort(ctx, node, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil) == nil
			var observedMethods []string
			turnInterrupted, observedMethods = waitForSharedRuntimeCompletion(ctx, node, threadID, turnID, 45*time.Second)
			methods = append(methods, observedMethods...)
		}
	}
	if !interruptAccepted {
		_ = callAttachmentShort(ctx, node, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil)
	}

	time.Sleep(500 * time.Millisecond)
	// Do not couple the Gate to localized or ANSI-rendered TUI wording. A redraw
	// after the Node-side controls, while the CLI remains connected, is the
	// bounded observation that the CLI consumed the shared Runtime updates.
	cliObservedControl := cliStayedConnected && terminal.running() && terminal.output.Len() > cliOutputBeforeControl
	terminal.close()

	node.close()
	reconnected := requireSharedRuntimeClient(t, ctx, server.socketPath, "reconnected")
	reconnectRead, reconnectTurns := readSharedRuntimeTurns(ctx, reconnected, threadID)
	reconnected.close()

	server.close()
	server = startSharedRuntimeServer(t, ctx, binary, workspace, runDirectory)
	defer server.close()
	node = requireSharedRuntimeClient(t, ctx, server.socketPath, "restarted")
	defer node.close()
	restartRead, restartTurns := readSharedRuntimeTurns(ctx, node, threadID)
	restartResume := resumeSharedRuntimeThread(ctx, node, threadID, workspace)
	duplicateTurn := turnCount != 1 || liveTurns != 1 || reconnectTurns != 1 || restartTurns != 1

	if err := callAttachmentShort(ctx, node, "thread/archive", map[string]any{"threadId": threadID}, nil); err == nil {
		archived = true
	}

	pass := liveRead && subscribed && liveFanout && approvalObserved && approvalAccepted && steerAccepted && interruptAccepted && turnInterrupted &&
		cliObservedControl && reconnectRead && restartRead && restartResume && !duplicateTurn
	t.Logf("PF-091 shared runtime evidence: codex=%s os=%s unix_socket=true cli_owned=true discovered=true live_read=%t subscribed=%t cli_connected=%t live_fanout=%t approval=%t steer=%t interrupt=%t cli_control_visible=%t reconnect_read=%t restart_read=%t restart_resume=%t duplicate_turn=%t pass=%t",
		version, runtime.GOOS, liveRead, subscribed, cliStayedConnected, liveFanout, approvalObserved && approvalAccepted, steerAccepted,
		interruptAccepted && turnInterrupted, cliObservedControl, reconnectRead, restartRead, restartResume, duplicateTurn, pass)
	if !pass {
		t.Fatal("the explicit shared Runtime did not satisfy the PF-091 live-control gate")
	}
}

type sharedRuntimeServer struct {
	command    *exec.Cmd
	done       chan error
	endpoint   string
	socketPath string
	closeOnce  sync.Once
}

func startSharedRuntimeServer(t *testing.T, ctx context.Context, binary, workspace, runDirectory string) *sharedRuntimeServer {
	t.Helper()
	socketPath := filepath.Join(runDirectory, "app.sock")
	_ = os.Remove(socketPath)
	endpoint := "unix://" + socketPath
	command := exec.CommandContext(ctx, binary, "app-server", "--listen", endpoint)
	command.Dir = workspace
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal("start shared Codex app-server")
	}
	server := &sharedRuntimeServer{command: command, done: make(chan error, 1), endpoint: endpoint, socketPath: socketPath}
	go func() { server.done <- command.Wait() }()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if sharedRuntimeSocketPrivate(runDirectory, socketPath) {
			probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			client, err := dialSharedRuntimeClient(probeCtx, socketPath)
			cancel()
			if err == nil {
				client.close()
				return server
			}
		}
		select {
		case <-server.done:
			t.Fatal("shared Codex app-server exited before readiness")
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}
	server.close()
	t.Fatal("shared Codex app-server did not become ready")
	return nil
}

func (s *sharedRuntimeServer) close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		if s.command != nil && s.command.Process != nil {
			_ = s.command.Process.Signal(os.Interrupt)
			select {
			case <-s.done:
			case <-time.After(3 * time.Second):
				_ = s.command.Process.Kill()
				select {
				case <-s.done:
				case <-time.After(5 * time.Second):
				}
			}
		}
		_ = os.Remove(s.socketPath)
	})
}

func dialSharedRuntimeClient(ctx context.Context, socketPath string) (*attachmentClient, error) {
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	httpClient := &http.Client{Transport: transport}
	conn, response, err := websocket.Dial(ctx, "ws://yuanshu.local/", &websocket.DialOptions{HTTPClient: httpClient, Host: "localhost"})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		transport.CloseIdleConnections()
		return nil, errors.New("shared Runtime endpoint unavailable")
	}
	conn.SetReadLimit(16 << 20)
	client := &attachmentClient{conn: conn, pending: make(map[int64]chan attachmentReply), methods: make(chan attachmentMessage, 256), done: make(chan struct{})}
	go func() {
		client.readLoop()
		transport.CloseIdleConnections()
	}()
	return client, nil
}

func requireSharedRuntimeClient(t *testing.T, ctx context.Context, socketPath, name string) *attachmentClient {
	t.Helper()
	client, err := dialSharedRuntimeClient(ctx, socketPath)
	if err != nil {
		t.Fatal("connect shared Runtime client")
	}
	if err := client.call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "yuanshu_pf091_" + name, "version": "0.0.0"}}, nil); err != nil {
		client.close()
		t.Fatal("initialize shared Runtime client")
	}
	if err := client.notify(ctx, "initialized", map[string]any{}); err != nil {
		client.close()
		t.Fatal("complete shared Runtime initialization")
	}
	return client
}

func readSharedRuntimeSummary(parent context.Context, client *attachmentClient, threadID string) bool {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	return client.call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": false}, &result) == nil && result.Thread.ID == threadID
}

func readSharedRuntimeTurns(parent context.Context, client *attachmentClient, threadID string) (bool, int) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	var result struct {
		Thread struct {
			ID    string `json:"id"`
			Turns []any  `json:"turns"`
		} `json:"thread"`
	}
	if client.call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &result) != nil || result.Thread.ID != threadID {
		return false, 0
	}
	return true, len(result.Thread.Turns)
}

func resumeSharedRuntimeThread(parent context.Context, client *attachmentClient, threadID, workspace string) bool {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	return client.call(ctx, "thread/resume", map[string]any{"threadId": threadID, "cwd": workspace}, &result) == nil && result.Thread.ID == threadID
}

type sharedRuntimeTerminal struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	done    chan struct{}
	output  *sharedRuntimeOutput
	once    sync.Once
	errMu   sync.Mutex
	err     error
}

func startSharedRuntimeCLI(t *testing.T, ctx context.Context, binary, endpoint, workspace, prompt string) *sharedRuntimeTerminal {
	t.Helper()
	command := exec.CommandContext(ctx, "/usr/bin/script", "-q", "/dev/null", binary,
		"--remote", endpoint, "--no-alt-screen", "-C", workspace, "-s", "read-only", "-a", "untrusted", prompt)
	command.Dir = workspace
	command.Env = append(os.Environ(), "TERM=xterm-256color", "NO_COLOR=1")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal("open shared Codex CLI input")
	}
	output := &sharedRuntimeOutput{remaining: 256 << 10}
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		t.Fatal("start shared Codex CLI")
	}
	terminal := &sharedRuntimeTerminal{command: command, stdin: stdin, done: make(chan struct{}), output: output}
	go func() {
		terminal.errMu.Lock()
		terminal.err = command.Wait()
		terminal.errMu.Unlock()
		close(terminal.done)
	}()
	return terminal
}

func (t *sharedRuntimeTerminal) running() bool {
	if t == nil {
		return false
	}
	select {
	case <-t.done:
		return false
	default:
		return true
	}
}

func (t *sharedRuntimeTerminal) failureCode() string {
	if t == nil || t.output == nil {
		return "cli_unavailable"
	}
	value := strings.ToLower(t.output.String())
	for _, candidate := range []struct {
		pattern string
		code    string
	}{
		{pattern: "already loaded", code: "thread_already_loaded"},
		{pattern: "already active", code: "thread_already_active"},
		{pattern: "not materialized", code: "thread_not_materialized"},
		{pattern: "not found", code: "thread_not_found"},
		{pattern: "failed to resume", code: "thread_resume_failed"},
		{pattern: "trust", code: "workspace_trust_required"},
		{pattern: "failed to connect", code: "endpoint_connect_failed"},
		{pattern: "connection refused", code: "endpoint_connect_failed"},
		{pattern: "invalid url", code: "endpoint_url_invalid"},
		{pattern: "no such file", code: "endpoint_socket_missing"},
		{pattern: "authentication", code: "endpoint_auth_failed"},
		{pattern: "unexpected argument", code: "cli_arguments_invalid"},
		{pattern: "unrecognized option", code: "cli_arguments_invalid"},
		{pattern: "unknown option", code: "cli_arguments_invalid"},
		{pattern: "not a terminal", code: "cli_terminal_unavailable"},
		{pattern: "terminal", code: "cli_terminal_error"},
		{pattern: "session", code: "cli_session_error"},
		{pattern: "socket", code: "endpoint_socket_error"},
		{pattern: "error", code: "cli_error"},
	} {
		if strings.Contains(value, candidate.pattern) {
			return candidate.code
		}
	}
	if strings.TrimSpace(value) == "" {
		return "cli_no_output"
	}
	return "cli_no_turn"
}

func (t *sharedRuntimeTerminal) close() {
	if t == nil {
		return
	}
	t.once.Do(func() {
		if t.stdin != nil {
			_, _ = t.stdin.Write([]byte{3})
			_ = t.stdin.Close()
		}
		if t.command != nil && t.command.Process != nil {
			_ = t.command.Process.Signal(os.Interrupt)
			select {
			case <-t.done:
			case <-time.After(3 * time.Second):
				_ = t.command.Process.Kill()
				select {
				case <-t.done:
				case <-time.After(3 * time.Second):
				}
			}
		}
		if t.output != nil {
			t.output.Clear()
		}
	})
}

func discoverSharedRuntimeThread(parent context.Context, client *attachmentClient, timeout time.Duration) (string, bool) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	for {
		var result struct {
			Data []string `json:"data"`
		}
		callCtx, callCancel := context.WithTimeout(ctx, 2*time.Second)
		err := client.call(callCtx, "thread/loaded/list", map[string]any{}, &result)
		callCancel()
		if err == nil && len(result.Data) == 1 && result.Data[0] != "" {
			return result.Data[0], true
		}
		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func waitForSharedRuntimeActiveTurn(parent context.Context, client *attachmentClient, threadID string, timeout time.Duration) (string, int, bool) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	for {
		var result struct {
			Thread struct {
				ID     string `json:"id"`
				Status struct {
					Type string `json:"type"`
				} `json:"status"`
				Turns []struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"turns"`
			} `json:"thread"`
		}
		callCtx, callCancel := context.WithTimeout(ctx, 3*time.Second)
		err := client.call(callCtx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &result)
		callCancel()
		if err == nil && result.Thread.ID == threadID && len(result.Thread.Turns) == 1 {
			turn := result.Thread.Turns[0]
			if turn.ID != "" && (result.Thread.Status.Type == "active" || turn.Status == "inProgress") {
				return turn.ID, len(result.Thread.Turns), true
			}
		}
		select {
		case <-ctx.Done():
			return "", 0, false
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type sharedRuntimeOutput struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	remaining int
}

func (o *sharedRuntimeOutput) Write(value []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	written := len(value)
	if o.remaining <= 0 {
		return written, nil
	}
	if len(value) > o.remaining {
		value = value[:o.remaining]
	}
	_, _ = o.buffer.Write(value)
	o.remaining -= len(value)
	return written, nil
}

func (o *sharedRuntimeOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.String()
}

func (o *sharedRuntimeOutput) Len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.Len()
}

func (o *sharedRuntimeOutput) Clear() {
	o.mu.Lock()
	defer o.mu.Unlock()
	value := o.buffer.Bytes()
	clear(value)
	o.buffer.Reset()
	o.remaining = 0
}

func waitForSharedRuntimeApproval(parent context.Context, client *attachmentClient, threadID, turnID string, timeout time.Duration) (attachmentApproval, []string) {
	return waitForAttachmentApproval(parent, client, threadID, turnID, timeout)
}

func waitForSharedRuntimeCommand(parent context.Context, client *attachmentClient, threadID, turnID string, timeout time.Duration) (bool, []string) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	methods := make([]string, 0, 16)
	for {
		select {
		case message := <-client.methods:
			methods = append(methods, message.method)
			if message.method == "item/commandExecution/outputDelta" {
				return true, methods
			}
			if message.method != "item/started" {
				continue
			}
			var params struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
				Item     struct {
					Type string `json:"type"`
				} `json:"item"`
			}
			if json.Unmarshal(message.params, &params) == nil && params.ThreadID == threadID && params.TurnID == turnID && params.Item.Type == "commandExecution" {
				return true, methods
			}
		case <-ctx.Done():
			return false, methods
		}
	}
}

func waitForSharedRuntimeCompletion(parent context.Context, client *attachmentClient, threadID, turnID string, timeout time.Duration) (bool, []string) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	methods := make([]string, 0, 16)
	for {
		select {
		case message := <-client.methods:
			methods = append(methods, message.method)
			if message.method != "turn/completed" {
				continue
			}
			var params struct {
				ThreadID string `json:"threadId"`
				Turn     struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"turn"`
			}
			if json.Unmarshal(message.params, &params) == nil && params.ThreadID == threadID && params.Turn.ID == turnID {
				return params.Turn.Status == "interrupted", methods
			}
		case <-ctx.Done():
			return false, methods
		}
	}
}

func sharedRuntimeDirectory(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/private/tmp", "yuanshu-pf091-")
	if err != nil {
		t.Fatal("create shared Runtime directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		t.Fatal("restrict shared Runtime directory")
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func sharedRuntimeSocketPrivate(directory, socketPath string) bool {
	directoryInfo, err := os.Lstat(directory)
	if err != nil || directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() || directoryInfo.Mode().Perm() != 0o700 {
		return false
	}
	socketInfo, err := os.Lstat(socketPath)
	return err == nil && socketInfo.Mode()&os.ModeSymlink == 0 && socketInfo.Mode()&os.ModeSocket != 0
}

func sharedRuntimeCodexBinary(t *testing.T) string {
	t.Helper()
	if value := os.Getenv("YUANSHU_CODEX_BINARY"); value != "" {
		if !filepath.IsAbs(value) {
			t.Fatal("YUANSHU_CODEX_BINARY must be absolute")
		}
		return value
	}
	if value, err := exec.LookPath("codex"); err == nil {
		return value
	}
	if runtime.GOOS == "darwin" {
		value := "/Applications/ChatGPT.app/Contents/Resources/codex"
		if info, err := os.Stat(value); err == nil && info.Mode().IsRegular() {
			return value
		}
	}
	t.Fatal("Codex binary is unavailable")
	return ""
}

func sharedRuntimeCodexVersion(t *testing.T, ctx context.Context, binary string) string {
	t.Helper()
	output, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		t.Fatal("Codex version is unavailable")
	}
	value := strings.TrimSpace(string(output))
	version, ok := strings.CutPrefix(value, "codex-cli ")
	if !ok || version == "" || strings.ContainsAny(version, "\r\n\t ") {
		t.Fatal("Codex version output is invalid")
	}
	return version
}
