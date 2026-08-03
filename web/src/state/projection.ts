import type { YuanshuMessage } from "../protocol/v1/types.generated";
import type { ControlAction } from "../relay/control-client";

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
  lastEventSequence: number;
  lastSeen?: string;
}

export interface WorkspaceProjection {
  key: string;
  ownerId: string;
  nodeId: string;
  workspaceId: string;
  name?: string;
  adapter?: string;
  permissionProfile?: string;
}

export interface ThreadProjection {
  key: string;
  ownerId: string;
  nodeId: string;
  workspaceId: string;
  threadId: string;
  status?: string;
  title?: string;
  preview?: string;
  historyState?: "complete" | "partial" | "unavailable";
  createdAt?: string;
  updatedAt?: string;
  pendingApprovals?: number;
  turnIds: string[];
  latestSequence: number;
  recovery: "none" | "pending" | "history_gap";
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

export type ThreadItemKind = "user_message" | "agent_message" | "command" | "command_output" | "tool" | "file_change" | "diff" | "error" | "unknown";

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
  event: YuanshuMessage;
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
  workspaces: Record<string, WorkspaceProjection>;
  threads: Record<string, ThreadProjection>;
  turns: Record<string, TurnProjection>;
  events: Record<string, EventProjection[]>;
  approvals: Record<string, ApprovalProjection>;
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
    workspaces: {},
    threads: {},
    turns: {},
    events: {},
    approvals: {},
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

