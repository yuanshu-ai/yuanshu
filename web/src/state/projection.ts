import type { ControlAction, RelayMessage } from "../relay/control-client";

export interface NodeProjection {
  ownerId: string;
  nodeId: string;
  name?: string;
  version?: string;
  status?: string;
  runtimeStatus?: string;
  online: boolean;
  discovered?: boolean;
  workspaceIds: string[];
  agentInstanceIds?: string[];
  lastEventSequence: number;
  lastSeen?: string;
}

export interface AgentProjection {
  key: string;
  ownerId: string;
  nodeId: string;
  agentInstanceId: string;
  adapterType: string;
  displayName: string;
  version?: string;
  runtimeMode: "managed" | "attached" | "history-only" | "detected-only";
  status: string;
  providerType?: string;
  customEndpoint?: boolean;
  authenticationAvailable?: boolean;
  configurationFingerprint?: string;
  capabilities: Array<{ id: string; level: "full" | "read-only" | "unavailable"; reason?: string }>;
  workspaceIds: string[];
  updatedAt: string;
}

export interface WorkspaceProjection {
  key: string;
  ownerId: string;
  nodeId: string;
  workspaceId: string;
  name?: string;
  adapter?: string;
  permissionProfile?: string;
  allowNetwork?: boolean;
  agentInstanceIds?: string[];
  defaultAgentInstanceId?: string;
}

export interface ThreadProjection {
  key: string;
  ownerId: string;
  nodeId: string;
  workspaceId: string;
  threadId: string;
  agentInstanceId?: string;
  status?: string;
  title?: string;
  preview?: string;
  historyState?: "complete" | "partial" | "unavailable";
  createdAt?: string;
  updatedAt?: string;
  pendingApprovals?: number;
  turnIds: string[];
  firstObservedSequence?: number;
  latestSequence: number;
  recovery: "none" | "pending" | "history_gap";
  tokenUsage?: TokenUsageProjection;
}

export interface TurnProjection {
  key: string;
  ownerId: string;
  nodeId: string;
  workspaceId: string;
  threadId: string;
  turnId: string;
  status?: string;
  historyState?: "complete" | "partial" | "unavailable";
  items: ThreadItemProjection[];
  updatedAt?: string;
}

export type ThreadItemKind = "user_message" | "agent_message" | "reasoning_summary" | "plan" | "command" | "command_output" | "tool" | "file_change" | "diff" | "error" | "unknown";

export interface TokenUsageProjection {
  inputTokens?: number;
  cachedInputTokens?: number;
  outputTokens?: number;
  reasoningOutputTokens?: number;
  totalTokens?: number;
  modelContextWindow?: number;
}

export interface PlanProjection {
  key: string;
  nodeId: string;
  workspaceId: string;
  threadId: string;
  turnId: string;
  explanation?: string;
  steps: Array<{ text: string; status: string }>;
  updatedAt: string;
}

export interface InteractionQuestionProjection {
  id: string;
  header: string;
  question: string;
  isOther?: boolean;
  options: Array<{ id: string; label: string; description?: string }>;
}

export interface InteractionProjection {
  key: string;
  nodeId: string;
  workspaceId: string;
  threadId: string;
  turnId: string;
  itemId?: string;
  interactionId: string;
  kind: string;
  status: string;
  summary?: string;
  operationDigest?: string;
  expiresAt?: string;
  blocking?: boolean;
  questions: InteractionQuestionProjection[];
}

export interface ThreadItemProjection {
  id: string;
  kind: ThreadItemKind;
  status?: string;
  text?: string;
  command?: string;
  output?: string;
  toolName?: string;
  path?: string;
  changeType?: string;
  diff?: string;
  exitCode?: number;
  errorCode?: string;
  errorMessage?: string;
  partial?: boolean;
  truncated?: boolean;
  totalBytes?: number;
  digest?: string;
  sequence?: number;
}

export interface FileChangeProjection {
  key: string;
  nodeId: string;
  workspaceId: string;
  threadId: string;
  turnId: string;
  path: string;
  changeType?: string;
  diff?: string;
  truncated?: boolean;
  totalBytes?: number;
  digest?: string;
  revision: number;
  updatedAt: string;
}

export interface EventProjection {
  key: string;
  nodeId: string;
  workspaceId?: string;
  threadId?: string;
  turnId?: string;
  sequence: number;
  event: RelayMessage;
}

export interface ApprovalProjection {
  key: string;
  nodeId: string;
  workspaceId: string;
  threadId: string;
  turnId: string;
  itemId?: string;
  approvalId: string;
  operationDigest?: string;
  kind?: string;
  summary?: string;
  expiresAt?: string;
  risk?: string;
  decision?: string;
  status: "pending" | "resolved";
}

export interface ControlActionProjection extends ControlAction {
  updatedAt: string;
}

export interface NotificationProjection {
  id: string;
  nodeId: string;
  workspaceId?: string;
  threadId?: string;
  turnId?: string;
  type: string;
  summary: string;
  sourceSequence: number;
  createdAt: string;
  read: boolean;
}

export interface ProjectionState {
  nodes: Record<string, NodeProjection>;
  agents: Record<string, AgentProjection>;
  workspaces: Record<string, WorkspaceProjection>;
  threads: Record<string, ThreadProjection>;
  turns: Record<string, TurnProjection>;
  events: Record<string, EventProjection[]>;
  approvals: Record<string, ApprovalProjection>;
  interactions: Record<string, InteractionProjection>;
  plans: Record<string, PlanProjection>;
  files: Record<string, FileChangeProjection>;
  notifications: Record<string, NotificationProjection>;
  actions: Record<string, ControlActionProjection>;
}

