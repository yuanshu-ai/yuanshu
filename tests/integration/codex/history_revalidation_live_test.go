package codex_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex/probe"
)

const historyRevalidationLiveEnvironment = "YUANSHU_CODEX_HISTORY_LIVE"

// TestPersistedHistoryLive revalidates PF-084 L2/L3 without treating an
// explicit app-server endpoint as an attached production Runtime. Phase one
// creates no Turn. Phase two creates at most one bounded, tool-free Turn and
// only runs after independent empty-history discovery has succeeded.
func TestPersistedHistoryLive(t *testing.T) {
	if os.Getenv(historyRevalidationLiveEnvironment) != "1" {
		t.Skip("set YUANSHU_CODEX_HISTORY_LIVE=1 to run the bounded persisted-history revalidation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	versionOutput, err := exec.CommandContext(ctx, "codex", "--version").Output()
	if err != nil {
		t.Fatal("Codex version is unavailable")
	}
	version := requireKnownCodexVersion(t, versionOutput)
	workspace := liveWorkspace(t)
	threadID := ""
	turnID := ""
	archived := false
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cleanupCancel()
		if threadID == "" || archived {
			return
		}
		client, startErr := startHistoryClient(cleanupCtx, workspace, "cleanup")
		if startErr != nil {
			return
		}
		defer client.Close()
		if turnID != "" {
			_ = client.Call(cleanupCtx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil)
		}
		_ = client.Call(cleanupCtx, "thread/archive", map[string]any{"threadId": threadID}, nil)
	}()

	creator, err := startHistoryClient(ctx, workspace, "creator")
	if err != nil {
		t.Fatal("start empty-history creator")
	}
	threadID, err = createEmptyHistoryThread(ctx, creator, workspace)
	if err != nil {
		_ = creator.Close()
		t.Fatal("create bounded empty Thread")
	}
	if err := creator.Close(); err != nil && !errors.Is(err, probe.ErrClosed) {
		t.Fatal("close empty-history creator")
	}

	emptyReader, err := startHistoryClient(ctx, workspace, "empty_reader")
	if err != nil {
		t.Fatal("start independent empty-history reader")
	}
	l2State := listHistoryThread(ctx, emptyReader, workspace, threadID, false, true)
	l2Scan := listHistoryThread(ctx, emptyReader, workspace, threadID, false, false)
	emptyRead := readHistoryThread(ctx, emptyReader, workspace, threadID)
	if err := emptyReader.Close(); err != nil && !errors.Is(err, probe.ErrClosed) {
		t.Fatal("close independent empty-history reader")
	}
	if !l2State || !l2Scan || !emptyRead.Valid || len(emptyRead.Turns) != 0 {
		t.Logf("PF-084R zero-Turn evidence: codex=%s l2_state=%t l2_scan=%t l3_empty=%t turns=%d", version, l2State, l2Scan, emptyRead.Valid, len(emptyRead.Turns))
		t.Fatal("independent app-server did not discover and read the empty persisted Thread")
	}

	runner, err := startHistoryClient(ctx, workspace, "single_turn")
	if err != nil {
		t.Fatal("start bounded history runner")
	}
	if err := resumeHistoryThread(ctx, runner, workspace, threadID); err != nil {
		_ = runner.Close()
		t.Fatal("resume bounded history Thread")
	}
	turnID, err = startHistoryTurn(ctx, runner, workspace, threadID)
	if err != nil {
		_ = runner.Close()
		t.Fatal("start bounded history Turn")
	}
	terminal, itemCount := waitForHistoryTurn(ctx, runner, workspace, threadID, turnID)
	if !terminal {
		_ = runner.Call(context.Background(), "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil)
		_ = runner.Close()
		t.Fatal("bounded history Turn did not reach a confirmed terminal state")
	}
	if err := runner.Close(); err != nil && !errors.Is(err, probe.ErrClosed) {
		t.Fatal("close bounded history runner")
	}

	verifier, err := startHistoryClient(ctx, workspace, "full_reader")
	if err != nil {
		t.Fatal("start independent full-history reader")
	}
	l2Full := listHistoryThread(ctx, verifier, workspace, threadID, false, true) && listHistoryThread(ctx, verifier, workspace, threadID, false, false)
	fullRead := readHistoryThread(ctx, verifier, workspace, threadID)
	l3Full := fullRead.Valid && fullRead.hasTurn(turnID) && fullRead.itemCount() >= itemCount && fullRead.itemCount() > 0
	if !l2Full || !l3Full {
		_ = verifier.Close()
		t.Logf("PF-084R full-history evidence: codex=%s l2=%t l3=%t turns=%d items=%d", version, l2Full, l3Full, len(fullRead.Turns), fullRead.itemCount())
		t.Fatal("independent app-server did not preserve the bounded Turn history")
	}
	if err := verifier.Call(ctx, "thread/archive", map[string]any{"threadId": threadID}, nil); err != nil {
		_ = verifier.Close()
		t.Fatal("archive bounded history Thread")
	}
	archived = true
	archivedList := listHistoryThread(ctx, verifier, workspace, threadID, true, true)
	archivedRead := readHistoryThread(ctx, verifier, workspace, threadID)
	if err := verifier.Close(); err != nil && !errors.Is(err, probe.ErrClosed) {
		t.Fatal("close independent full-history reader")
	}
	if !archivedList || !archivedRead.Valid || !archivedRead.hasTurn(turnID) {
		t.Fatal("archived bounded history was not readable")
	}

	t.Logf("PF-084R persisted-history evidence: codex=%s l2_state=true l2_scan=true l3_empty=true l3_turn=true archived=true turns=1", version)
}

func startHistoryClient(ctx context.Context, workspace, name string) (*probe.Client, error) {
	client, err := probe.Start(ctx, probe.Options{Dir: workspace, Env: probe.Environment()})
	if err != nil {
		return nil, err
	}
	if _, err := client.Initialize(ctx, probe.ClientInfo{Name: "yuanshu_pf084r_" + name, Version: "0.0.0"}); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func createEmptyHistoryThread(ctx context.Context, client *probe.Client, workspace string) (string, error) {
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	err := client.Call(ctx, "thread/start", map[string]any{
		"cwd": workspace, "approvalPolicy": "never", "sandbox": "read-only",
		"serviceName": "yuanshu_pf084r_history", "ephemeral": false,
	}, &result)
	if err != nil || result.Thread.ID == "" {
		return "", errors.New("empty Thread creation failed")
	}
	return result.Thread.ID, nil
}

func resumeHistoryThread(ctx context.Context, client *probe.Client, workspace, threadID string) error {
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := client.Call(ctx, "thread/resume", map[string]any{
		"threadId": threadID, "cwd": workspace, "approvalPolicy": "never", "sandbox": "read-only",
	}, &result); err != nil || result.Thread.ID != threadID {
		return errors.New("Thread resume failed")
	}
	return nil
}

func startHistoryTurn(ctx context.Context, client *probe.Client, workspace, threadID string) (string, error) {
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	err := client.Call(ctx, "turn/start", map[string]any{
		"threadId": threadID, "cwd": workspace, "approvalPolicy": "never",
		"sandboxPolicy": map[string]any{"type": "readOnly"},
		"input":         []map[string]string{{"type": "text", "text": "Reply with exactly YUANSHU_PF084R_HISTORY_OK. Do not use tools."}},
	}, &result)
	if err != nil || result.Turn.ID == "" {
		return "", errors.New("bounded Turn creation failed")
	}
	return result.Turn.ID, nil
}

type historyRead struct {
	Valid bool
	Turns []historyTurn
}

type historyTurn struct {
	ID     string            `json:"id"`
	Status string            `json:"status"`
	Items  []json.RawMessage `json:"items"`
}

func readHistoryThread(ctx context.Context, client *probe.Client, workspace, threadID string) historyRead {
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var result struct {
		Thread struct {
			ID    string        `json:"id"`
			Cwd   string        `json:"cwd"`
			Turns []historyTurn `json:"turns"`
		} `json:"thread"`
	}
	if client.Call(callCtx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &result) != nil {
		return historyRead{}
	}
	return historyRead{Valid: result.Thread.ID == threadID && sameHistoryPath(result.Thread.Cwd, workspace) && result.Thread.Turns != nil, Turns: result.Thread.Turns}
}

func listHistoryThread(ctx context.Context, client *probe.Client, workspace, threadID string, archived, stateOnly bool) bool {
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var result struct {
		Data []struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		} `json:"data"`
	}
	if client.Call(callCtx, "thread/list", map[string]any{
		"limit": 100, "archived": archived, "cwd": workspace,
		"sourceKinds": attachmentSourceKinds(), "useStateDbOnly": stateOnly,
	}, &result) != nil {
		return false
	}
	for _, thread := range result.Data {
		if thread.ID == threadID && sameHistoryPath(thread.Cwd, workspace) {
			return true
		}
	}
	return false
}

func waitForHistoryTurn(ctx context.Context, client *probe.Client, workspace, threadID, turnID string) (bool, int) {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		read := readHistoryThread(ctx, client, workspace, threadID)
		for _, turn := range read.Turns {
			if turn.ID != turnID {
				continue
			}
			switch turn.Status {
			case "completed":
				return true, len(turn.Items)
			case "failed", "interrupted":
				return false, len(turn.Items)
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false, 0
}

func (h historyRead) hasTurn(id string) bool {
	for _, turn := range h.Turns {
		if turn.ID == id && turn.Status == "completed" {
			return true
		}
	}
	return false
}

func (h historyRead) itemCount() int {
	count := 0
	for _, turn := range h.Turns {
		count += len(turn.Items)
	}
	return count
}

func sameHistoryPath(left, right string) bool {
	leftClean, leftErr := filepath.Abs(left)
	rightClean, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && filepath.Clean(leftClean) == filepath.Clean(rightClean)
}
