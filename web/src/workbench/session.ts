import type { ControlType } from "../protocol/v1/catalog.generated";
import type { YuanshuMessage } from "../protocol/v1/types.generated";
import {
  ControlClient,
  type ControlClientOptions,
  type ControlClientState,
  type ControlRequestHandle,
  type ControlTarget,
  type LeaseScope,
  type LeaseState,
  type NodeBinding,
} from "../relay/control-client";
import type { ControlStorage, StoredControlIdentity } from "../relay/storage";
import type { RuntimeSettings } from "../relay/runtime-config";
import { DataProjection, type ProjectionState } from "../state/projection";

export type ResourceState =
  | { state: "idle" }
  | { state: "loading" }
  | { state: "ready"; updatedAt: string }
  | { state: "empty"; updatedAt: string }
  | { state: "stale"; updatedAt: string; errorCode?: string }
  | { state: "error"; errorCode: string; correlationId?: string; retryable: boolean };

export interface CreatedThreadSignal {
  messageId: string;
  nodeId: string;
  workspaceId: string;
  threadId: string;
}

export interface WorkbenchSnapshot {
  revision: number;
  connectionState: ControlClientState;
  projection: ProjectionState;
  resources: Readonly<Record<string, ResourceState>>;
  createdThread?: CreatedThreadSignal;
}

export interface WorkbenchSessionOptions {
  identity: StoredControlIdentity;
  settings: RuntimeSettings;
  storage: ControlStorage;
  clientFactory?: (options: ControlClientOptions) => ControlClient;
  now?: () => Date;
}

type RequestIntent =
  | { kind: "diff"; path: string }
  | { kind: "thread-start"; nodeId: string; workspaceId: string };

const EMPTY_RESOURCE: ResourceState = { state: "idle" };

export class WorkbenchSession {
  readonly projection = new DataProjection();
  readonly client: ControlClient;
  readonly settings: RuntimeSettings;

  private readonly now: () => Date;
  private readonly listeners = new Set<() => void>();
  private readonly resources: Record<string, ResourceState> = {};
  private readonly requestIntents = new Map<string, RequestIntent>();
  private snapshotValue: WorkbenchSnapshot;
  private revision = 0;
  private synchronizationGeneration = 0;
  private disposed = false;
  private createdThread?: CreatedThreadSignal;
  private presenceTimer?: ReturnType<typeof setInterval>;
  private visibilityListener?: () => void;

  constructor(options: WorkbenchSessionOptions) {
    this.settings = options.settings;
    this.now = options.now ?? (() => new Date());
    const clientOptions: ControlClientOptions = {
      url: options.settings.relayUrl,
      identity: options.identity,
      storage: options.storage,
      onState: (state) => this.handleConnectionState(state),
      onNode: (node) => {
        this.projection.registerNode({ ...node, online: node.online ?? true });
        this.emit();
      },
      onEvent: (event) => this.handleEvent(event),
      onControlAction: (action) => {
        this.projection.applyControlAction(action);
        this.emit();
      },
      onControlResult: (event) => {
        this.projection.applyServerControlResult(event);
        this.emit();
      },
      onLease: () => this.emit(),
    };
    this.client = (options.clientFactory ?? ((value) => new ControlClient(value)))(clientOptions);
    this.snapshotValue = this.makeSnapshot("idle");
  }

  async initialize(): Promise<void> {
    await this.client.ready;
    if (this.disposed) return;
    for (const node of this.client.listNodes()) this.projection.registerNode(node);
    this.emit();
  }

  connect(): void {
    this.client.connect();
    this.startPresenceMonitoring();
  }

  close(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.synchronizationGeneration += 1;
    this.client.close();
    if (this.presenceTimer) clearInterval(this.presenceTimer);
    if (this.visibilityListener && typeof document !== "undefined") document.removeEventListener("visibilitychange", this.visibilityListener);
    this.presenceTimer = undefined;
    this.visibilityListener = undefined;
    this.listeners.clear();
  }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  getSnapshot = (): WorkbenchSnapshot => this.snapshotValue;

  listNodes(): NodeBinding[] {
    return this.client.listNodes();
  }

  resource(key: string): ResourceState {
    return this.resources[key] ?? EMPTY_RESOURCE;
  }

  async refreshAll(): Promise<void> {
    const generation = ++this.synchronizationGeneration;
    await this.synchronize(generation);
  }

  async loadThread(nodeId: string, workspaceId: string, threadId: string, force = false): Promise<void> {
    const key = resourceKey.thread(nodeId, workspaceId, threadId);
    if (!force && this.resources[key]?.state === "loading") return;
    this.client.registerLeaseScope({ nodeId, workspaceId, threadId });
    this.client.registerRecoveryTarget(nodeId, workspaceId, threadId);
    await this.readControl(key, "thread.read", { includeTurns: true, includeDiffs: false }, { nodeId, workspaceId, threadId });
  }