export interface ProjectionOptions {
  maxEventsPerBucket?: number;
  now?: () => string;
}

const DEFAULT_MAX_EVENTS = 512;

/**
 * Pure, in-memory projection for the personal Web control client.
 *
 * The projection deliberately does not persist task content or send anything
 * to the Server. The Relay client owns cursor/replay correctness; this layer
 * only turns accepted, ordered envelopes into UI-friendly records.
 */
export class DataProjection {
  private readonly maxEventsPerBucket: number;
  private readonly now: () => string;
  private readonly seenEvents = new Set<string>();
  private readonly stateValue: ProjectionState = {
    nodes: {},
    agents: {},
    workspaces: {},
    threads: {},
    turns: {},
    events: {},
    approvals: {},
    interactions: {},
    plans: {},
    files: {},
    notifications: {},
    actions: {},
  };

  constructor(options: ProjectionOptions = {}) {
    this.maxEventsPerBucket = options.maxEventsPerBucket ?? DEFAULT_MAX_EVENTS;
    this.now = options.now ?? (() => new Date().toISOString());
  }

  get state(): ProjectionState {
    return this.stateValue;
  }

  registerNode(node: Partial<NodeProjection> & Pick<NodeProjection, "ownerId" | "nodeId">): NodeProjection {
    const current = this.stateValue.nodes[node.nodeId];
    const next: NodeProjection = {
      ...current,
      ...node,
      online: node.online ?? current?.online ?? true,
      workspaceIds: [...(node.workspaceIds ?? current?.workspaceIds ?? [])],
      agentInstanceIds: [...(node.agentInstanceIds ?? current?.agentInstanceIds ?? [])],
      lastEventSequence: node.lastEventSequence ?? current?.lastEventSequence ?? 0,
    };
    this.stateValue.nodes[node.nodeId] = next;
    return next;
  }

  applyControlAction(action: ControlAction): ControlActionProjection {
    const next: ControlActionProjection = { ...action, updatedAt: this.now() };
    this.stateValue.actions[action.messageId] = next;
    return next;
  }

  applyServerControlResult(event: RelayMessage): void {
    this.applyControlResultProjection(event);
    if (!Array.isArray(event.payload.notifications)) return;
    for (const raw of event.payload.notifications) {
      if (!isRecord(raw) || typeof raw.id !== "string" || typeof raw.type !== "string" || typeof raw.summary !== "string") continue;
      this.stateValue.notifications[raw.id] = {
        id: raw.id, nodeId: stringValue(raw.nodeId) ?? event.nodeId, workspaceId: stringValue(raw.workspaceId), threadId: stringValue(raw.threadId), turnId: stringValue(raw.turnId),
        type: raw.type, summary: raw.summary, sourceSequence: numberValue(raw.sourceSequence) ?? 0, createdAt: stringValue(raw.createdAt) ?? event.sentAt, read: raw.read === true,
      };
    }
  }

  markNotificationRead(notificationId: string): void {
    const current = this.stateValue.notifications[notificationId];
    if (current) current.read = true;
  }

