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
  readonly operationDigest: string;
  readonly expiresAt: string;
  readonly options?: readonly { readonly id: string; readonly label: string }[];
}

export interface TaskSnapshot {
  readonly task: TaskSummary;
  readonly runs?: readonly RunSummary[];
  readonly activities?: readonly Activity[];
  readonly interactions?: readonly Interaction[];
  readonly nextCursor?: string;
  readonly latestSequence?: number;
}
