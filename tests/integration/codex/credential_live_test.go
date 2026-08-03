package codex_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex/probe"
)

const (
	credentialLiveEnvironment = "YUANSHU_CODEX_CREDENTIAL_LIVE"
	credentialLiveTurnLimit   = 1
)

func TestCredentialLiveBoundary(t *testing.T) {
	if os.Getenv(credentialLiveEnvironment) != "1" {
		t.Skip("set YUANSHU_CODEX_CREDENTIAL_LIVE=1 to run the bounded credential probe")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	workspace := t.TempDir()

	versionOutput, err := exec.CommandContext(ctx, "codex", "--version").Output()
	if err != nil {
		t.Fatalf("read Codex version: %v", err)
	}
	version := requireCompatibleCodexVersion(t, versionOutput)

	client, err := startLiveClient(ctx, workspace)
	if err != nil {
		t.Fatalf("start credential live client: %v", err)
	}
	runner := &liveRunner{client: client}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cleanupCancel()
		if err := cleanupLiveScenario(cleanupCtx, runner, workspace); err != nil {
			t.Errorf("cleanup credential live scenario: %v", err)
		}
	}()

	var accountResult json.RawMessage
	if err := client.Call(ctx, "account/read", map[string]any{"refreshToken": false}, &accountResult); err != nil {
		t.Fatalf("account/read: %v", safeClientError(client, err))
	}
	authMode, err := probe.ClassifyAuth(accountResult)
	accountResult = nil
	if err != nil {
		t.Fatalf("classify authentication: %v", err)
	}
	switch authMode {
	case probe.AuthAPIKey, probe.AuthChatGPT, probe.AuthCustomProvider:
	default:
		t.Fatalf("authentication is unavailable or unsupported: mode=%q", authMode)
	}

	var models struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := client.Call(ctx, "model/list", map[string]any{"limit": 1, "includeHidden": false}, &models); err != nil {
		t.Fatalf("model/list: %v", safeClientError(client, err))
	}
	if len(models.Data) == 0 {
		t.Fatal("model/list returned no available models")
	}
	models.Data = nil

	var startResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := client.Call(ctx, "thread/start", map[string]any{
		"cwd":            workspace,
		"approvalPolicy": "never",
		"sandbox":        "read-only",
		"serviceName":    "yuanshu_ac003_credential_probe",
	}, &startResult); err != nil {
		t.Fatalf("thread/start: %v", safeClientError(client, err))
	}
	if startResult.Thread.ID == "" {
		t.Fatal("thread/start returned an empty thread id")
	}
	runner.threadID = startResult.Thread.ID

	turns := 0
	if turns >= credentialLiveTurnLimit {
		t.Fatalf("credential live turn limit %d exceeded", credentialLiveTurnLimit)
	}
	turns++
	var turnResult struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := client.Call(ctx, "turn/start", map[string]any{
		"threadId":       runner.threadID,
		"approvalPolicy": "never",
		"sandboxPolicy":  map[string]any{"type": "readOnly"},
		"input": []map[string]any{{
			"type": "text",
			"text": "Reply with exactly YUANSHU_CREDENTIAL_OK. Do not use tools or inspect files.",
		}},
	}, &turnResult); err != nil {
		t.Fatalf("turn/start: %v", safeClientError(client, err))
	}
	if turnResult.Turn.ID == "" {
		t.Fatal("turn/start returned an empty turn id")
	}
	runner.persisted = true

	methods := make(map[string]bool)
	agentLifecycle := false
	status := ""
	for status == "" {
		select {
		case message, ok := <-client.Messages():
			if !ok {
				t.Fatalf("app-server closed during credential Turn: %v", safeClientError(client, client.Err()))
			}
			methods[message.Method] = true
			if message.IsRequest() {
				_ = client.Respond(*message.ID, nil, &probe.RPCError{Code: -32601, Message: "tools are not supported by the AC-003 credential probe"})
				t.Fatalf("unexpected server request during credential Turn: %s", message.Method)
			}
			switch message.Method {
			case "item/agentMessage/delta":
				agentLifecycle = true
			case "item/started", "item/completed":
				itemType, err := credentialItemType(message)
				if err != nil {
					t.Fatal(err)
				}
				agentLifecycle = agentLifecycle || itemType == "agentMessage"
			case "turn/completed":
				var matches bool
				status, matches, err = completedTurnStatus(message, turnResult.Turn.ID)
				if err != nil {
					t.Fatal(err)
				}
				if !matches {
					status = ""
				}
			}
		case <-ctx.Done():
			t.Fatalf("wait for credential Turn: %v", ctx.Err())
		}
	}
	if status != "completed" {
		t.Fatalf("credential Turn status = %q, want completed", status)
	}
	if !agentLifecycle {
		t.Fatal("credential Turn did not expose the Agent message lifecycle")
	}
	if turns != credentialLiveTurnLimit {
		t.Fatalf("credential live probe used %d turns, want %d", turns, credentialLiveTurnLimit)
	}

	observed := make([]string, 0, len(methods))
	for method := range methods {
		observed = append(observed, method)
	}
	sort.Strings(observed)
	t.Logf("AC-003 credential result: codex=%s auth=%s transport=stdio turns=%d status=%s methods=%s", version, authMode, turns, status, strings.Join(observed, ","))
}

func credentialItemType(message probe.Message) (string, error) {
	var params struct {
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return "", fmt.Errorf("decode %s item type: %w", message.Method, err)
	}
	return params.Item.Type, nil
}