  apply(event: RelayMessage): void {
    if (event.ownerId === "" || event.nodeId === "") return;
    const eventKey = `${event.ownerId}\u001f${event.nodeId}\u001f${event.streamId}\u001f${event.sequence}`;
    if (this.seenEvents.has(eventKey)) return;
    this.seenEvents.add(eventKey);

    const node = this.ensureNode(event);
    node.lastEventSequence = Math.max(node.lastEventSequence, event.sequence);
    node.lastSeen = event.sentAt;
    node.online = true;

    if (event.type === "agent.snapshot") {
      this.applyAgentSnapshot(node, event);
      this.appendEvent(event);
      return;
    }
	if (event.type === "agent.status" && event.agentInstanceId) {
	  const agent = this.stateValue.agents[agentKey(event.nodeId, event.agentInstanceId)];
	  if (agent) {
		agent.status = stringValue(event.payload.state) ?? stringValue(event.payload.status) ?? agent.status;
		agent.updatedAt = event.sentAt;
	  }
	  this.appendEvent(event);
	  return;
	}
    if (event.protocolVersion === "1.1") {
      if (event.type === "task.updated") {
        this.applyTaskUpdated(event);
        this.appendEvent(event);
        return;
      }
      if (event.type === "reasoning.summary.delta" || event.type === "reasoning.summary.completed") {
        this.applyThreadItem(event, { id: event.activityId ?? `${event.runId ?? "run"}:reasoning`, kind: "reasoning_summary", text: stringValue(event.payload.text) ?? "", status: event.type.endsWith("completed") ? "completed" : "streaming", partial: event.payload.partial === true, sequence: event.sequence });
        this.appendEvent(event);
        return;
      }
      if (event.type === "plan.updated") {
        this.applyPlan(event);
        this.appendEvent(event);
        return;
      }
      if (event.type === "interaction.requested" || event.type === "interaction.resolved") {
        this.applyInteraction(event);
        const approvalEvent = normalizeAgentEvent(event);
        if (approvalEvent.type === "approval.requested") this.applyApprovalRequested(approvalEvent);
        if (approvalEvent.type === "approval.resolved") this.applyApprovalResolved(approvalEvent);
        this.updatePendingInteractionCount(event);
        this.appendEvent(event);
        return;
      }
    }
    const normalized = normalizeAgentEvent(event);
    switch (normalized.type) {
      case "device.status":
        this.applyDeviceStatus(node, normalized);
        break;
      case "runtime.status":
        this.applyRuntimeStatus(node, normalized);
        break;
      case "thread.snapshot":
        this.applyThreadSnapshot(normalized);
        break;
      case "thread.started":
        this.applyThreadLifecycle(normalized, "running");
        break;
      case "turn.started":
        this.applyTurnLifecycle(normalized, "running");
        break;
      case "turn.completed":
        this.applyTurnLifecycle(normalized, "completed");
        break;
      case "turn.failed":
        this.applyTurnLifecycle(normalized, "failed");
        break;
      case "turn.interrupted":
        this.applyTurnLifecycle(normalized, "interrupted");
        break;
      case "agent.message.delta":
        this.applyThreadItem(normalized, { id: normalized.itemId ?? `${normalized.turnId ?? "turn"}:agent`, kind: normalized.payload.role === "user" ? "user_message" : "agent_message", text: stringValue(normalized.payload.text) ?? "", status: "streaming", sequence: normalized.sequence });
        break;
      case "agent.message.completed":
        this.applyThreadItem(normalized, { id: normalized.itemId ?? `${normalized.turnId ?? "turn"}:agent`, kind: normalized.payload.role === "user" ? "user_message" : "agent_message", text: stringValue(normalized.payload.text) ?? "", status: "completed", sequence: normalized.sequence });
        break;
      case "command.started":
      case "command.completed":
        this.applyThreadItem(normalized, { id: stringValue(normalized.payload.commandId) ?? normalized.itemId ?? `${normalized.sequence}`, kind: "command", command: stringValue(normalized.payload.displayText) ?? stringValue(normalized.payload.command), status: normalized.type === "command.completed" ? "completed" : "running", exitCode: numberValue(normalized.payload.exitCode), sequence: normalized.sequence });
        break;
      case "command.output.delta":
        this.applyThreadItem(normalized, { id: stringValue(normalized.payload.commandId) ?? normalized.itemId ?? `${normalized.sequence}`, kind: "command_output", output: stringValue(normalized.payload.text) ?? "", status: "streaming", sequence: normalized.sequence });
        break;
      case "tool.started":
      case "tool.completed":
        this.applyThreadItem(normalized, { id: normalized.itemId ?? `${normalized.sequence}`, kind: "tool", toolName: stringValue(normalized.payload.toolName), status: normalized.type === "tool.completed" ? "completed" : "running", sequence: normalized.sequence });
        break;
      case "file.changed":
        this.applyThreadItem(normalized, { id: normalized.itemId ?? `${normalized.sequence}`, kind: "file_change", path: stringValue(normalized.payload.path), changeType: stringValue(normalized.payload.changeType), status: "completed", sequence: normalized.sequence });
        break;
      case "diff.updated":
        this.applyThreadItem(normalized, { id: normalized.itemId ?? stringValue(normalized.payload.path) ?? `${normalized.sequence}`, kind: "diff", path: stringValue(normalized.payload.path), diff: stringValue(normalized.payload.diff), status: "updated", sequence: normalized.sequence });
        break;
      case "error":
        this.applyThreadItem(normalized, { id: normalized.itemId ?? `${normalized.sequence}`, kind: "error", errorCode: stringValue(normalized.payload.code), errorMessage: stringValue(normalized.payload.message), status: "failed", sequence: normalized.sequence });
        break;
      case "approval.requested":
        this.applyApprovalRequested(normalized);
        break;
      case "approval.resolved":
        this.applyApprovalResolved(normalized);
        break;
      case "history.gap":
        this.applyHistoryGap(normalized);
        break;
      case "control.result":
        this.applyControlResult(normalized);
        break;
    }

    if (normalized.type !== "device.status" && normalized.type !== "runtime.status" && normalized.type !== "control.result") {
      this.appendEvent(normalized);
    }
  }

  /**
   * Applies a targeted Diff read without replacing the Thread history that is
   * already on screen. The request intent is kept by WorkbenchSession and is
   * deliberately not represented on the protocol envelope.
   */
  applyDiffSnapshot(event: RelayMessage, path: string): void {
	event = normalizeAgentEvent(event);
	if (event.type !== "thread.snapshot" || !event.workspaceId || !event.threadId || !path) return;
    const eventKey = `${event.ownerId}\u001f${event.nodeId}\u001f${event.streamId}\u001f${event.sequence}`;
    if (this.seenEvents.has(eventKey)) return;
    this.seenEvents.add(eventKey);

    const node = this.ensureNode(event);
    node.lastEventSequence = Math.max(node.lastEventSequence, event.sequence);
    node.lastSeen = event.sentAt;
    node.online = true;

    if (!Array.isArray(event.payload.turns)) return;
    for (const rawTurn of event.payload.turns) {
      if (!isRecord(rawTurn) || typeof rawTurn.id !== "string" || !Array.isArray(rawTurn.items)) continue;
      for (const rawItem of rawTurn.items) {
        const item = this.itemFromPayload(rawItem, event.sequence);
        if (!item || item.path !== path || (item.kind !== "file_change" && item.kind !== "diff")) continue;
        this.applyFileChange(event, item, rawTurn.id);
      }
    }
  }

  private ensureNode(event: RelayMessage): NodeProjection {
    return this.registerNode({ ownerId: event.ownerId, nodeId: event.nodeId });
  }

