package v11

// Yuanshu Protocol v1.1 Agent-neutral control and event envelope.
type YuanshuMessage struct {
	ActivityID      *string                `json:"activityId,omitempty"`
	AgentInstanceID *string                `json:"agentInstanceId,omitempty"`
	CorrelationID   string                 `json:"correlationId"`
	ExpiresAt       *string                `json:"expiresAt,omitempty"`
	InteractionID   *string                `json:"interactionId,omitempty"`
	MessageID       string                 `json:"messageId"`
	NodeID          string                 `json:"nodeId"`
	Nonce           *string                `json:"nonce,omitempty"`
	OwnerID         string                 `json:"ownerId"`
	Payload         map[string]interface{} `json:"payload"`
	ProtocolVersion ProtocolVersion        `json:"protocolVersion"`
	RunID           *string                `json:"runId,omitempty"`
	SentAt          string                 `json:"sentAt"`
	Sequence        int64                  `json:"sequence"`
	Signature       *string                `json:"signature,omitempty"`
	Signer          *Signer                `json:"signer,omitempty"`
	StreamID        string                 `json:"streamId"`
	TaskID          *string                `json:"taskId,omitempty"`
	Type            Type                   `json:"type"`
	WorkspaceID     *string                `json:"workspaceId,omitempty"`
}

type Signer struct {
	ClientID string `json:"clientId"`
	KeyID    string `json:"keyId"`
}

type ProtocolVersion string

const (
	The11 ProtocolVersion = "1.1"
)

type Type string

const (
	ActivityCompleted         Type = "activity.completed"
	ActivityStarted           Type = "activity.started"
	ActivityUpdated           Type = "activity.updated"
	AgentList                 Type = "agent.list"
	AgentRead                 Type = "agent.read"
	AgentSnapshot             Type = "agent.snapshot"
	AgentStatus               Type = "agent.status"
	ConfigRead                Type = "config.read"
	ConfigUpdate              Type = "config.update"
	ControlResult             Type = "control.result"
	DeviceStatus              Type = "device.status"
	DeviceSync                Type = "device.sync"
	DiffUpdated               Type = "diff.updated"
	Error                     Type = "error"
	EventsReplay              Type = "events.replay"
	FileChanged               Type = "file.changed"
	HistoryGap                Type = "history.gap"
	InteractionRequested      Type = "interaction.requested"
	InteractionResolve        Type = "interaction.resolve"
	InteractionResolved       Type = "interaction.resolved"
	LeaseAcquire              Type = "lease.acquire"
	LeaseChanged              Type = "lease.changed"
	LeaseRelease              Type = "lease.release"
	LeaseRenew                Type = "lease.renew"
	LeaseStatus               Type = "lease.status"
	MessageCompleted          Type = "message.completed"
	MessageDelta              Type = "message.delta"
	NotificationsList         Type = "notifications.list"
	NotificationsRead         Type = "notifications.read"
	PlanUpdated               Type = "plan.updated"
	ReasoningSummaryCompleted Type = "reasoning.summary.completed"
	ReasoningSummaryDelta     Type = "reasoning.summary.delta"
	RunCompleted              Type = "run.completed"
	RunFailed                 Type = "run.failed"
	RunInterrupt              Type = "run.interrupt"
	RunInterrupted            Type = "run.interrupted"
	RunStart                  Type = "run.start"
	RunStarted                Type = "run.started"
	RunSteer                  Type = "run.steer"
	RuntimeStatus             Type = "runtime.status"
	SnapshotRequest           Type = "snapshot.request"
	TaskList                  Type = "task.list"
	TaskRead                  Type = "task.read"
	TaskResume                Type = "task.resume"
	TaskSnapshot              Type = "task.snapshot"
	TaskStart                 Type = "task.start"
	TaskStarted               Type = "task.started"
	TaskUpdated               Type = "task.updated"
	WorkspaceList             Type = "workspace.list"
)
