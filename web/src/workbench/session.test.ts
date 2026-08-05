// @vitest-environment node

import { afterEach, describe, expect, it, vi } from "vitest";

import type { ControlClient, ControlClientOptions, ControlRequestHandle, ControlTarget, LeaseScope, LeaseState, NodeBinding } from "../relay/control-client";
import { MemoryControlStorage } from "../relay/storage";
import type { YuanshuMessage } from "../protocol/v1/types.generated";
import { fileChangeKey, threadKey, turnKey } from "../state/projection";
import { WorkbenchSession, resourceKey } from "./session";

describe("WorkbenchSession", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("synchronizes every Node and limits Thread list reads to two at a time", async () => {
    let fake!: FakeClient;
    const session = new WorkbenchSession({
      identity: { ownerId: "owner", clientId: "client", keyId: "key", privateKey: {} as CryptoKey },
      settings: { relayUrl: "wss://relay.test/web/connect", pairingUrl: "https://relay.test/pair" },
      storage: new MemoryControlStorage(),
      clientFactory: (options) => (fake = new FakeClient(options)) as unknown as ControlClient,
    });
    await session.initialize();
    session.connect();
    await waitFor(() => Object.values(session.projection.state.threads).length === 3);

    expect(fake.maxThreadReads).toBe(2);
    expect(Object.values(session.projection.state.threads)).toHaveLength(3);
    expect(Object.values(session.projection.state.notifications)).toHaveLength(1);
    expect(session.getSnapshot().resources[resourceKey.node("node-a")].state).toBe("ready");
    session.close();
  });

  it("keeps baseline history when a targeted Diff snapshot arrives", async () => {
    let fake!: FakeClient;
    const session = new WorkbenchSession({
      identity: { ownerId: "owner", clientId: "client", keyId: "key", privateKey: {} as CryptoKey },
      settings: { relayUrl: "wss://relay.test/web/connect", pairingUrl: "https://relay.test/pair" },
      storage: new MemoryControlStorage(),
      clientFactory: (options) => (fake = new FakeClient(options)) as unknown as ControlClient,
    });
    await session.initialize();
    session.connect();
    await waitFor(() => Object.values(session.projection.state.agents).length === 1);
    await session.loadThread("node-a", "workspace-1", "thread-1", true);
    await session.loadDiff("node-a", "workspace-1", "thread-1", "app.go");

    expect(session.projection.state.threads[threadKey("node-a", "workspace-1", "thread-1")]).toBeDefined();
    expect(session.projection.state.turns[turnKey("node-a", "workspace-1", "thread-1", "turn-1")].items.find((item) => item.id === "agent")?.text).toBe("baseline");
    expect(session.projection.state.files[fileChangeKey("node-a", "workspace-1", "thread-1", "turn-1", "app.go")].diff).toBe("+detail");
    session.close();
  });

  it("refreshes presence every 30 seconds only while the page is visible", async () => {
    vi.useFakeTimers();
    const fakeDocument = new EventTarget() as EventTarget & { visibilityState: "visible" | "hidden" };
    fakeDocument.visibilityState = "visible";
    vi.stubGlobal("document", fakeDocument);
    let fake!: FakeClient;
    const session = new WorkbenchSession({
      identity: { ownerId: "owner", clientId: "client", keyId: "key", privateKey: {} as CryptoKey },
      settings: { relayUrl: "wss://relay.test/web/connect", pairingUrl: "https://relay.test/pair" },
      storage: new MemoryControlStorage(),
      clientFactory: (options) => (fake = new FakeClient(options)) as unknown as ControlClient,
    });
    await session.initialize();
    session.connect();
    await vi.advanceTimersByTimeAsync(0);
    const initial = fake.notificationReads;
    expect(initial).toBeGreaterThan(0);

    await vi.advanceTimersByTimeAsync(30_000);
    expect(fake.notificationReads).toBeGreaterThan(initial);
    const visibleReads = fake.notificationReads;
    fakeDocument.visibilityState = "hidden";
    await vi.advanceTimersByTimeAsync(30_000);
    expect(fake.notificationReads).toBe(visibleReads);
    fakeDocument.visibilityState = "visible";
    fakeDocument.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(0);
    expect(fake.notificationReads).toBe(visibleReads + 1);
    session.close();
  });
});

class FakeClient {
  readonly ready = Promise.resolve();
  state = "idle" as const as ControlClient["state"];
  maxThreadReads = 0;
  notificationReads = 0;
  private activeThreadReads = 0;
  private message = 0;
  private readonly nodes: NodeBinding[] = [{ ownerId: "owner", nodeId: "node-a", name: "Office", online: true }];

  constructor(private readonly options: ControlClientOptions) {}