  private applyAgentSnapshot(node: NodeProjection, event: RelayMessage): void {
    if (!Array.isArray(event.payload.agents)) return;
    for (const raw of event.payload.agents) {
      if (!isRecord(raw) || typeof raw.id !== "string" || typeof raw.adapterType !== "string" || typeof raw.displayName !== "string") continue;
      const runtimeMode = isRuntimeMode(raw.runtimeMode) ? raw.runtimeMode : "detected-only";
      const capabilities = Array.isArray(raw.capabilities)
        ? raw.capabilities.flatMap((entry) => isRecord(entry) && typeof entry.id === "string" && isCapabilityLevel(entry.level)
          ? [{ id: entry.id, level: entry.level, ...(typeof entry.reason === "string" ? { reason: entry.reason } : {}) }]
          : [])
        : [];
      const key = agentKey(event.nodeId, raw.id);
      const current = this.stateValue.agents[key];
      this.stateValue.agents[key] = {
        ...current,
        key,
        ownerId: event.ownerId,
        nodeId: event.nodeId,
        agentInstanceId: raw.id,
        adapterType: raw.adapterType,
        displayName: raw.displayName,
        version: stringValue(raw.version),
        runtimeMode,
        status: stringValue(raw.status) ?? "unknown",
        providerType: stringValue(raw.providerType),
        customEndpoint: booleanValue(raw.customEndpoint),
        authenticationAvailable: booleanValue(raw.authenticationAvailable),
        configurationFingerprint: stringValue(raw.configurationFingerprint),
        capabilities,
        workspaceIds: [...(current?.workspaceIds ?? [])],
        updatedAt: event.sentAt,
      };
      node.agentInstanceIds ??= [];
      if (!node.agentInstanceIds.includes(raw.id)) node.agentInstanceIds.push(raw.id);
    }
  }

  private applyDeviceStatus(node: NodeProjection, event: RelayMessage): void {
    const payload = event.payload;
    if (typeof payload.status === "string") {
      node.status = payload.status;
      node.online = !["offline", "unavailable", "not_available"].includes(payload.status);
    }
    if (typeof payload.runtime === "string") node.runtimeStatus = payload.runtime;
    if (typeof payload.name === "string") node.name = payload.name;
    if (typeof payload.version === "string") node.version = payload.version;
    if (!Array.isArray(payload.workspaces)) return;
    for (const raw of payload.workspaces) {
      if (!isRecord(raw) || typeof raw.id !== "string") continue;
      const workspace = this.upsertWorkspace(node, raw.id, {
        name: stringValue(raw.name),
        adapter: stringValue(raw.adapter),
        permissionProfile: stringValue(raw.permissionProfile),
        allowNetwork: booleanValue(raw.allowNetwork),
        agentInstanceIds: [],
      });
	  if (Array.isArray(raw.agents)) {
		workspace.agentInstanceIds = [];
		for (const link of raw.agents) {
		  if (!isRecord(link) || typeof link.agentInstanceId !== "string") continue;
		  workspace.agentInstanceIds.push(link.agentInstanceId);
		  if (link.default === true) workspace.defaultAgentInstanceId = link.agentInstanceId;
		  const agent = this.stateValue.agents[agentKey(node.nodeId, link.agentInstanceId)];
		  if (agent && !agent.workspaceIds.includes(raw.id)) agent.workspaceIds.push(raw.id);
		}
	  }
      if (!node.workspaceIds.includes(workspace.workspaceId)) node.workspaceIds.push(workspace.workspaceId);
    }
  }

  private applyRuntimeStatus(node: NodeProjection, event: RelayMessage): void {
    const state = typeof event.payload.state === "string" ? event.payload.state : typeof event.payload.status === "string" ? event.payload.status : undefined;
    if (state) node.runtimeStatus = state;
    node.online = true;
  }

  private applyThreadSnapshot(event: RelayMessage): void {
    const payload = event.payload;
    const workspaceId = event.workspaceId;
    if (!workspaceId) return;
    if (Array.isArray(payload.threads)) {
      for (const raw of payload.threads) {
        if (!isRecord(raw) || typeof raw.id !== "string") continue;
        const thread = this.upsertThread(event.nodeId, event.ownerId, workspaceId, raw.id, event.sequence);
		thread.agentInstanceId = stringValue(raw.agentInstanceId) ?? event.agentInstanceId ?? thread.agentInstanceId;
        if (typeof raw.status === "string") thread.status = raw.status;
        this.applyThreadMetadata(thread, raw);
        if (raw.historyState === "complete" || raw.historyState === "partial" || raw.historyState === "unavailable") thread.historyState = raw.historyState;
        if (typeof raw.pendingApprovals === "number") thread.pendingApprovals = raw.pendingApprovals;
        thread.latestSequence = Math.max(thread.latestSequence, event.sequence);
        thread.recovery = "none";
        thread.updatedAt = stringValue(raw.updatedAt) ?? event.sentAt;
      }
      return;
    }
    if (!event.threadId) return;
    const thread = this.upsertThread(event.nodeId, event.ownerId, workspaceId, event.threadId, event.sequence);
	thread.agentInstanceId = event.agentInstanceId ?? stringValue(payload.agentInstanceId) ?? thread.agentInstanceId;
    if (typeof payload.status === "string") thread.status = payload.status;
    this.applyThreadMetadata(thread, payload);
    if (isRecord(payload.thread)) {
      if (typeof payload.thread.status === "string") thread.status = payload.thread.status;
      this.applyThreadMetadata(thread, payload.thread);
    }
    if (payload.historyState === "complete" || payload.historyState === "partial" || payload.historyState === "unavailable") thread.historyState = payload.historyState;
    if (Array.isArray(payload.pendingApprovals)) thread.pendingApprovals = payload.pendingApprovals.length;
	if (Array.isArray(payload.interactions)) {
	  thread.pendingApprovals = payload.interactions.filter((value) => isRecord(value) && value.status === "pending").length;
	  for (const interaction of payload.interactions) if (isRecord(interaction)) this.applyInteraction({ ...event, interactionId: stringValue(interaction.id), payload: interaction });
	}
    if (Array.isArray(payload.pendingApprovals)) {
      for (const raw of payload.pendingApprovals) {
        if (!isRecord(raw)) continue;
        const approvalId = stringValue(raw.approvalId);
        if (!approvalId) continue;
        const key = this.approvalKey(event.nodeId, workspaceId, event.threadId, stringValue(raw.turnId) ?? "", approvalId);
        this.stateValue.approvals[key] = {
          key,
          nodeId: event.nodeId,
          workspaceId,
          threadId: event.threadId,
          turnId: stringValue(raw.turnId) ?? "",
          itemId: stringValue(raw.itemId),
          approvalId,
          operationDigest: stringValue(raw.operationDigest),
          kind: stringValue(raw.kind),
          summary: stringValue(raw.summary),
          expiresAt: stringValue(raw.expiresAt),
          risk: stringValue(raw.risk),
          status: "pending",
        };
      }
    }
    if (typeof payload.latestSequence === "number") thread.latestSequence = Math.max(thread.latestSequence, payload.latestSequence);
    thread.latestSequence = Math.max(thread.latestSequence, event.sequence);
    thread.recovery = "none";
    thread.updatedAt = event.sentAt;
    if (!Array.isArray(payload.turns)) return;
    for (const raw of payload.turns) {
      if (!isRecord(raw) || typeof raw.id !== "string") continue;
      const turn = this.upsertTurn(event.nodeId, event.ownerId, workspaceId, event.threadId, raw.id);
      if (typeof raw.status === "string") turn.status = raw.status;
      if (raw.historyState === "complete" || raw.historyState === "partial" || raw.historyState === "unavailable") turn.historyState = raw.historyState;
      if (Array.isArray(raw.items)) {
        turn.items = raw.items.map((item) => this.itemFromPayload(item, event.sequence)).filter((item): item is ThreadItemProjection => item !== undefined);
        for (const item of turn.items) if (item.kind === "file_change" || item.kind === "diff") this.applyFileChange(event, item, turn.turnId);
      }
      turn.updatedAt = event.sentAt;
      if (!thread.turnIds.includes(turn.turnId)) thread.turnIds.push(turn.turnId);
    }
  }

