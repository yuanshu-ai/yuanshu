package codex_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	liveEnvironment     = "YUANSHU_CODEX_LIVE"
	liveTurnLimit       = 2
	liveApprovalTimeout = 2 * time.Minute
	liveCallTimeout     = 30 * time.Second
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

type liveTurnBudget struct {
	used int
}

type liveRunner struct {
	client    *probe.Client
	observer  *liveObserver
	budget    *liveTurnBudget
	threadID  string
	persisted bool
}

type approvalScenario struct {
	name           string
	approvalMethod string
	turnParams     map[string]any
}

type liveScenarioResult struct {
	authMode       probe.AuthMode
	observer       *liveObserver
	name           string
	approvalMethod string
}

type heldApproval struct {
	rawID json.RawMessage
}

func TestLiveAppServerProtocol(t *testing.T) {
	if os.Getenv(liveEnvironment) != "1" {
		t.Skip("set YUANSHU_CODEX_LIVE=1 to run the bounded live Codex probe")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()

	versionOutput, err := exec.CommandContext(ctx, "codex", "--version").Output()
	if err != nil {
		t.Fatalf("read Codex version: %v", err)
	}
	version := requireKnownCodexVersion(t, versionOutput)

	scenarios := []approvalScenario{
		{
			name:           "command-approval",
			approvalMethod: "item/commandExecution/requestApproval",
			turnParams: map[string]any{
				"approvalPolicy": "untrusted",
				"sandboxPolicy":  map[string]any{"type": "readOnly"},
				"input": []map[string]any{{
					"type": "text",
					"text": `Use the shell exactly once to run: powershell -NoProfile -Command "Write-Output YUANSHU_ACTIVE_APPROVAL". Do not perform any other action.`,
				}},
			},
		},
		{
			name:           "file-change-approval",
			approvalMethod: "item/fileChange/requestApproval",
			turnParams: map[string]any{
				"approvalPolicy": "on-request",
				"sandboxPolicy":  map[string]any{"type": "readOnly"},
				"input": []map[string]any{{
					"type": "text",
					"text": "Create active-approval.txt containing YUANSHU_ACTIVE_FILE by applying a file change. Do not use the shell.",
				}},
			},
		},
	}

	budget := &liveTurnBudget{}
	var winner liveScenarioResult
	var failures []error
	for _, scenario := range scenarios {
		result, retryable, err := runActiveApprovalScenario(ctx, t.TempDir(), scenario, budget)
		if err == nil {
			winner = result
			break
		}
		failures = append(failures, fmt.Errorf("%s: %w", scenario.name, err))
		if !retryable || budget.used >= liveTurnLimit {
			break
		}
	}
	if winner.observer == nil {
		t.Fatalf("active-turn probe failed after %d turn(s): %v", budget.used, errors.Join(failures...))
	}
	if budget.used < 1 || budget.used > liveTurnLimit {
		t.Fatalf("live probe used %d turns, want 1..%d", budget.used, liveTurnLimit)
	}

	for _, method := range []string{
		"thread/started", "turn/started", "turn/completed",
		winner.approvalMethod, "serverRequest/resolved",
	} {
		if winner.observer.methods[method] == 0 {
			t.Errorf("live active-turn probe did not observe %s", method)
		}
	}

	methods := sortedObserved(winner.observer.methods)
	items := sortedObserved(winner.observer.itemTypes)
	t.Logf("AC-002 active-turn result: codex=%s auth=%s transport=stdio turns=%d scenario=%s methods=%s itemTypes=%s", version, winner.authMode, budget.used, winner.name, strings.Join(methods, ","), strings.Join(items, ","))
}

func runActiveApprovalScenario(ctx context.Context, workspace string, scenario approvalScenario, budget *liveTurnBudget) (result liveScenarioResult, retryable bool, err error) {
	runner := &liveRunner{observer: newLiveObserver(), budget: budget}
	client, err := startLiveClient(ctx, workspace)
	if err != nil {
		return result, false, fmt.Errorf("start live client: %w", err)
	}
	runner.client = client
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if cleanupErr := cleanupLiveScenario(cleanupCtx, runner, workspace); cleanupErr != nil {
			retryable = false
			err = errors.Join(err, fmt.Errorf("live scenario cleanup: %w", cleanupErr))
		}
	}()

	var accountResult json.RawMessage
	if err := client.Call(ctx, "account/read", map[string]any{"refreshToken": false}, &accountResult); err != nil {
		return result, false, fmt.Errorf("account/read: %w", safeClientError(client, err))
	}
	authMode, err := probe.ClassifyAuth(accountResult)
	if err != nil {
		return result, false, fmt.Errorf("classify authentication: %w", err)
	}
	if authMode != probe.AuthAPIKey {
		return result, false, fmt.Errorf("authentication mode = %q, want %q", authMode, probe.AuthAPIKey)
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
		return result, false, fmt.Errorf("thread/start: %w", safeClientError(client, err))
	}
	if startResult.Thread.ID == "" {
		return result, false, errors.New("thread/start returned an empty thread id")
	}
	runner.threadID = startResult.Thread.ID

	turnID, err := runner.startTurn(ctx, scenario.turnParams)
	if err != nil {
		return result, budget.used < liveTurnLimit, err
	}
	runner.persisted = true

	approvalCtx, approvalCancel := context.WithTimeout(ctx, liveApprovalTimeout)
	approval, err := runner.waitForApproval(approvalCtx, turnID, scenario.approvalMethod)
	approvalCancel()
	if err != nil {
		return result, true, err
	}

	var steerResult struct {
		TurnID string `json:"turnId"`
	}
	if err := callWithTimeout(ctx, client, "turn/steer", map[string]any{
		"threadId":       runner.threadID,
		"expectedTurnId": turnID,
		"input": []map[string]any{{
			"type": "text",
			"text": "After the pending action is handled, reply with YUANSHU_STEERED. Do not perform another tool call.",
		}},
	}, &steerResult); err != nil {
		return result, true, fmt.Errorf("turn/steer: %w", safeClientError(client, err))
	}
	if steerResult.TurnID != turnID {
		return result, true, errors.New("turn/steer returned a different turn id")
	}

	if err := callWithTimeout(ctx, client, "turn/interrupt", map[string]any{
		"threadId": runner.threadID,
		"turnId":   turnID,
	}, nil); err != nil {
		return result, true, fmt.Errorf("turn/interrupt: %w", safeClientError(client, err))
	}

	waitCtx, waitCancel := context.WithTimeout(ctx, 90*time.Second)
	err = runner.waitForInterruptedTurn(waitCtx, turnID, approval)
	waitCancel()
	if err != nil {
		return result, true, err
	}

	if err := verifyThreadDiscovery(ctx, client, runner.threadID); err != nil {
		return result, false, err
	}
	if err := runner.restartAndResume(ctx, workspace); err != nil {
		return result, false, err
	}

	return liveScenarioResult{authMode: authMode, observer: runner.observer, name: scenario.name, approvalMethod: scenario.approvalMethod}, false, nil
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

func (r *liveRunner) startTurn(ctx context.Context, overrides map[string]any) (string, error) {
	if r.budget.used >= liveTurnLimit {
		return "", fmt.Errorf("live turn limit %d exceeded", liveTurnLimit)
	}
	r.budget.used++
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

func (r *liveRunner) waitForApproval(ctx context.Context, turnID, method string) (heldApproval, error) {
	for {
		select {
		case message, ok := <-r.client.Messages():
			if !ok {
				return heldApproval{}, safeClientError(r.client, r.client.Err())
			}
			if err := r.observer.record(message); err != nil {
				return heldApproval{}, err
			}
			if message.IsRequest() {
				if message.Method != method {
					_ = r.client.Respond(*message.ID, nil, &probe.RPCError{Code: -32601, Message: "unsupported by AC-002 probe"})
					return heldApproval{}, fmt.Errorf("unexpected server request %s", message.Method)
				}
				var params struct {
					ThreadID string `json:"threadId"`
					TurnID   string `json:"turnId"`
				}
				if err := json.Unmarshal(message.Params, &params); err != nil {
					return heldApproval{}, fmt.Errorf("decode %s: %w", message.Method, err)
				}
				if params.ThreadID != r.threadID || params.TurnID != turnID {
					return heldApproval{}, errors.New("approval request did not match the active thread and turn")
				}
				rawID, err := json.Marshal(*message.ID)
				if err != nil {
					return heldApproval{}, fmt.Errorf("encode approval request id: %w", err)
				}
				return heldApproval{rawID: rawID}, nil
			}
			if message.Method == "turn/completed" {
				status, matches, err := completedTurnStatus(message, turnID)
				if err != nil {
					return heldApproval{}, err
				}
				if matches {
					return heldApproval{}, fmt.Errorf("turn completed with status %q before %s", status, method)
				}
			}
		case <-ctx.Done():
			return heldApproval{}, fmt.Errorf("wait for %s: %w", method, ctx.Err())
		}
	}
}

func (r *liveRunner) waitForInterruptedTurn(ctx context.Context, turnID string, approval heldApproval) error {
	resolved := false
	completed := false
	status := ""
	for !resolved || !completed {
		select {
		case message, ok := <-r.client.Messages():
			if !ok {
				return safeClientError(r.client, r.client.Err())
			}
			if err := r.observer.record(message); err != nil {
				return err
			}
			if message.IsRequest() {
				_ = r.client.Respond(*message.ID, nil, &probe.RPCError{Code: -32601, Message: "unsupported after turn interrupt"})
				return fmt.Errorf("unexpected server request %s after turn/interrupt", message.Method)
			}
			switch message.Method {
			case "serverRequest/resolved":
				matches, err := resolvedApprovalMatches(message, r.threadID, approval.rawID)
				if err != nil {
					return err
				}
				resolved = resolved || matches
			case "turn/completed":
				var matches bool
				var err error
				status, matches, err = completedTurnStatus(message, turnID)
				if err != nil {
					return err
				}
				completed = completed || matches
			}
		case <-ctx.Done():
			return fmt.Errorf("wait for interrupted turn and approval resolution: %w", ctx.Err())
		}
	}
	if status != "interrupted" {
		return fmt.Errorf("interrupted turn status = %q, want interrupted", status)
	}
	return nil
}

func (r *liveRunner) restartAndResume(ctx context.Context, workspace string) error {
	if err := r.client.Close(); err != nil && !errors.Is(err, probe.ErrClosed) {
		return fmt.Errorf("close before resume: %w", safeClientError(r.client, err))
	}
	client, err := startLiveClient(ctx, workspace)
	if err != nil {
		return fmt.Errorf("restart live client: %w", err)
	}
	r.client = client
	var resumeResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := client.Call(ctx, "thread/resume", map[string]any{"threadId": r.threadID}, &resumeResult); err != nil {
		return fmt.Errorf("thread/resume: %w", safeClientError(client, err))
	}
	if resumeResult.Thread.ID != r.threadID {
		return errors.New("thread/resume returned a different thread id")
	}
	return nil
}

func cleanupLiveScenario(ctx context.Context, runner *liveRunner, workspace string) error {
	var cleanupErrors []error
	if runner.client != nil {
		if err := runner.client.Close(); err != nil && !errors.Is(err, probe.ErrClosed) {
			cleanupErrors = append(cleanupErrors, safeClientError(runner.client, err))
		}
	}
	if runner.persisted {
		client, err := startLiveClient(ctx, workspace)
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("start archive client: %w", err))
		} else {
			if err := client.Call(ctx, "thread/archive", map[string]any{"threadId": runner.threadID}, nil); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("thread/archive: %w", safeClientError(client, err)))
			}
			if err := client.Close(); err != nil && !errors.Is(err, probe.ErrClosed) {
				cleanupErrors = append(cleanupErrors, safeClientError(client, err))
			}
		}
	}
	return errors.Join(cleanupErrors...)
}

func callWithTimeout(ctx context.Context, client *probe.Client, method string, params, result any) error {
	callCtx, cancel := context.WithTimeout(ctx, liveCallTimeout)
	defer cancel()
	return client.Call(callCtx, method, params, result)
}

func completedTurnStatus(message probe.Message, turnID string) (string, bool, error) {
	var params struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return "", false, fmt.Errorf("decode turn/completed: %w", err)
	}
	return params.Turn.Status, params.Turn.ID == turnID, nil
}

func resolvedApprovalMatches(message probe.Message, threadID string, requestID json.RawMessage) (bool, error) {
	var params struct {
		ThreadID  string          `json:"threadId"`
		RequestID json.RawMessage `json:"requestId"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return false, fmt.Errorf("decode serverRequest/resolved: %w", err)
	}
	return params.ThreadID == threadID && bytes.Equal(bytes.TrimSpace(params.RequestID), bytes.TrimSpace(requestID)), nil
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