  listNodes() { return this.nodes.map((node) => ({ ...node })); }
  connect() { this.connected(); }
  connected() { this.state = "connected"; this.options.onState?.("connected"); }
  close() { this.state = "closed"; this.options.onState?.("closed"); }
  registerLeaseScope(_scope: LeaseScope) {}
  registerRecoveryTarget() { return () => undefined; }
  getLease(): LeaseState { return { state: "none", epoch: 0 }; }
  canMutate() { return false; }
  acquireLease(): Promise<LeaseState> { return Promise.resolve({ state: "held", leaseId: "lease", epoch: 1, expiresAt: "2099-01-01T00:00:00Z" }); }
  releaseLease(): Promise<LeaseState> { return Promise.resolve({ state: "none", epoch: 2 }); }

  async request(type: string, _payload: Record<string, unknown>, target: ControlTarget = {}): Promise<YuanshuMessage> {
    const correlationId = `control-${++this.message}`;
    if (type === "workspace.list") {
      await this.emit(event("device.status", target.nodeId ?? "node-a", correlationId, { status: "online", workspaces: [1, 2, 3].map((index) => ({ id: `workspace-${index}`, name: `Workspace ${index}`, agents: [{ agentInstanceId: "codex-default", default: true }] })) }));
    }
    if (type === "agent.list") {
      await this.emit(event("agent.snapshot", target.nodeId ?? "node-a", correlationId, { agents: [{ id: "codex-default", adapterType: "codex", displayName: "Codex", runtimeMode: "managed", status: "ready", capabilities: [{ id: "task.start", level: "full" }] }] }));
    }
    if (type === "task.list") {
      this.activeThreadReads += 1;
      this.maxThreadReads = Math.max(this.maxThreadReads, this.activeThreadReads);
      await new Promise((resolve) => setTimeout(resolve, 8));
      this.activeThreadReads -= 1;
      await this.emit(event("task.snapshot", target.nodeId ?? "node-a", correlationId, { tasks: [{ id: `thread-${target.workspaceId?.slice(-1)}`, agentInstanceId: "codex-default", title: `Task ${target.workspaceId}`, status: "idle", updatedAt: "2026-08-03T00:00:00Z" }] }, target.workspaceId, undefined, undefined, "codex-default"));
    }
    if (type === "task.read") {
      await this.emit(event("task.snapshot", target.nodeId ?? "node-a", correlationId, { task: { id: target.taskId, agentInstanceId: "codex-default", status: "idle" }, runs: [{ id: "turn-1", status: "completed", items: [{ id: "agent", kind: "agent_message", text: "baseline" }, { id: "file", kind: "file_change", path: "app.go", changeType: "modified" }] }] }, target.workspaceId, target.taskId, undefined, "codex-default"));
    }
    const result = controlResult(target.nodeId ?? "node-a", correlationId);
    if (type === "notifications.list") {
      this.notificationReads += 1;
      result.payload.notifications = [{ id: "notice", nodeId: "node-a", type: "task.completed", summary: "任务已完成", sourceSequence: 1, createdAt: "2026-08-03T00:00:00Z", read: false }];
      this.options.onControlResult?.(result);
    }
    return result;
  }

  async startRequest(_type: string, _payload: Record<string, unknown>, target: ControlTarget = {}, onStarted?: (messageId: string) => void): Promise<ControlRequestHandle> {
    const messageId = `control-${++this.message}`;
    onStarted?.(messageId);
    const result = Promise.resolve().then(async () => {
      await this.emit(event("task.snapshot", target.nodeId ?? "node-a", messageId, { task: { id: target.taskId, agentInstanceId: "codex-default", status: "idle" }, runs: [{ id: "turn-1", status: "completed", items: [{ id: "file", kind: "file_change", path: "app.go", changeType: "modified", diff: "+detail", truncated: true, totalBytes: 70000, digest: "digest" }] }] }, target.workspaceId, target.taskId, undefined, "codex-default"));
      return controlResult(target.nodeId ?? "node-a", messageId);
    });
    return { messageId, result };
  }

  private async emit(value: YuanshuMessage) { await this.options.onEvent?.(value); }
}

function event(type: string, nodeId: string, correlationId: string, payload: Record<string, unknown>, workspaceId?: string, taskId?: string, runId?: string, agentInstanceId?: string): YuanshuMessage {
  const sequence = eventSequence++;
  return { protocolVersion: "1.1", messageId: `event-${sequence}`, type, ownerId: "owner", nodeId, streamId: "node-events-v1.1", sequence, correlationId, sentAt: "2026-08-03T00:00:00Z", payload, ...(workspaceId ? { workspaceId } : {}), ...(taskId ? { taskId } : {}), ...(runId ? { runId } : {}), ...(agentInstanceId ? { agentInstanceId } : {}) } as YuanshuMessage;
}

let eventSequence = 0;

function controlResult(nodeId: string, correlationId: string): YuanshuMessage {
  return { protocolVersion: "1.1", messageId: `result-${correlationId}`, type: "control.result", ownerId: "owner", nodeId, streamId: "server-control-v1.1-client", sequence: 1, correlationId, sentAt: "2026-08-03T00:00:00Z", payload: { status: "confirmed" } } as YuanshuMessage;
}

async function waitFor(check: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (check()) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error("condition was not met");
}