  private applyTaskUpdated(event: RelayMessage): void {
    if (!event.workspaceId || !event.taskId) return;
    const thread = this.upsertThread(event.nodeId, event.ownerId, event.workspaceId, event.taskId, event.sequence);
    if (typeof event.payload.status === "string") thread.status = event.payload.status;
    if (isRecord(event.payload.tokenUsage)) {
      thread.tokenUsage = {
        inputTokens: numberValue(event.payload.tokenUsage.inputTokens), cachedInputTokens: numberValue(event.payload.tokenUsage.cachedInputTokens),
        outputTokens: numberValue(event.payload.tokenUsage.outputTokens), reasoningOutputTokens: numberValue(event.payload.tokenUsage.reasoningOutputTokens),
        totalTokens: numberValue(event.payload.tokenUsage.totalTokens), modelContextWindow: numberValue(event.payload.tokenUsage.modelContextWindow),
      };
    }
    thread.latestSequence = Math.max(thread.latestSequence, event.sequence);
    thread.updatedAt = event.sentAt;
  }

  private applyPlan(event: RelayMessage): void {
    if (!event.workspaceId || !event.taskId || !event.runId) return;
    const steps = Array.isArray(event.payload.steps) ? event.payload.steps.flatMap((raw) => isRecord(raw) && typeof raw.text === "string" ? [{ text: raw.text, status: stringValue(raw.status) ?? "pending" }] : []) : [];
    const key = [event.nodeId, event.workspaceId, event.taskId, event.runId].join("\u001f");
    this.stateValue.plans[key] = { key, nodeId: event.nodeId, workspaceId: event.workspaceId, threadId: event.taskId, turnId: event.runId, explanation: stringValue(event.payload.explanation), steps, updatedAt: event.sentAt };
    if (typeof event.payload.text === "string") {
      this.applyThreadItem(event, { id: event.activityId ?? `${event.runId}:plan`, kind: "plan", text: event.payload.text, status: "updated", sequence: event.sequence });
    }
  }

  private applyInteraction(event: RelayMessage): void {
    const workspaceId = event.workspaceId;
    const threadId = event.taskId ?? event.threadId;
    const turnId = event.runId ?? event.turnId ?? stringValue(event.payload.runId);
    const interactionId = event.interactionId ?? stringValue(event.payload.id);
    if (!workspaceId || !threadId || !turnId || !interactionId) return;
    const key = [event.nodeId, workspaceId, threadId, turnId, interactionId].join("\u001f");
    const current = this.stateValue.interactions[key];
    const questions = Array.isArray(event.payload.questions) ? event.payload.questions.flatMap((raw) => {
      if (!isRecord(raw) || typeof raw.id !== "string" || typeof raw.header !== "string" || typeof raw.question !== "string") return [];
      const options = Array.isArray(raw.options) ? raw.options.flatMap((option) => isRecord(option) && typeof option.id === "string" && typeof option.label === "string" ? [{ id: option.id, label: option.label, description: stringValue(option.description) }] : []) : [];
      return [{ id: raw.id, header: raw.header, question: raw.question, isOther: raw.isOther === true, options }];
    }) : current?.questions ?? [];
    this.stateValue.interactions[key] = {
      ...current, key, nodeId: event.nodeId, workspaceId, threadId, turnId, itemId: event.activityId ?? event.itemId ?? stringValue(event.payload.activityId) ?? current?.itemId,
      interactionId, kind: stringValue(event.payload.kind) ?? current?.kind ?? "unknown", status: stringValue(event.payload.status) ?? current?.status ?? "pending",
      summary: stringValue(event.payload.summary) ?? current?.summary, operationDigest: stringValue(event.payload.operationDigest) ?? current?.operationDigest,
      expiresAt: stringValue(event.payload.expiresAt) ?? current?.expiresAt, blocking: booleanValue(event.payload.blocking) ?? current?.blocking, questions,
    };
    this.updatePendingInteractionCount(event);
  }

