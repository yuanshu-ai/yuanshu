package codex_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex/probe"
)

const (
	liveEnvironment = "YUANSHU_CODEX_LIVE"
	liveTurnLimit   = 4
)

type liveObserver struct {
	methods   map[string]int
	itemTypes map[string]int
}

func newLiveObserver() *liveObserver {
	return &liveObserver{methods: make(map[string]int), itemTypes: make(map[string]int)}
}

func (o *liveObserver) record(message probe.Message) error {
	o.methods[message.Method]++
	if message.Method != "item/started" && message.Method != "item/completed" {
		return nil
	}
	var params struct {
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return fmt.Errorf("decode %s item type: %w", message.Method, err)
	}
	if params.Item.Type != "" {
		o.itemTypes[params.Item.Type]++
	}
	return nil
}

type liveRunner struct {
	client    *probe.Client
	observer  *liveObserver
	threadID  string
	turns     int
	persisted bool
}

func TestLiveAppServerProtocol(t *testing.T) {
	if os.Getenv(liveEnvironment) != "1" {
		t.Skip("set YUANSHU_CODEX_LIVE=1 to run the bounded live Codex probe")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	workspace := t.TempDir()

	versionOutput, err := exec.CommandContext(ctx, "codex", "--version").Output()
	if err != nil {
		t.Fatalf("read Codex version: %v", err)
	}
	version := strings.TrimSpace(string(versionOutput))
	if version != "codex-cli "+schemaVersion {
		t.Fatalf("Codex version = %q, want %q", version, "codex-cli "+schemaVersion)
	}

	runner := &liveRunner{observer: newLiveObserver()}
	client, err := startLiveClient(ctx, workspace)
	if err != nil {
		t.Fatalf("start live client: %v", err)
	}
	runner.client = client
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cleanupCancel()
		if runner.persisted {
			if err := archiveProbeThread(cleanupCtx, runner.client, workspace, runner.threadID); err != nil {
				t.Errorf("archive live Probe Thread: %v", err)
			}
		}
		if runner.client != nil {
			if err := runner.client.Close(); err != nil && !errors.Is(err, probe.ErrClosed) {
				t.Errorf("close live client: %v; stderr=%q", err, runner.client.Stderr())
			}
		}
	}()

	var accountResult json.RawMessage
	if err := client.Call(ctx, "account/read", map[string]any{"refreshToken": false}, &accountResult); err != nil {
		t.Fatalf("account/read: %v", safeClientError(client, err))
	}
	authMode, err := probe.ClassifyAuth(accountResult)
	if err != nil {
		t.Fatalf("classify authentication: %v", err)
	}
	if authMode != probe.AuthAPIKey {
		t.Fatalf("authentication mode = %q, want %q", authMode, probe.AuthAPIKey)
	}

	var startResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := client.Call(ctx, "thread/start", map[string]any{
		"cwd":            workspace,
		"approvalPolicy": "never",
		"sandbox":        "workspace-write",
		"serviceName":    "yuanshu_ac002_probe",
	}, &startResult); err != nil {
		t.Fatalf("thread/start: %v", safeClientError(client, err))
	}
	if startResult.Thread.ID == "" {
		t.Fatal("thread/start returned an empty thread id")
	}
	runner.threadID = startResult.Thread.ID

	commandApprovalBefore := runner.observer.methods["item/commandExecution/requestApproval"]
	status, err := runner.runTurn(ctx, map[string]any{
		"approvalPolicy": "untrusted",
		"sandboxPolicy":  map[string]any{"type": "readOnly"},
		"input": []map[string]any{{
			"type": "text",
			"text": `Use the shell exactly once to run: powershell -NoProfile -Command "Write-Output YUANSHU_COMMAND_APPROVAL". Do not perform any other action.`,
		}},
	})
	if err != nil {
		t.Fatalf("command approval turn: %v", err)
	}
	if status != "completed" || runner.observer.methods["item/commandExecution/requestApproval"] <= commandApprovalBefore {
		t.Fatalf("command approval status=%q requests=%d", status, runner.observer.methods["item/commandExecution/requestApproval"]-commandApprovalBefore)
	}
	runner.persisted = true
	if err := verifyThreadDiscovery(ctx, client, runner.threadID); err != nil {
		t.Fatal(err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("close before resume: %v; stderr=%q", err, client.Stderr())
	}
	client, err = startLiveClient(ctx, workspace)
	if err != nil {
		t.Fatalf("restart live client: %v", err)
	}
	runner.client = client
	var resumeResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := client.Call(ctx, "thread/resume", map[string]any{"threadId": runner.threadID}, &resumeResult); err != nil {
		t.Fatalf("thread/resume: %v", safeClientError(client, err))
	}
	if resumeResult.Thread.ID != runner.threadID {
		t.Fatal("thread/resume returned a different thread id")
	}

	deniedFile := filepath.Join(workspace, "approval-denied.txt")
	fileApprovalBefore := runner.observer.methods["item/fileChange/requestApproval"]
	status, err = runner.runTurn(ctx, map[string]any{
		"approvalPolicy": "on-request",
		"sandboxPolicy":  map[string]any{"type": "readOnly"},
		"input": []map[string]any{{
			"type": "text",
			"text": "Create approval-denied.txt containing YUANSHU_FILE_APPROVAL by applying a file change. Do not use the shell.",
		}},
	})
	if err != nil {
		t.Fatalf("file approval turn: %v", err)
	}
	if status != "completed" || runner.observer.methods["item/fileChange/requestApproval"] <= fileApprovalBefore {
		t.Fatalf("file approval status=%q requests=%d", status, runner.observer.methods["item/fileChange/requestApproval"]-fileApprovalBefore)
	}
	if _, err := os.Stat(deniedFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("declined file change created a file: %v", err)
	}

	commandItemBefore := runner.observer.itemTypes["commandExecution"]
	fileItemBefore := runner.observer.itemTypes["fileChange"]
	diffBefore := runner.observer.methods["turn/diff/updated"]
	status, err = runner.runTurn(ctx, map[string]any{
		"approvalPolicy": "never",
		"sandboxPolicy": map[string]any{
			"type":          "workspaceWrite",
			"writableRoots": []string{workspace},
			"networkAccess": false,
		},
		"input": []map[string]any{{
			"type": "text",
			"text": `First run: powershell -NoProfile -Command "Write-Output YUANSHU_COMMAND_EVENT". Then create probe-output.txt containing exactly YUANSHU_FILE_EVENT by applying a file change. Perform both actions and nothing else.`,
		}},
	})
	if err != nil {
		t.Fatalf("command and file event turn: %v", err)
	}
	if status != "completed" || runner.observer.itemTypes["commandExecution"] <= commandItemBefore || runner.observer.itemTypes["fileChange"] <= fileItemBefore || runner.observer.methods["turn/diff/updated"] <= diffBefore {
		t.Fatalf("command/file event status=%q commandItems=%d fileItems=%d diffs=%d", status, runner.observer.itemTypes["commandExecution"]-commandItemBefore, runner.observer.itemTypes["fileChange"]-fileItemBefore, runner.observer.methods["turn/diff/updated"]-diffBefore)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "probe-output.txt"))
	if err != nil || strings.TrimSpace(string(content)) != "YUANSHU_FILE_EVENT" {
		t.Fatalf("probe output file mismatch: err=%v", err)
	}

	turnID, err := runner.startTurn(ctx, map[string]any{
		"approvalPolicy": "never",
		"sandboxPolicy": map[string]any{
			"type":          "workspaceWrite",
			"writableRoots": []string{workspace},
			"networkAccess": false,
		},
		"input": []map[string]any{{
			"type": "text",
			"text": "Run a PowerShell Start-Sleep command for 30 seconds, then reply with YUANSHU_SLEEP_DONE. Do not perform any other action.",
		}},
	})
	if err != nil {
		t.Fatalf("start steer/interrupt turn: %v", err)
	}
	var steerResult struct {
		TurnID string `json:"turnId"`
	}
	if err := client.Call(ctx, "turn/steer", map[string]any{
		"threadId":       runner.threadID,
		"expectedTurnId": turnID,
		"input":          []map[string]any{{"type": "text", "text": "After the wait, also mention YUANSHU_STEERED."}},
	}, &steerResult); err != nil {
		t.Fatalf("turn/steer: %v", safeClientError(client, err))
	}
	if steerResult.TurnID != turnID {
		t.Fatal("turn/steer returned a different turn id")
	}
	if err := client.Call(ctx, "turn/interrupt", map[string]any{"threadId": runner.threadID, "turnId": turnID}, nil); err != nil {
		t.Fatalf("turn/interrupt: %v", safeClientError(client, err))
	}
	status, err = runner.waitForTurn(ctx, turnID)
	if err != nil {
		t.Fatalf("wait for interrupted turn: %v", err)
	}
	if status != "interrupted" {
		t.Fatalf("interrupted turn status = %q, want interrupted", status)
	}

	for _, method := range []string{
		"thread/started", "turn/started", "turn/completed", "item/started", "item/completed",
		"item/agentMessage/delta", "item/commandExecution/requestApproval",
		"item/fileChange/requestApproval", "serverRequest/resolved", "turn/diff/updated",
	} {
		if runner.observer.methods[method] == 0 {
			t.Errorf("live probe did not observe %s", method)
		}
	}
	if runner.turns != liveTurnLimit {
		t.Fatalf("live probe used %d turns, want exactly %d", runner.turns, liveTurnLimit)
	}

	methods := sortedObserved(runner.observer.methods)
	items := sortedObserved(runner.observer.itemTypes)
	t.Logf("AC-002 live result: codex=%s auth=%s transport=stdio turns=%d methods=%s itemTypes=%s", schemaVersion, authMode, runner.turns, strings.Join(methods, ","), strings.Join(items, ","))
}