  applyServerControlResult(event: YuanshuMessage): void {
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

  apply(event: YuanshuMessage): void {
    if (event.ownerId === "" || event.nodeId === "") return;
    const eventKey = `${event.ownerId}\u001f${event.nodeId}\u001f${event.streamId}\u001f${event.sequence}`;
    if (this.seenEvents.has(eventKey)) return;
    this.seenEvents.add(eventKey);

    const node = this.ensureNode(event);
    node.lastEventSequence = Math.max(node.lastEventSequence, event.sequence);
    node.lastSeen = event.sentAt;
    node.online = !["offline", "unavailable", "not_available"].includes(node.status ?? "");

    switch (event.type) {
      case "device.status":
        this.applyDeviceStatus(node, event);
        break;
      case "runtime.status":
        this.applyRuntimeStatus(node, event);
        break;
      case "thread.snapshot":
        this.applyThreadSnapshot(event);
        break;
      case "thread.started":
        this.applyThreadLifecycle(event, "running");
        break;
      case "turn.started":
        this.applyTurnLifecycle(event, "running");
        break;
      case "turn.completed":
        this.applyTurnLifecycle(event, "completed");
        break;
      case "turn.failed":
        this.applyTurnLifecycle(event, "failed");
        break;
      case "turn.interrupted":
        this.applyTurnLifecycle(event, "interrupted");
        break;
      case "agent.message.delta":
        this.applyThreadItem(event, { id: event.itemId ?? `${event.turnId ?? "turn"}:agent`, kind: "agent_message", text: stringValue(event.payload.text) ?? "", status: "streaming", sequence: event.sequence });
        break;
      case "agent.message.completed":
        this.applyThreadItem(event, { id: event.itemId ?? `${event.turnId ?? "turn"}:agent`, kind: "agent_message", text: stringValue(event.payload.text) ?? "", status: "completed", sequence: event.sequence });
        break;
      case "command.started":
      case "command.completed":
        this.applyThreadItem(event, { id: stringValue(event.payload.commandId) ?? event.itemId ?? `${event.sequence}`, kind: "command", command: stringValue(event.payload.displayText) ?? stringValue(event.payload.command), status: event.type === "command.completed" ? "completed" : "running", exitCode: numberValue(event.payload.exitCode), sequence: event.sequence });
        break;
      case "command.output.delta":
        this.applyThreadItem(event, { id: stringValue(event.payload.commandId) ?? event.itemId ?? `${event.sequence}`, kind: "command_output", output: stringValue(event.payload.text) ?? "", status: "streaming", sequence: event.sequence });
        break;
      case "tool.started":
      case "tool.completed":
        this.applyThreadItem(event, { id: event.itemId ?? `${event.sequence}`, kind: "tool", toolName: stringValue(event.payload.toolName), status: event.type === "tool.completed" ? "completed" : "running", sequence: event.sequence });
        break;
      case "file.changed":
        this.applyThreadItem(event, { id: event.itemId ?? `${event.sequence}`, kind: "file_change", path: stringValue(event.payload.path), changeType: stringValue(event.payload.changeType), status: "completed", sequence: event.sequence });
        break;
      case "diff.updated":
        this.applyThreadItem(event, { id: event.itemId ?? stringValue(event.payload.path) ?? `${event.sequence}`, kind: "diff", path: stringValue(event.payload.path), diff: stringValue(event.payload.diff), status: "updated", sequence: event.sequence });
        break;
      case "error":
        this.applyThreadItem(event, { id: event.itemId ?? `${event.sequence}`, kind: "error", errorCode: stringValue(event.payload.code), errorMessage: stringValue(event.payload.message), status: "failed", sequence: event.sequence });
        break;
      case "approval.requested":
        this.applyApprovalRequested(event);
        break;
      case "approval.resolved":
        this.applyApprovalResolved(event);
        break;
      case "history.gap":
        this.applyHistoryGap(event);
        break;
      case "control.result":
        this.applyControlResult(event);
        break;
    }

    if (event.type !== "device.status" && event.type !== "runtime.status" && event.type !== "control.result") {
      this.appendEvent(event);
    }
  }

  /**
   * Applies a targeted Diff read without replacing the Thread history that is
   * already on screen. The request intent is kept by WorkbenchSession and is
   * deliberately not represented on the protocol envelope.
   */
  applyDiffSnapshot(event: YuanshuMessage, path: string): void {
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

  private ensureNode(event: YuanshuMessage): NodeProjection {
    return this.registerNode({ ownerId: event.ownerId, nodeId: event.nodeId });
  }

  private applyDeviceStatus(node: NodeProjection, event: YuanshuMessage): void {
    const payload = event.payload;
    if (typeof payload.status === "string") node.status = payload.status;
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
      });
      if (!node.workspaceIds.includes(workspace.workspaceId)) node.workspaceIds.push(workspace.workspaceId);
    }
  }

  private applyRuntimeStatus(node: NodeProjection, event: YuanshuMessage): void {
    if (typeof event.payload.status === "string") node.runtimeStatus = event.payload.status;
    node.online = !["offline", "unavailable", "not_available"].includes(node.runtimeStatus ?? "");
  }

  private applyThreadSnapshot(event: YuanshuMessage): void {
    const payload = event.payload;
    const workspaceId = event.workspaceId;
    if (!workspaceId) return;
    if (Array.isArray(payload.threads)) {
      for (const raw of payload.threads) {
        if (!isRecord(raw) || typeof raw.id !== "string") continue;
        const thread = this.upsertThread(event.nodeId, event.ownerId, workspaceId, raw.id);
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
    const thread = this.upsertThread(event.nodeId, event.ownerId, workspaceId, event.threadId);
    if (typeof payload.status === "string") thread.status = payload.status;
    this.applyThreadMetadata(thread, payload);
    if (isRecord(payload.thread)) {
      if (typeof payload.thread.status === "string") thread.status = payload.thread.status;
      this.applyThreadMetadata(thread, payload.thread);
    }
    if (payload.historyState === "complete" || payload.historyState === "partial" || payload.historyState === "unavailable") thread.historyState = payload.historyState;
    if (Array.isArray(payload.pendingApprovals)) thread.pendingApprovals = payload.pendingApprovals.length;
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

  private applyThreadLifecycle(event: YuanshuMessage, fallback: string): void {
    if (!event.workspaceId || !event.threadId) return;
    const thread = this.upsertThread(event.nodeId, event.ownerId, event.workspaceId, event.threadId);
    thread.status = stringValue(event.payload.status) ?? fallback;
    this.applyThreadMetadata(thread, event.payload);
    thread.recovery = "none";
    thread.latestSequence = Math.max(thread.latestSequence, event.sequence);
    thread.updatedAt = event.sentAt;
  }

  private applyTurnLifecycle(event: YuanshuMessage, fallback: string): void {
    if (!event.workspaceId || !event.threadId || !event.turnId) return;
    const turn = this.upsertTurn(event.nodeId, event.ownerId, event.workspaceId, event.threadId, event.turnId);
    turn.status = stringValue(event.payload.status) ?? fallback;
    if (event.type === "turn.started") turn.historyState = "partial";
    turn.updatedAt = event.sentAt;
    const thread = this.upsertThread(event.nodeId, event.ownerId, event.workspaceId, event.threadId);
    if (!thread.turnIds.includes(turn.turnId)) thread.turnIds.push(turn.turnId);
    thread.latestSequence = Math.max(thread.latestSequence, event.sequence);
    thread.updatedAt = event.sentAt;
  }

  private applyHistoryGap(event: YuanshuMessage): void {
    if (!event.workspaceId || !event.threadId) return;
    const thread = this.upsertThread(event.nodeId, event.ownerId, event.workspaceId, event.threadId);
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

  private applyThreadItem(event: YuanshuMessage, item: ThreadItemProjection): void {
    if (!event.workspaceId || !event.threadId || !event.turnId) return;
    const turn = this.upsertTurn(event.nodeId, event.ownerId, event.workspaceId, event.threadId, event.turnId);
    const index = turn.items.findIndex((candidate) => candidate.id === item.id);
    if (index < 0) turn.items.push(item);
    else turn.items[index] = mergeItem(turn.items[index], item);
    const thread = this.upsertThread(event.nodeId, event.ownerId, event.workspaceId, event.threadId);
    if (!thread.turnIds.includes(turn.turnId)) thread.turnIds.push(turn.turnId);
    turn.updatedAt = event.sentAt;
    if ((item.kind === "file_change" || item.kind === "diff") && item.path) this.applyFileChange(event, item);
  }

  private itemFromPayload(raw: unknown, sequence: number): ThreadItemProjection | undefined {
    if (!isRecord(raw) || typeof raw.id !== "string" || typeof raw.kind !== "string") return undefined;
    const kinds = new Set<ThreadItemKind>(["user_message", "agent_message", "command", "command_output", "tool", "file_change", "diff", "error", "unknown"]);
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

  private applyFileChange(event: YuanshuMessage, item: ThreadItemProjection, turnId = event.turnId ?? ""): void {
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

  private applyApprovalRequested(event: YuanshuMessage): void {
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

  private applyApprovalResolved(event: YuanshuMessage): void {
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

  private applyControlResult(event: YuanshuMessage): void {
    this.applyControlResultProjection(event);
  }

  private applyControlResultProjection(event: YuanshuMessage): void {
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

  private appendEvent(event: YuanshuMessage): void {
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
    const next: WorkspaceProjection = { ...current, key, ownerId: node.ownerId, nodeId: node.nodeId, workspaceId, ...workspaceValues };
    this.stateValue.workspaces[key] = next;
    return next;
  }

  private upsertThread(nodeId: string, ownerId: string, workspaceId: string, threadId: string): ThreadProjection {
    const key = threadKey(nodeId, workspaceId, threadId);
    const current = this.stateValue.threads[key];
    const next: ThreadProjection = { ...current, key, ownerId, nodeId, workspaceId, threadId, turnIds: [...(current?.turnIds ?? [])], latestSequence: current?.latestSequence ?? 0, recovery: current?.recovery ?? "pending" };
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

export function threadKey(nodeId: string, workspaceId: string, threadId: string): string {
  return [nodeId, workspaceId, threadId].join("\u001f");
}

export function turnKey(nodeId: string, workspaceId: string, threadId: string, turnId: string): string {
  return [nodeId, workspaceId, threadId, turnId].join("\u001f");
}

export function fileChangeKey(nodeId: string, workspaceId: string, threadId: string, turnId: string, path: string): string {
  return [nodeId, workspaceId, threadId, turnId, path].join("\u001f");
}

export function eventBucketKey(event: Pick<YuanshuMessage, "nodeId" | "workspaceId" | "threadId" | "turnId">): string {
  return [event.nodeId, event.workspaceId ?? "", event.threadId ?? "", event.turnId ?? ""].join("\u001f");
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function numberValue(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function mergeItem(current: ThreadItemProjection, incoming: ThreadItemProjection): ThreadItemProjection {
  const next = { ...current, ...incoming };
  if (current.kind === "agent_message" && incoming.kind === "agent_message" && incoming.sequence !== current.sequence) {
    next.text = incoming.text === current.text ? current.text : `${current.text ?? ""}${incoming.text ?? ""}`;
  }
  if (current.kind === "command_output" && incoming.kind === "command_output" && incoming.sequence !== current.sequence) {
    next.output = `${current.output ?? ""}${incoming.output ?? ""}`;
  }
  if (current.kind === "command" && incoming.kind === "command_output") {
    next.output = `${current.output ?? ""}${incoming.output ?? ""}`;
    next.kind = "command";
  }
  return next;
}