  private updatePendingInteractionCount(event: RelayMessage): void {
    const workspaceId = event.workspaceId;
    const threadId = event.taskId ?? event.threadId;
    if (!workspaceId || !threadId) return;
    const pendingQuestions = Object.values(this.stateValue.interactions).filter((item) =>
      item.nodeId === event.nodeId && item.workspaceId === workspaceId && item.threadId === threadId && item.status === "pending" && (item.kind === "question" || item.kind === "mcp_elicitation"),
    ).length;
    const pendingApprovals = Object.values(this.stateValue.approvals).filter((item) =>
      item.nodeId === event.nodeId && item.workspaceId === workspaceId && item.threadId === threadId && item.status === "pending",
    ).length;
    const thread = this.upsertThread(event.nodeId, event.ownerId, workspaceId, threadId, event.sequence);
    thread.pendingApprovals = pendingQuestions + pendingApprovals;
  }

  private applyThreadLifecycle(event: RelayMessage, fallback: string): void {
    if (!event.workspaceId || !event.threadId) return;
    const thread = this.upsertThread(event.nodeId, event.ownerId, event.workspaceId, event.threadId, event.sequence);
	thread.agentInstanceId = event.agentInstanceId ?? thread.agentInstanceId;
    thread.status = stringValue(event.payload.status) ?? fallback;
    this.applyThreadMetadata(thread, event.payload);
    thread.recovery = "none";
    thread.latestSequence = Math.max(thread.latestSequence, event.sequence);
    thread.updatedAt = event.sentAt;
  }

  private applyTurnLifecycle(event: RelayMessage, fallback: string): void {
    if (!event.workspaceId || !event.threadId || !event.turnId) return;
    const turn = this.upsertTurn(event.nodeId, event.ownerId, event.workspaceId, event.threadId, event.turnId);
    turn.status = stringValue(event.payload.status) ?? fallback;
    if (event.type === "turn.started") turn.historyState = "partial";
    turn.updatedAt = event.sentAt;
    const thread = this.upsertThread(event.nodeId, event.ownerId, event.workspaceId, event.threadId, event.sequence);
    if (!thread.turnIds.includes(turn.turnId)) thread.turnIds.push(turn.turnId);
    // A Thread has one active Codex Turn at a time. Keep the summary and
    // detail header aligned with the authoritative Turn lifecycle event.
    thread.status = turn.status;
    thread.latestSequence = Math.max(thread.latestSequence, event.sequence);
    thread.updatedAt = event.sentAt;
  }

  private applyHistoryGap(event: RelayMessage): void {
    if (!event.workspaceId || !event.threadId) return;
    const thread = this.upsertThread(event.nodeId, event.ownerId, event.workspaceId, event.threadId, event.sequence);
    thread.recovery = "history_gap";
    thread.historyState = "partial";
    thread.latestSequence = Math.max(thread.latestSequence, event.sequence);
    thread.updatedAt = event.sentAt;
  }

  private applyThreadMetadata(thread: ThreadProjection, payload: Record<string, unknown>): void {
    if (typeof payload.title === "string") thread.title = payload.title;
    if (typeof payload.preview === "string") thread.preview = payload.preview;
    if (typeof payload.createdAt === "string") thread.createdAt = payload.createdAt;
    if (typeof payload.updatedAt === "string") thread.updatedAt = payload.updatedAt;
  }

  private applyThreadItem(event: RelayMessage, item: ThreadItemProjection): void {
	const threadId = event.threadId ?? event.taskId;
	const turnId = event.turnId ?? event.runId;
	if (!event.workspaceId || !threadId || !turnId) return;
	const turn = this.upsertTurn(event.nodeId, event.ownerId, event.workspaceId, threadId, turnId);
    const index = turn.items.findIndex((candidate) => candidate.id === item.id);
    if (index < 0) turn.items.push(item);
    else turn.items[index] = mergeItem(turn.items[index], item);
	const thread = this.upsertThread(event.nodeId, event.ownerId, event.workspaceId, threadId, event.sequence);
    if (!thread.turnIds.includes(turn.turnId)) thread.turnIds.push(turn.turnId);
    turn.updatedAt = event.sentAt;
    if ((item.kind === "file_change" || item.kind === "diff") && item.path) this.applyFileChange(event, item);
  }

  private itemFromPayload(raw: unknown, sequence: number): ThreadItemProjection | undefined {
    if (!isRecord(raw) || typeof raw.id !== "string" || typeof raw.kind !== "string") return undefined;
    const kinds = new Set<ThreadItemKind>(["user_message", "agent_message", "reasoning_summary", "plan", "command", "command_output", "tool", "file_change", "diff", "error", "unknown"]);
    const kind = kinds.has(raw.kind as ThreadItemKind) ? raw.kind as ThreadItemKind : "unknown";
    return {
      id: raw.id,
      kind,
      status: stringValue(raw.status),
      text: stringValue(raw.text),
      command: stringValue(raw.command),
      output: stringValue(raw.output),
      toolName: stringValue(raw.toolName),
      path: stringValue(raw.path),
      changeType: stringValue(raw.changeType),
      diff: stringValue(raw.diff),
      exitCode: numberValue(raw.exitCode),
      errorCode: stringValue(raw.errorCode),
      errorMessage: stringValue(raw.errorMessage),
      partial: raw.partial === true,
      truncated: raw.truncated === true,
      totalBytes: numberValue(raw.totalBytes),
      digest: stringValue(raw.digest),
      sequence,
    };
  }

