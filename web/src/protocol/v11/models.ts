export type CapabilityLevel = "full" | "read-only" | "unavailable";

export interface CapabilityDescriptor {
  readonly id: string;
  readonly level: CapabilityLevel;
  readonly reason?: string;
}

export interface AgentSummary {
  readonly id: string;
  readonly adapterType: string;
  readonly displayName: string;
  readonly version?: string;
  readonly runtimeMode: "managed" | "attached" | "history-only" | "detected-only";
  readonly status: string;
  readonly providerType?: string;
  readonly customEndpoint?: boolean;
  readonly authenticationAvailable?: boolean;
  readonly configurationFingerprint?: string;
  readonly capabilities: readonly CapabilityDescriptor[];
}

export interface TaskSummary {
  readonly id: string;
  readonly agentInstanceId: string;
  readonly workspaceId: string;
  readonly status: string;
  readonly title?: string;
  readonly preview?: string;
  readonly createdAt?: string;
  readonly updatedAt?: string;
  readonly historyState?: "complete" | "partial" | "unavailable";
  readonly pendingInteractions?: number;
}

export interface RunSummary {
  readonly id: string;
  readonly status: string;
  readonly startedAt?: string;
  readonly completedAt?: string;
  readonly items?: readonly RunItem[];
}

export interface RunItem {
  readonly id: string;
  readonly kind: "user_message" | "agent_message" | "reasoning_summary" | "plan" | "command" | "command_output" | "tool" | "file_change" | "diff" | "error" | "unknown";
  readonly status?: string;
  readonly text?: string;
  readonly command?: string;
  readonly output?: string;
  readonly toolName?: string;
  readonly path?: string;
  readonly changeType?: string;
  readonly diff?: string;
  readonly exitCode?: number;
  readonly errorCode?: string;
  readonly errorMessage?: string;
  readonly partial?: boolean;
  readonly truncated?: boolean;
  readonly totalBytes?: number;
  readonly digest?: string;
}

export interface Activity {
  readonly id: string;
  readonly kind: "command" | "tool" | "mcp" | "web_search" | "image" | "file_change" | "diff" | "review" | "collaboration" | "compaction" | "unknown";
  readonly status: string;
  readonly title?: string;
  readonly text?: string;
  readonly path?: string;
  readonly exitCode?: number;
  readonly truncated?: boolean;
  readonly digest?: string;
}

export interface Interaction {
  readonly id: string;
  readonly kind: "command_approval" | "file_approval" | "question" | "permission" | "mcp_elicitation";
  readonly status: "pending" | "processing" | "accepted" | "declined" | "answered" | "expired" | "ambiguous";
  readonly summary?: string;
  readonly risk?: "normal" | "high" | "unknown";
  readonly runId?: string;
  readonly activityId?: string;
  readonly details?: Readonly<Record<string, unknown>>;
  readonly operationDigest: string;
  readonly expiresAt: string;
  readonly options?: readonly InteractionOption[];
  readonly questions?: readonly InteractionQuestion[];
  readonly blocking?: boolean;
}

export interface InteractionOption { readonly id: string; readonly label: string; readonly description?: string }
export interface InteractionQuestion { readonly id: string; readonly header: string; readonly question: string; readonly isOther?: boolean; readonly isSecret?: boolean; readonly options?: readonly InteractionOption[] }
export interface InteractionAnswer { readonly questionId: string; readonly answers: readonly string[] }

export interface TaskSnapshot {
  readonly task: TaskSummary;
  readonly runs?: readonly RunSummary[];
  readonly activities?: readonly Activity[];
  readonly interactions?: readonly Interaction[];
  readonly nextCursor?: string;
  readonly latestSequence?: number;
}
