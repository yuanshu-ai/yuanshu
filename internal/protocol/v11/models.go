package v11

// CapabilityDescriptor reports what the selected Agent instance can actually
// do. A detected installation must not be promoted to a controllable runtime.
type CapabilityDescriptor struct {
	ID     string `json:"id"`
	Level  string `json:"level"`
	Reason string `json:"reason,omitempty"`
}

type AgentSummary struct {
	ID                       string                 `json:"id"`
	AdapterType              string                 `json:"adapterType"`
	DisplayName              string                 `json:"displayName"`
	Version                  string                 `json:"version,omitempty"`
	RuntimeMode              string                 `json:"runtimeMode"`
	Status                   string                 `json:"status"`
	ProviderType             string                 `json:"providerType,omitempty"`
	CustomEndpoint           bool                   `json:"customEndpoint,omitempty"`
	AuthenticationAvailable  bool                   `json:"authenticationAvailable,omitempty"`
	ConfigurationFingerprint string                 `json:"configurationFingerprint,omitempty"`
	Capabilities             []CapabilityDescriptor `json:"capabilities"`
}

type TaskSummary struct {
	ID                  string `json:"id"`
	AgentInstanceID     string `json:"agentInstanceId"`
	WorkspaceID         string `json:"workspaceId"`
	Status              string `json:"status"`
	Title               string `json:"title,omitempty"`
	Preview             string `json:"preview,omitempty"`
	CreatedAt           string `json:"createdAt,omitempty"`
	UpdatedAt           string `json:"updatedAt,omitempty"`
	HistoryState        string `json:"historyState,omitempty"`
	PendingInteractions int    `json:"pendingInteractions,omitempty"`
}

type RunSummary struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	StartedAt   string    `json:"startedAt,omitempty"`
	CompletedAt string    `json:"completedAt,omitempty"`
	Items       []RunItem `json:"items,omitempty"`
}

type RunItem struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Status       string `json:"status,omitempty"`
	Text         string `json:"text,omitempty"`
	Command      string `json:"command,omitempty"`
	Output       string `json:"output,omitempty"`
	ToolName     string `json:"toolName,omitempty"`
	Path         string `json:"path,omitempty"`
	ChangeType   string `json:"changeType,omitempty"`
	Diff         string `json:"diff,omitempty"`
	ExitCode     *int   `json:"exitCode,omitempty"`
	ErrorCode    string `json:"errorCode,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	Partial      bool   `json:"partial,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	TotalBytes   int    `json:"totalBytes,omitempty"`
	Digest       string `json:"digest,omitempty"`
}

type Activity struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Title     string `json:"title,omitempty"`
	Text      string `json:"text,omitempty"`
	Path      string `json:"path,omitempty"`
	ExitCode  *int   `json:"exitCode,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Digest    string `json:"digest,omitempty"`
}

type InteractionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type InteractionQuestion struct {
	ID       string              `json:"id"`
	Header   string              `json:"header"`
	Question string              `json:"question"`
	IsOther  bool                `json:"isOther,omitempty"`
	IsSecret bool                `json:"isSecret,omitempty"`
	Options  []InteractionOption `json:"options,omitempty"`
}

type InteractionAnswer struct {
	QuestionID string   `json:"questionId"`
	Answers    []string `json:"answers"`
}

type Interaction struct {
	ID              string                `json:"id"`
	Kind            string                `json:"kind"`
	Status          string                `json:"status"`
	Summary         string                `json:"summary,omitempty"`
	Risk            string                `json:"risk,omitempty"`
	RunID           string                `json:"runId,omitempty"`
	ActivityID      string                `json:"activityId,omitempty"`
	Details         map[string]any        `json:"details,omitempty"`
	OperationDigest string                `json:"operationDigest"`
	ExpiresAt       string                `json:"expiresAt"`
	Options         []InteractionOption   `json:"options,omitempty"`
	Questions       []InteractionQuestion `json:"questions,omitempty"`
	Blocking        bool                  `json:"blocking,omitempty"`
}

type TaskSnapshotModel struct {
	Task           TaskSummary   `json:"task"`
	Runs           []RunSummary  `json:"runs,omitempty"`
	Activities     []Activity    `json:"activities,omitempty"`
	Interactions   []Interaction `json:"interactions,omitempty"`
	NextCursor     string        `json:"nextCursor,omitempty"`
	LatestSequence int64         `json:"latestSequence,omitempty"`
}