  async loadDiff(nodeId: string, workspaceId: string, threadId: string, path: string): Promise<void> {
    const key = resourceKey.diff(nodeId, workspaceId, threadId, path);
    if (this.resources[key]?.state === "loading") return;
    this.setResource(key, { state: "loading" });
    let messageId = "";
    try {
      const handle = await this.client.startRequest("thread.read", { includeTurns: true, includeDiffs: true, diffPath: path, maxDiffBytes: 65_536 }, { nodeId, workspaceId, threadId }, (startedMessageId) => {
        this.requestIntents.set(startedMessageId, { kind: "diff", path });
      });
      messageId = handle.messageId;
      const result = await handle.result;
      this.assertConfirmed(result);
      this.setResource(key, { state: "ready", updatedAt: this.timestamp() });
    } catch (error) {
      this.setResource(key, resourceError(error));
      throw error;
    } finally {
      if (messageId) this.requestIntents.delete(messageId);
    }
  }

  async startThread(nodeId: string, workspaceId: string, input: string): Promise<ControlRequestHandle> {
    const handle = await this.client.startRequest("thread.start", { input }, { nodeId, workspaceId }, (messageId) => {
      this.requestIntents.set(messageId, { kind: "thread-start", nodeId, workspaceId });
    });
    void handle.result.finally(() => this.requestIntents.delete(handle.messageId)).catch(() => undefined);
    return handle;
  }

  request(type: ControlType, payload: Record<string, unknown>, target: ControlTarget = {}): Promise<YuanshuMessage> {
    return this.client.request(type, payload, target);
  }

  getLease(scope: LeaseScope): LeaseState {
    return this.client.getLease(scope);
  }

  canMutate(scope: LeaseScope, controlType: string): boolean {
    return this.client.canMutate(scope, controlType);
  }

  acquireLease(scope: LeaseScope, options?: { force?: boolean; expectedEpoch?: number }): Promise<LeaseState> {
    return this.client.acquireLease(scope, options);
  }

  releaseLease(scope: LeaseScope): Promise<LeaseState> {
    return this.client.releaseLease(scope);
  }

  async refreshNotifications(): Promise<void> {
    const node = this.client.listNodes()[0];
    if (!node) return;
    await this.readControl(resourceKey.notifications, "notifications.list", { limit: 100 }, { nodeId: node.nodeId });
  }

  async markNotificationRead(notificationId: string): Promise<void> {
    const notification = this.projection.state.notifications[notificationId];
    const node = this.client.listNodes()[0];
    if (!notification || !node) return;
    await this.client.request("notifications.read", { notificationId }, { nodeId: node.nodeId });
    this.projection.markNotificationRead(notificationId);
    this.emit();
  }

  clearCreatedThread(messageId: string): void {
    if (this.createdThread?.messageId !== messageId) return;
    this.createdThread = undefined;
    this.emit();
  }

  private handleConnectionState(state: ControlClientState): void {
    if (this.disposed) return;
    if (state !== "connected") {
      for (const [key, resource] of Object.entries(this.resources)) {
        if (resource.state === "ready" || resource.state === "empty") {
          this.resources[key] = { state: "stale", updatedAt: resource.updatedAt, errorCode: state === "reauth_required" ? "reauth_required" : "connection_lost" };
        }
      }
    }
    this.snapshotValue = this.makeSnapshot(state);
    this.notify();
    if (state === "connected") {
      const generation = ++this.synchronizationGeneration;
      void this.synchronize(generation);
    }
  }

  private handleEvent(event: YuanshuMessage): void {
    if (this.disposed) return;
    const intent = this.requestIntents.get(event.correlationId);
    if (intent?.kind === "diff" && event.type === "thread.snapshot") {
      this.projection.applyDiffSnapshot(event, intent.path);
    } else {
      this.projection.apply(event);
    }
    if (intent?.kind === "thread-start" && event.type === "thread.started" && event.threadId) {
      this.createdThread = {
        messageId: event.correlationId,
        nodeId: intent.nodeId,
        workspaceId: intent.workspaceId,
        threadId: event.threadId,
      };
    }
    this.emit();
  }

  private async synchronize(generation: number): Promise<void> {
    const nodes = this.client.listNodes();
    await this.refreshNotifications().catch(() => undefined);
    if (generation !== this.synchronizationGeneration || this.disposed) return;
    await Promise.all(nodes.map((node) => this.synchronizeNode(node.nodeId, generation)));
    if (generation !== this.synchronizationGeneration || this.disposed) return;

    const workspaces = Object.values(this.projection.state.workspaces);
    await mapLimit(workspaces, 2, async (workspace) => {
      if (generation !== this.synchronizationGeneration || this.disposed) return;
      await this.readControl(
        resourceKey.threads(workspace.nodeId, workspace.workspaceId),
        "thread.list",
        { limit: 100 },
        { nodeId: workspace.nodeId, workspaceId: workspace.workspaceId },
        false,
      );
    });
  }