  private applyFileChange(event: RelayMessage, item: ThreadItemProjection, turnId = event.turnId ?? ""): void {
    if (!event.workspaceId || !event.threadId || !turnId || !item.path) return;
    const key = fileChangeKey(event.nodeId, event.workspaceId, event.threadId, turnId, item.path);
    const current = this.stateValue.files[key];
    this.stateValue.files[key] = {
      ...current,
      key, nodeId: event.nodeId, workspaceId: event.workspaceId, threadId: event.threadId, turnId,
      path: item.path,
      changeType: item.changeType ?? current?.changeType,
      diff: item.diff || current?.diff,
      truncated: item.truncated ?? current?.truncated,
      totalBytes: item.totalBytes ?? current?.totalBytes,
      digest: item.digest ?? current?.digest,
      revision: (current?.revision ?? 0) + 1,
      updatedAt: event.sentAt,
    };
  }

  private applyApprovalRequested(event: RelayMessage): void {
    if (!event.workspaceId || !event.threadId || !event.turnId) return;
    const approvalId = stringValue(event.payload.approvalId);
    if (!approvalId) return;
    const key = this.approvalKey(event.nodeId, event.workspaceId, event.threadId, event.turnId, approvalId);
    this.stateValue.approvals[key] = {
      key,
      nodeId: event.nodeId,
      workspaceId: event.workspaceId,
      threadId: event.threadId,
      turnId: event.turnId,
      itemId: event.itemId,
      approvalId,
      operationDigest: stringValue(event.payload.operationDigest),
      kind: stringValue(event.payload.kind),
      summary: stringValue(event.payload.summary),
      expiresAt: stringValue(event.payload.expiresAt),
      risk: stringValue(event.payload.risk),
      status: "pending",
    };
  }

  private applyApprovalResolved(event: RelayMessage): void {
    if (!event.workspaceId || !event.threadId || !event.turnId) return;
    const approvalId = stringValue(event.payload.approvalId);
    if (!approvalId) return;
    const key = this.approvalKey(event.nodeId, event.workspaceId, event.threadId, event.turnId, approvalId);
    const current = this.stateValue.approvals[key] ?? {
      key,
      nodeId: event.nodeId,
      workspaceId: event.workspaceId,
      threadId: event.threadId,
      turnId: event.turnId,
      itemId: event.itemId,
      approvalId,
      status: "resolved" as const,
    };
    current.status = "resolved";
    current.decision = stringValue(event.payload.decision);
    this.stateValue.approvals[key] = current;
  }

  private applyControlResult(event: RelayMessage): void {
    this.applyControlResultProjection(event);
  }

  private applyControlResultProjection(event: RelayMessage): void {
    const current = this.stateValue.actions[event.correlationId];
    const status = stringValue(event.payload.status);
    const mapped = status === "confirmed" || status === "rejected" || status === "ambiguous" ? status : current?.state ?? "sent";
    this.stateValue.actions[event.correlationId] = {
      ...(current ?? { messageId: event.correlationId, nodeId: event.nodeId, type: "unknown" }),
      state: mapped,
      errorCode: stringValue(event.payload.errorCode),
      updatedAt: this.now(),
    };
  }

  private appendEvent(event: RelayMessage): void {
    const key = eventBucketKey(event);
    const bucket = this.stateValue.events[key] ?? [];
    if (bucket.some((item) => item.sequence === event.sequence && item.nodeId === event.nodeId)) return;
    bucket.push({ key, nodeId: event.nodeId, workspaceId: event.workspaceId, threadId: event.threadId, turnId: event.turnId, sequence: event.sequence, event });
    bucket.sort((left, right) => left.sequence - right.sequence);
    if (bucket.length > this.maxEventsPerBucket) bucket.splice(0, bucket.length - this.maxEventsPerBucket);
    this.stateValue.events[key] = bucket;
  }

  private upsertWorkspace(node: NodeProjection, workspaceId: string, values: Partial<WorkspaceProjection>): WorkspaceProjection {
    const key = workspaceKey(node.nodeId, workspaceId);
    const current = this.stateValue.workspaces[key];
    const { key: _key, ownerId: _ownerId, nodeId: _nodeId, workspaceId: _workspaceId, ...workspaceValues } = values;
    const next: WorkspaceProjection = { ...current, key, ownerId: node.ownerId, nodeId: node.nodeId, workspaceId, agentInstanceIds: [...(current?.agentInstanceIds ?? [])], ...workspaceValues };
    this.stateValue.workspaces[key] = next;
    return next;
  }

  private upsertThread(nodeId: string, ownerId: string, workspaceId: string, threadId: string, observedSequence = 0): ThreadProjection {
    const key = threadKey(nodeId, workspaceId, threadId);
    const current = this.stateValue.threads[key];
    const next: ThreadProjection = { ...current, key, ownerId, nodeId, workspaceId, threadId, turnIds: [...(current?.turnIds ?? [])], firstObservedSequence: current?.firstObservedSequence ?? observedSequence, latestSequence: current?.latestSequence ?? 0, recovery: current?.recovery ?? "pending" };
    this.stateValue.threads[key] = next;
    return next;
  }

