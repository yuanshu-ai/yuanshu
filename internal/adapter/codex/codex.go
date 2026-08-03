// Package codex implements Yuanshu's formal Codex AgentAdapter over the
// stable app-server stdio transport selected by ADR-012.
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex/appserver"
	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/node/workspace"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

const (
	AdapterID       = "codex"
	BaselineVersion = "0.144.6"
	ProtocolVersion = "stable-v2"
)

type WorkspaceResolver interface {
	Resolve(context.Context, string) (workspace.ResolvedWorkspace, error)
	ResolvePath(context.Context, string, string, workspace.PathIntent) (workspace.ResolvedPath, error)
}

type RuntimeThreadStore interface {
	SaveRuntimeThread(context.Context, store.RuntimeThreadRecord) error
	RuntimeThread(context.Context, string) (store.RuntimeThreadRecord, error)
	RuntimeThreads(context.Context) ([]store.RuntimeThreadRecord, error)
}

type Options struct {
	Config          config.CodexAdapterConfig
	Processes       platform.ProcessManager
	Workspaces      WorkspaceResolver
	Threads         RuntimeThreadStore
	EventCapacity   int
	ApprovalTimeout time.Duration
}

type Adapter struct{ options Options }

var _ adapter.AgentAdapter = (*Adapter)(nil)

func New(options Options) (*Adapter, error) {
	if !options.Config.Enabled || options.Config.RuntimeMode != "stdio" || strings.TrimSpace(options.Config.Binary) == "" ||
		options.Processes == nil || options.Workspaces == nil || options.Threads == nil {
		return nil, adapter.ErrInvalid
	}
	if !options.Processes.Available() {
		return nil, adapter.ErrUnavailable
	}
	if options.EventCapacity < 0 || options.ApprovalTimeout < 0 {
		return nil, adapter.ErrInvalid
	}
	if options.EventCapacity == 0 {
		options.EventCapacity = 256
	}
	if options.ApprovalTimeout == 0 {
		options.ApprovalTimeout = 5 * time.Minute
	}
	return &Adapter{options: options}, nil
}

func (*Adapter) ID() string { return AdapterID }

func (*Adapter) Capabilities() adapter.CapabilitySet {
	return adapter.CapabilitySet{
		ThreadList: true, ThreadRead: true, ThreadStart: true, ThreadResume: true,
		TurnStart: true, TurnSteer: true, TurnInterrupt: true, Approvals: true,
		CommandEvents: true, ToolEvents: true, FileChanges: true, FileDiff: true,
	}
}

func (a *Adapter) Detect(ctx context.Context) (adapter.Installation, error) {
	output, err := a.runVersion(ctx)
	if err != nil {
		return adapter.Installation{}, err
	}
	version, ok := strings.CutPrefix(strings.TrimSpace(output), "codex-cli ")
	if !ok || version == "" {
		return adapter.Installation{}, adapter.ErrUnavailable
	}
	profile, compatible := compatibilityForVersion(version)
	if !compatible {
		return adapter.Installation{Detected: true, Version: version, Protocol: ProtocolVersion}, adapter.ErrUnsupported
	}
	return adapter.Installation{Detected: true, Version: version, Protocol: profile.Protocol}, nil
}

func (a *Adapter) StartRuntime(ctx context.Context) (adapter.Runtime, error) {
	installation, err := a.Detect(ctx)
	if err != nil {
		return nil, err
	}
	runtime := newRuntime(a.options, installation)
	if err := runtime.startClient(ctx); err != nil {
		return nil, err
	}
	return runtime, nil
}

func (a *Adapter) runVersion(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", context.Canceled
	}
	executable, prefix, err := resolveConfiguredCommand(a.options.Config.Binary)
	if err != nil {
		return "", adapter.ErrUnavailable
	}
	process, err := a.options.Processes.Start(ctx, platform.ProcessSpec{
		Executable: executable,
		Args:       append(prefix, "--version"),
		Env:        a.environment(),
	})
	if err != nil {
		return "", adapter.ErrUnavailable
	}
	_ = process.Stdin().Close()
	var stdout bytes.Buffer
	var wait sync.WaitGroup
	wait.Add(2)
	go func() { defer wait.Done(); _, _ = io.CopyN(&stdout, process.Stdout(), 4097) }()
	go func() { defer wait.Done(); _, _ = io.Copy(io.Discard, process.Stderr()) }()
	exit, waitErr := process.Wait(ctx)
	wait.Wait()
	if waitErr != nil || exit.Code != 0 || stdout.Len() > 4096 {
		return "", adapter.ErrUnavailable
	}
	return stdout.String(), nil
}

func (a *Adapter) environment() []string { return runtimeEnvironment(a.options.Config) }

func runtimeEnvironment(configuration config.CodexAdapterConfig) []string {
	if configuration.Home == "" {
		return nil
	}
	environment := os.Environ()
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.EqualFold(strings.SplitN(item, "=", 2)[0], "CODEX_HOME") {
			result = append(result, item)
		}
	}
	return append(result, "CODEX_HOME="+configuration.Home)
}

type authMode string

const (
	authNone    authMode = "none"
	authAPIKey  authMode = "api_key"
	authChatGPT authMode = "chatgpt"
	authCustom  authMode = "custom_provider"
	authOther   authMode = "other"
)

func classifyAuth(raw json.RawMessage) (authMode, error) {
	var envelope struct {
		Account json.RawMessage `json:"account"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return "", appserver.ErrInvalidMessage
	}
	if len(bytes.TrimSpace(envelope.Account)) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Account), []byte("null")) {
		return authNone, nil
	}
	var account struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(envelope.Account, &account) != nil {
		return "", appserver.ErrInvalidMessage
	}
	switch account.Type {
	case "apiKey", "apikey":
		return authAPIKey, nil
	case "chatgpt", "chatgptAuthTokens":
		return authChatGPT, nil
	case "amazonBedrock", "bedrockApiKey":
		return authCustom, nil
	case "":
		return authNone, nil
	default:
		return authOther, nil
	}
}

func mapCallError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var rpcError *appserver.RPCError
	if errors.As(err, &rpcError) {
		if rpcError.Code == -32601 {
			return adapter.ErrUnsupported
		}
		return adapter.ErrConflict
	}
	if errors.Is(err, appserver.ErrProcessExited) || errors.Is(err, appserver.ErrClosed) {
		return adapter.ErrAmbiguous
	}
	return adapter.ErrUnavailable
}