  private startPresenceMonitoring(): void {
    if (this.presenceTimer || typeof document === "undefined") return;
    const refresh = () => {
      if (!this.disposed && this.client.state === "connected" && document.visibilityState === "visible") {
        void this.refreshNotifications().catch(() => undefined);
      }
    };
    this.visibilityListener = refresh;
    document.addEventListener("visibilitychange", refresh);
    this.presenceTimer = setInterval(refresh, 30_000);
  }

  private async synchronizeNode(nodeId: string, generation: number): Promise<void> {
    const key = resourceKey.node(nodeId);
    this.setResource(key, { state: "loading" });
    try {
      await Promise.all([
        this.client.request("device.sync", {}, { nodeId }),
        this.client.request("workspace.list", { limit: 100 }, { nodeId }),
      ]);
      if (generation !== this.synchronizationGeneration || this.disposed) return;
      const count = Object.values(this.projection.state.workspaces).filter((workspace) => workspace.nodeId === nodeId).length;
      this.setResource(key, { state: count ? "ready" : "empty", updatedAt: this.timestamp() });
    } catch (error) {
      if (generation === this.synchronizationGeneration && !this.disposed) this.setResource(key, resourceError(error));
    }
  }

  private async readControl(key: string, type: ControlType, payload: Record<string, unknown>, target: ControlTarget, rethrow = true): Promise<void> {
    this.setResource(key, { state: "loading" });
    try {
      const result = await this.client.request(type, payload, target);
      this.assertConfirmed(result);
      const empty = type === "thread.list" && !Object.values(this.projection.state.threads).some((thread) => thread.nodeId === target.nodeId && thread.workspaceId === target.workspaceId);
      this.setResource(key, { state: empty ? "empty" : "ready", updatedAt: this.timestamp() });
    } catch (error) {
      this.setResource(key, resourceError(error));
      if (rethrow) throw error;
    }
  }

  private assertConfirmed(result: YuanshuMessage): void {
    const status = typeof result.payload.status === "string" ? result.payload.status : "rejected";
    if (status === "confirmed") return;
    const code = typeof result.payload.errorCode === "string" ? result.payload.errorCode : status;
    throw new Error(code);
  }

  private setResource(key: string, state: ResourceState): void {
    this.resources[key] = state;
    this.emit();
  }

  private emit(): void {
    if (this.disposed) return;
    this.revision += 1;
    this.snapshotValue = this.makeSnapshot(this.client.state);
    this.notify();
  }

  private makeSnapshot(connectionState: ControlClientState): WorkbenchSnapshot {
    return {
      revision: this.revision,
      connectionState,
      projection: this.projection.state,
      resources: { ...this.resources },
      ...(this.createdThread ? { createdThread: { ...this.createdThread } } : {}),
    };
  }

  private notify(): void {
    for (const listener of this.listeners) listener();
  }

  private timestamp(): string {
    return this.now().toISOString();
  }
}

export const resourceKey = {
  node: (nodeId: string) => `node:${nodeId}`,
  threads: (nodeId: string, workspaceId: string) => `threads:${nodeId}:${workspaceId}`,
  thread: (nodeId: string, workspaceId: string, threadId: string) => `thread:${nodeId}:${workspaceId}:${threadId}`,
  diff: (nodeId: string, workspaceId: string, threadId: string, path: string) => `diff:${nodeId}:${workspaceId}:${threadId}:${path}`,
  notifications: "notifications",
  nodeConfig: (nodeId: string) => `node-config:${nodeId}`,
} as const;

function resourceError(error: unknown): ResourceState {
  const message = error instanceof Error ? error.message : "request_failed";
  return {
    state: "error",
    errorCode: normalizeErrorCode(message),
    retryable: !/forbidden|invalid|unsupported|reauth|revoked/i.test(message),
  };
}

function normalizeErrorCode(message: string): string {
  const value = message.trim().toLowerCase().replace(/[^a-z0-9_]+/g, "_").replace(/^_+|_+$/g, "");
  return value.slice(0, 96) || "request_failed";
}

async function mapLimit<T>(items: T[], limit: number, task: (item: T) => Promise<void>): Promise<void> {
  let cursor = 0;
  const workers = Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (cursor < items.length) {
      const index = cursor;
      cursor += 1;
      await task(items[index]);
    }
  });
  await Promise.all(workers);
}