  private upsertTurn(nodeId: string, ownerId: string, workspaceId: string, threadId: string, turnId: string): TurnProjection {
    const key = turnKey(nodeId, workspaceId, threadId, turnId);
    const current = this.stateValue.turns[key];
    const next: TurnProjection = { ...current, key, ownerId, nodeId, workspaceId, threadId, turnId, items: [...(current?.items ?? [])] };
    this.stateValue.turns[key] = next;
    return next;
  }

  private approvalKey(nodeId: string, workspaceId: string, threadId: string, turnId: string, approvalId: string): string {
    return [nodeId, workspaceId, threadId, turnId, approvalId].join("\u001f");
  }
}

export function workspaceKey(nodeId: string, workspaceId: string): string {
  return [nodeId, workspaceId].join("\u001f");
}

export function agentKey(nodeId: string, agentInstanceId: string): string {
  return [nodeId, agentInstanceId].join("\u001f");
}

export function threadKey(nodeId: string, workspaceId: string, threadId: string): string {
  return [nodeId, workspaceId, threadId].join("\u001f");
}

export function turnKey(nodeId: string, workspaceId: string, threadId: string, turnId: string): string {
  return [nodeId, workspaceId, threadId, turnId].join("\u001f");
}

export function fileChangeKey(nodeId: string, workspaceId: string, threadId: string, turnId: string, path: string): string {
  return [nodeId, workspaceId, threadId, turnId, path].join("\u001f");
}

export function eventBucketKey(event: Pick<RelayMessage, "nodeId" | "workspaceId" | "threadId" | "turnId">): string {
  return [event.nodeId, event.workspaceId ?? "", event.threadId ?? "", event.turnId ?? ""].join("\u001f");
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function normalizeAgentEvent(event: RelayMessage): RelayMessage {
  if (event.protocolVersion !== "1.1") return event;
  const typeMap: Record<string, string> = {
    "task.snapshot": "thread.snapshot",
    "task.started": "thread.started",
    "task.updated": "thread.started",
    "run.started": "turn.started",
    "run.completed": "turn.completed",
    "run.failed": "turn.failed",
    "run.interrupted": "turn.interrupted",
    "message.delta": "agent.message.delta",
    "message.completed": "agent.message.completed",
    "interaction.requested": "approval.requested",
    "interaction.resolved": "approval.resolved",
  };
  let type = typeMap[event.type] ?? event.type;
  const payload: Record<string, unknown> = { ...event.payload };
  if (event.type === "task.snapshot") {
    if (Array.isArray(payload.tasks)) payload.threads = payload.tasks;
    if (isRecord(payload.task)) Object.assign(payload, payload.task);
    if (Array.isArray(payload.runs)) payload.turns = payload.runs;
  }
  if (event.type.startsWith("activity.")) {
    const kind = stringValue(payload.kind) ?? "tool";
    const completed = event.type === "activity.completed";
    if (kind === "command") {
      type = completed ? "command.completed" : event.type === "activity.updated" && typeof payload.output === "string" ? "command.output.delta" : "command.started";
      if (payload.commandId === undefined) payload.commandId = event.activityId ?? stringValue(payload.id);
      if (payload.text === undefined && typeof payload.output === "string") payload.text = payload.output;
    } else {
      type = completed ? "tool.completed" : "tool.started";
      if (payload.toolName === undefined) payload.toolName = stringValue(payload.title) ?? kind;
    }
  }
  if (event.type.startsWith("interaction.")) {
    const kind = stringValue(payload.kind) ?? "";
    if (kind === "question" || kind === "mcp_elicitation") return event;
    if (payload.approvalId === undefined) payload.approvalId = event.interactionId ?? stringValue(payload.id);
  }
  return {
    ...event,
    type: type as RelayMessage["type"],
    payload,
    threadId: event.taskId,
    turnId: event.runId,
    itemId: event.activityId ?? event.interactionId,
  };
}

function isRuntimeMode(value: unknown): value is AgentProjection["runtimeMode"] {
  return value === "managed" || value === "attached" || value === "history-only" || value === "detected-only";
}

function isCapabilityLevel(value: unknown): value is "full" | "read-only" | "unavailable" {
  return value === "full" || value === "read-only" || value === "unavailable";
}

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function booleanValue(value: unknown): boolean | undefined {
  return typeof value === "boolean" ? value : undefined;
}

function numberValue(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function mergeItem(current: ThreadItemProjection, incoming: ThreadItemProjection): ThreadItemProjection {
  const next = { ...current, ...incoming };
  if (current.kind === "agent_message" && incoming.kind === "agent_message" && incoming.sequence !== current.sequence) {
    if (incoming.text === current.text) next.text = current.text;
    else [next.text, next.truncated] = appendBounded(current.text, incoming.text, current.truncated);
  }
  if (current.kind === "reasoning_summary" && incoming.kind === "reasoning_summary" && incoming.sequence !== current.sequence) {
    if (incoming.text === current.text) next.text = current.text;
    else [next.text, next.truncated] = appendBounded(current.text, incoming.text, current.truncated);
  }
  if (current.kind === "command_output" && incoming.kind === "command_output" && incoming.sequence !== current.sequence) {
    [next.output, next.truncated] = appendBounded(current.output, incoming.output, current.truncated);
  }
  if (current.kind === "command" && incoming.kind === "command_output") {
    next.output = `${current.output ?? ""}${incoming.output ?? ""}`;
    next.kind = "command";
  }
  return next;
}

function appendBounded(current = "", incoming = "", alreadyTruncated = false): [string, boolean] {
  const limit = 256 * 1024;
  const combined = `${current}${incoming}`;
  if (combined.length <= limit) return [combined, alreadyTruncated];
  return [combined.slice(0, limit), true];
}
