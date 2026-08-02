// Package adapter defines the platform-neutral Agent boundary owned by a
// Yuanshu Node. It does not expose local filesystem paths or runtime protocol
// request identifiers.
package adapter

import (
	"context"
	"errors"
	"time"

	protocol "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

var (
	ErrInvalid              = errors.New("agent request is invalid")
	ErrUnavailable          = errors.New("agent runtime is unavailable")
	ErrUnsupported          = errors.New("agent capability is unsupported")
	ErrNotFound             = errors.New("agent resource was not found")
	ErrConflict             = errors.New("agent request conflicts with runtime state")
	ErrForbidden            = errors.New("agent request is forbidden by local policy")
	ErrReconciliationNeeded = errors.New("agent state requires reconciliation")
	ErrAmbiguous            = errors.New("agent operation result is ambiguous")
	ErrClosed               = errors.New("agent runtime is closed")
)

type Installation struct {
	Detected       bool
	Version        string
	Protocol       string
	Authentication string
}

type CapabilitySet struct {
	ThreadList    bool
	ThreadRead    bool
	ThreadStart   bool
	ThreadResume  bool
	TurnStart     bool
	TurnSteer     bool
	TurnInterrupt bool
	Approvals     bool
	CommandEvents bool
	ToolEvents    bool
	FileChanges   bool
	FileDiff      bool
	Images        bool
	Attachments   bool
}

type AgentAdapter interface {
	ID() string
	Detect(ctx context.Context) (Installation, error)
	Capabilities() CapabilitySet
	StartRuntime(ctx context.Context) (Runtime, error)
}

type Runtime interface {
	ListThreads(ctx context.Context, request ListThreadsRequest) (ThreadPage, error)
	ReadThread(ctx context.Context, request ReadThreadRequest) (ThreadSnapshot, error)
	StartThread(ctx context.Context, request StartThreadRequest) (Thread, error)
	ResumeThread(ctx context.Context, request ResumeThreadRequest) (Thread, error)
	StartTurn(ctx context.Context, request StartTurnRequest) (Turn, error)
	SteerTurn(ctx context.Context, request SteerTurnRequest) error
	InterruptTurn(ctx context.Context, request InterruptTurnRequest) error
	ResolveApproval(ctx context.Context, decision ApprovalDecision) error
	Events() <-chan AgentEvent
	Health() HealthStatus
	Close(ctx context.Context) error
}

type ListThreadsRequest struct {
	WorkspaceID string
	Cursor      string
	Limit       int
}

type ReadThreadRequest struct {
	WorkspaceID  string
	ThreadID     string
	IncludeTurns bool
}

type StartThreadRequest struct{ WorkspaceID string }

type ResumeThreadRequest struct {
	WorkspaceID string
	ThreadID    string
}

type StartTurnRequest struct {
	WorkspaceID string
	ThreadID    string
	Input       string
}

type SteerTurnRequest struct {
	WorkspaceID string
	ThreadID    string
	TurnID      string
	Input       string
}

type InterruptTurnRequest struct {
	WorkspaceID string
	ThreadID    string
	TurnID      string
}

type ApprovalDecision struct {
	WorkspaceID string
	ThreadID    string
	TurnID      string
	ItemID      string
	ApprovalID  string
	Decision    string
}

type Thread struct {
	ID          string
	WorkspaceID string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type ThreadPage struct {
	Data       []Thread
	NextCursor string
}

type ThreadSnapshot struct {
	Thread Thread
	Turns  []Turn
}

type Turn struct {
	ID       string
	ThreadID string
	Status   string
}

type Approval struct {
	ID        string
	Kind      string
	Summary   string
	Operation any
	ExpiresAt time.Time
}

type AgentEvent struct {
	Type          protocol.EventType
	CorrelationID string
	WorkspaceID   string
	ThreadID      string
	TurnID        string
	ItemID        string
	Payload       any
	Approval      *Approval
	Ambiguous     bool
}

type HealthStatus struct {
	State          string
	CodexVersion   string
	Protocol       string
	Authentication string
	FailureCode    string
}
