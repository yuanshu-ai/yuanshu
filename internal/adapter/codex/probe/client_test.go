package probe

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const helperModeEnv = "YUANSHU_CODEX_PROBE_HELPER_MODE"

func TestInitializeCallAndServerRequests(t *testing.T) {
	t.Parallel()

	client := startHelper(t, "normal", Options{})
	defer closeClient(t, client)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	title := "Yuanshu Test"
	initialized, err := client.Initialize(ctx, ClientInfo{Name: "yuanshu_test", Title: &title, Version: "0.0.0"})
	if err != nil {
		t.Fatalf("Initialize() error = %v; stderr = %q", err, client.Stderr())
	}
	if initialized.PlatformOS != "test" {
		t.Fatalf("Initialize() platformOs = %q, want test", initialized.PlatformOS)
	}

	var list struct {
		Data []any `json:"data"`
	}
	if err := client.Call(ctx, "thread/list", map[string]any{"limit": 1}, &list); err != nil {
		t.Fatalf("Call(thread/list) error = %v", err)
	}
	if list.Data == nil || len(list.Data) != 0 {
		t.Fatalf("thread/list data = %#v, want empty non-nil slice", list.Data)
	}

	want := []struct {
		method    string
		isRequest bool
	}{
		{method: "turn/started", isRequest: false},
		{method: "item/commandExecution/requestApproval", isRequest: true},
		{method: "item/fileChange/requestApproval", isRequest: true},
	}
	for _, expected := range want {
		select {
		case message := <-client.Messages():
			if message.Method != expected.method || message.IsRequest() != expected.isRequest {
				t.Fatalf("message = {%q, request=%v}, want {%q, request=%v}", message.Method, message.IsRequest(), expected.method, expected.isRequest)
			}
			if message.IsRequest() {
				if err := client.Respond(*message.ID, map[string]any{"decision": "decline"}, nil); err != nil {
					t.Fatalf("Respond(%s) error = %v", message.Method, err)
				}
			}
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", expected.method, ctx.Err())
		}
	}
}

func TestConcurrentCalls(t *testing.T) {
	t.Parallel()

	client := startHelper(t, "echo", Options{})
	defer closeClient(t, client)

	const calls = 32
	var wait sync.WaitGroup
	errorsChannel := make(chan error, calls)
	for i := 0; i < calls; i++ {
		wait.Add(1)
		go func(sequence int) {
			defer wait.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var result struct {
				OK bool `json:"ok"`
			}
			if err := client.Call(ctx, "probe/echo", map[string]int{"sequence": sequence}, &result); err != nil {
				errorsChannel <- err
				return
			}
			if !result.OK {
				errorsChannel <- errors.New("echo response was not OK")
			}
		}(i)
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Error(err)
	}
}

func TestRPCError(t *testing.T) {
	t.Parallel()

	client := startHelper(t, "rpc-error", Options{})
	defer closeClient(t, client)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := client.Call(ctx, "probe/error", struct{}{}, nil)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("Call() error = %v, want RPCError", err)
	}
	if rpcErr.Code != -32601 {
		t.Fatalf("RPCError code = %d, want -32601", rpcErr.Code)
	}
}

func TestCallContextCancellation(t *testing.T) {
	t.Parallel()

	client := startHelper(t, "no-response", Options{})
	defer closeClient(t, client)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := client.Call(ctx, "probe/wait", struct{}{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call() error = %v, want context deadline exceeded", err)
	}
}

func TestProtocolFailuresTerminateClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    string
		options Options
		want    error
	}{
		{name: "malformed JSON", mode: "malformed", want: ErrInvalidMessage},
		{name: "oversized message", mode: "oversized", options: Options{MaxMessageBytes: 128}, want: ErrMessageTooLarge},
		{name: "queue overflow", mode: "queue", options: Options{QueueSize: 1}, want: ErrQueueFull},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := startHelper(t, test.mode, test.options)
			select {
			case <-client.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("client did not terminate")
			}
			if !errors.Is(client.Err(), test.want) {
				t.Fatalf("Err() = %v, want %v", client.Err(), test.want)
			}
			_ = client.Close()
		})
	}
}

func TestUnexpectedProcessExit(t *testing.T) {
	t.Parallel()

	client := startHelper(t, "exit", Options{})
	select {
	case <-client.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client did not observe process exit")
	}
	if client.Err() == nil || !strings.Contains(client.Err().Error(), "exited") {
		t.Fatalf("Err() = %v, want process exit error", client.Err())
	}
	_ = client.Close()
}

func TestCredentialCanaryDoesNotReachStderr(t *testing.T) {
	t.Parallel()

	client := startHelper(t, "credential-stderr", Options{})
	select {
	case <-client.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("credential stderr helper did not terminate")
	}
	stderr := client.Stderr()
	if strings.Contains(stderr, probeCredentialCanary()) || strings.Contains(stderr, probeAWSCredentialCanary()) {
		t.Fatal("credential canary reached the redacted stderr boundary")
	}
	if !strings.Contains(stderr, "<REDACTED_SECRET>") {
		t.Fatal("redacted stderr did not contain the replacement marker")
	}
	_ = client.Close()
}

func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	client := startHelper(t, "echo", Options{})
	first := client.Close()
	second := client.Close()
	if (first == nil) != (second == nil) || (first != nil && first.Error() != second.Error()) {
		t.Fatalf("Close() results differ: first=%v second=%v", first, second)
	}
	if err := client.Notify("probe/after-close", struct{}{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Notify() after close error = %v, want ErrClosed", err)
	}
}

func TestClassifyAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
		want AuthMode
	}{
		{name: "none", json: `{"account":null}`, want: AuthNone},
		{name: "api key", json: `{"account":{"type":"apiKey","email":"must-not-be-read@example.test"}}`, want: AuthAPIKey},
		{name: "chatgpt", json: `{"account":{"type":"chatgpt"}}`, want: AuthChatGPT},
		{name: "external chatgpt", json: `{"account":{"type":"chatgptAuthTokens"}}`, want: AuthChatGPT},
		{name: "custom provider", json: `{"account":{"type":"amazonBedrock"}}`, want: AuthCustomProvider},
		{name: "other", json: `{"account":{"type":"futureMode"}}`, want: AuthOther},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ClassifyAuth(json.RawMessage(test.json))
			if err != nil {
				t.Fatalf("ClassifyAuth() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("ClassifyAuth() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestClassifyAuthType(t *testing.T) {
	t.Parallel()

	tests := map[string]AuthMode{
		"":                    AuthNone,
		"apiKey":              AuthAPIKey,
		"apikey":              AuthAPIKey,
		"chatgpt":             AuthChatGPT,
		"chatgptAuthTokens":   AuthChatGPT,
		"amazonBedrock":       AuthCustomProvider,
		"bedrockApiKey":       AuthCustomProvider,
		"agentIdentity":       AuthOther,
		"personalAccessToken": AuthOther,
		"futureMode":          AuthOther,
	}
	for input, want := range tests {
		if got := ClassifyAuthType(input); got != want {
			t.Fatalf("ClassifyAuthType() returned the wrong coarse authentication mode")
		}
	}
}

func startHelper(t *testing.T, mode string, options Options) *Client {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	options.Binary = binary
	options.Args = []string{"-test.run=^TestProbeHelperProcess$"}
	options.Env = append(os.Environ(), helperModeEnv+"="+mode)
	client, err := Start(context.Background(), options)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	return client
}

func closeClient(t *testing.T, client *Client) {
	t.Helper()
	if err := client.Close(); err != nil {
		t.Errorf("Close() error = %v; stderr = %q", err, client.Stderr())
	}
}

func TestProbeHelperProcess(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		return
	}
	code := runHelper(mode)
	os.Exit(code)
}

func runHelper(mode string) int {
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)

	switch mode {
	case "malformed":
		fmt.Fprintln(os.Stdout, "{not-json}")
		return 0
	case "oversized":
		fmt.Fprintf(os.Stdout, `{"method":"probe/large","params":{"value":"%s"}}`+"\n", strings.Repeat("x", 1024))
		return 0
	case "queue":
		_ = encoder.Encode(map[string]any{"method": "probe/one", "params": map[string]any{}})
		_ = encoder.Encode(map[string]any{"method": "probe/two", "params": map[string]any{}})
		for scanner.Scan() {
		}
		return 0
	case "exit":
		return 7
	case "credential-stderr":
		canary := probeCredentialCanary()
		fmt.Fprintln(os.Stderr, strings.Join([]string{
			"OPENAI_API_KEY=" + canary,
			"Authorization: Bearer " + canary,
			"Cookie: session=" + canary,
			`auth.json={"access_token":"` + canary + `"}`,
			"ANTHROPIC_API_KEY=" + canary,
			"AWS_SECRET_ACCESS_KEY=" + canary,
			"AWS_ACCESS_KEY_ID=" + probeAWSCredentialCanary(),
		}, "\n"))
		return 0
	}

	responsesNeeded := 0
	for scanner.Scan() {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return 20
		}
		if message.Method == "" {
			if responsesNeeded == 0 || len(message.ID) == 0 || len(message.Result) == 0 {
				return 21
			}
			responsesNeeded--
			if responsesNeeded == 0 && mode == "normal" {
				return 0
			}
			continue
		}

		switch mode {
		case "echo":
			if len(message.ID) > 0 {
				_ = encoder.Encode(map[string]any{"id": json.RawMessage(message.ID), "result": map[string]any{"ok": true}})
			}
		case "rpc-error":
			if len(message.ID) > 0 {
				_ = encoder.Encode(map[string]any{"id": json.RawMessage(message.ID), "error": map[string]any{"code": -32601, "message": "not found"}})
			}
		case "no-response":
			continue
		case "normal":
			switch message.Method {
			case "initialize":
				_ = encoder.Encode(map[string]any{"id": json.RawMessage(message.ID), "result": map[string]any{"userAgent": "test", "platformFamily": "test", "platformOs": "test"}})
			case "initialized":
			case "thread/list":
				_ = encoder.Encode(map[string]any{"id": json.RawMessage(message.ID), "result": map[string]any{"data": []any{}, "nextCursor": nil}})
				_ = encoder.Encode(map[string]any{"method": "turn/started", "params": map[string]any{"turn": map[string]any{"id": "turn-test"}}})
				_ = encoder.Encode(map[string]any{"id": "approval-string", "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "thread-test"}})
				_ = encoder.Encode(map[string]any{"id": 77, "method": "item/fileChange/requestApproval", "params": map[string]any{"threadId": "thread-test"}})
				responsesNeeded = 2
			default:
				return 22
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return 23
	}
	return 0
}

func probeCredentialCanary() string {
	return strings.Join([]string{"yuanshu", "probe", "credential", "canary"}, "-")
}

func probeAWSCredentialCanary() string {
	return "AKIA" + strings.Repeat("B", 12)
}