func startLiveClient(ctx context.Context, workspace string) (*probe.Client, error) {
	client, err := probe.Start(ctx, probe.Options{Dir: workspace, Env: probe.Environment()})
	if err != nil {
		return nil, err
	}
	title := "Yuanshu AC-002 Probe"
	if _, err := client.Initialize(ctx, probe.ClientInfo{Name: "yuanshu_probe", Title: &title, Version: "0.0.0"}); err != nil {
		_ = client.Close()
		return nil, safeClientError(client, err)
	}
	return client, nil
}

func verifyThreadDiscovery(ctx context.Context, client *probe.Client, threadID string) error {
	var read struct {
		Thread struct {
			ID    string `json:"id"`
			Turns []any  `json:"turns"`
		} `json:"thread"`
	}
	if err := client.Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &read); err != nil {
		return fmt.Errorf("thread/read: %w", safeClientError(client, err))
	}
	if read.Thread.ID != threadID || read.Thread.Turns == nil {
		return errors.New("thread/read did not return the expected thread with turns")
	}

	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		list.Data = nil
		if err := client.Call(ctx, "thread/list", map[string]any{
			"limit":       100,
			"sourceKinds": []string{"cli", "vscode", "exec", "appServer", "subAgent", "subAgentReview", "subAgentCompact", "subAgentThreadSpawn", "subAgentOther", "unknown"},
		}, &list); err != nil {
			return fmt.Errorf("thread/list: %w", safeClientError(client, err))
		}
		for _, thread := range list.Data {
			if thread.ID == threadID {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("thread/list did not return the Probe Thread")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (r *liveRunner) runTurn(ctx context.Context, overrides map[string]any) (string, error) {
	turnID, err := r.startTurn(ctx, overrides)
	if err != nil {
		return "", err
	}
	return r.waitForTurn(ctx, turnID)
}

func (r *liveRunner) startTurn(ctx context.Context, overrides map[string]any) (string, error) {
	if r.turns >= liveTurnLimit {
		return "", fmt.Errorf("live turn limit %d exceeded", liveTurnLimit)
	}
	r.turns++
	params := make(map[string]any, len(overrides)+1)
	params["threadId"] = r.threadID
	for key, value := range overrides {
		params[key] = value
	}
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := r.client.Call(ctx, "turn/start", params, &result); err != nil {
		return "", safeClientError(r.client, err)
	}
	if result.Turn.ID == "" {
		return "", errors.New("turn/start returned an empty turn id")
	}
	return result.Turn.ID, nil
}

func (r *liveRunner) waitForTurn(ctx context.Context, turnID string) (string, error) {
	for {
		select {
		case message, ok := <-r.client.Messages():
			if !ok {
				return "", safeClientError(r.client, r.client.Err())
			}
			if err := r.observer.record(message); err != nil {
				return "", err
			}
			if message.IsRequest() {
				switch message.Method {
				case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
					if err := r.client.Respond(*message.ID, map[string]any{"decision": "decline"}, nil); err != nil {
						return "", err
					}
				default:
					_ = r.client.Respond(*message.ID, nil, &probe.RPCError{Code: -32601, Message: "unsupported by AC-002 probe"})
					return "", fmt.Errorf("unexpected server request %s", message.Method)
				}
			}
			if message.Method == "turn/completed" {
				var params struct {
					Turn struct {
						ID     string `json:"id"`
						Status string `json:"status"`
					} `json:"turn"`
				}
				if err := json.Unmarshal(message.Params, &params); err != nil {
					return "", fmt.Errorf("decode turn/completed: %w", err)
				}
				if params.Turn.ID == turnID {
					return params.Turn.Status, nil
				}
			}
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func archiveProbeThread(ctx context.Context, client *probe.Client, workspace, threadID string) error {
	if client == nil || client.Err() != nil {
		var err error
		client, err = startLiveClient(ctx, workspace)
		if err != nil {
			return err
		}
		defer client.Close()
	}
	return client.Call(ctx, "thread/archive", map[string]any{"threadId": threadID}, nil)
}

func safeClientError(client *probe.Client, err error) error {
	if err == nil {
		return nil
	}
	stderr := strings.TrimSpace(client.Stderr())
	if stderr == "" {
		return err
	}
	return fmt.Errorf("%w (redacted app-server stderr: %s)", err, stderr)
}

func sortedObserved(counts map[string]int) []string {
	result := make([]string, 0, len(counts))
	for name, count := range counts {
		if count > 0 {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}
