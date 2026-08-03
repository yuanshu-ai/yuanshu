// @vitest-environment node

import { describe, expect, it } from "vitest";

import type { ControlClient, ControlClientOptions, ControlRequestHandle, ControlTarget, LeaseScope, LeaseState, NodeBinding } from "../relay/control-client";
import { MemoryControlStorage } from "../relay/storage";
import type { YuanshuMessage } from "../protocol/v1/types.generated";
import { fileChangeKey, threadKey, turnKey } from "../state/projection";
import { WorkbenchSession, resourceKey } from "./session";

describe("WorkbenchSession", () => {
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
    await waitFor(() => session.getSnapshot().resources[resourceKey.notifications]?.state === "ready");

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
    await session.loadThread("node-a", "workspace-1", "thread-1", true);
    await session.loadDiff("node-a", "workspace-1", "thread-1", "app.go");

    expect(session.projection.state.threads[threadKey("node-a", "workspace-1", "thread-1")]).toBeDefined();
    expect(session.projection.state.turns[turnKey("node-a", "workspace-1", "thread-1", "turn-1")].items.find((item) => item.id === "agent")?.text).toBe("baseline");
    expect(session.projection.state.files[fileChangeKey("node-a", "workspace-1", "thread-1", "turn-1", "app.go")].diff).toBe("+detail");
    session.close();
  });
});

class FakeClient {
  readonly ready = Promise.resolve();
  state = "idle" as const as ControlClient["state"];
  maxThreadReads = 0;
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
      await this.emit(event("device.status", target.nodeId ?? "node-a", correlationId, { status: "online", workspaces: [1, 2, 3].map((index) => ({ id: `workspace-${index}`, name: `Workspace ${index}` })) }));
    }
    if (type === "thread.list") {
      this.activeThreadReads += 1;
      this.maxThreadReads = Math.max(this.maxThreadReads, this.activeThreadReads);
      await new Promise((resolve) => setTimeout(resolve, 8));
      this.activeThreadReads -= 1;
      await this.emit(event("thread.snapshot", target.nodeId ?? "node-a", correlationId, { threads: [{ id: `thread-${target.workspaceId?.slice(-1)}`, title: `Task ${target.workspaceId}`, status: "idle", updatedAt: "2026-08-03T00:00:00Z" }] }, target.workspaceId));
    }
    if (type === "thread.read") {
      await this.emit(event("thread.snapshot", target.nodeId ?? "node-a", correlationId, { status: "idle", turns: [{ id: "turn-1", status: "completed", items: [{ id: "agent", kind: "agent_message", text: "baseline" }, { id: "file", kind: "file_change", path: "app.go", changeType: "modified" }] }] }, target.workspaceId, target.threadId));
    }
    const result = controlResult(target.nodeId ?? "node-a", correlationId);
    if (type === "notifications.list") {
      result.payload.notifications = [{ id: "notice", nodeId: "node-a", type: "task.completed", summary: "任务已完成", sourceSequence: 1, createdAt: "2026-08-03T00:00:00Z", read: false }];
      this.options.onControlResult?.(result);
    }
    return result;
  }

  async startRequest(_type: string, _payload: Record<string, unknown>, target: ControlTarget = {}, onStarted?: (messageId: string) => void): Promise<ControlRequestHandle> {
    const messageId = `control-${++this.message}`;
    onStarted?.(messageId);
    const result = Promise.resolve().then(async () => {
      await this.emit(event("thread.snapshot", target.nodeId ?? "node-a", messageId, { status: "idle", turns: [{ id: "turn-1", status: "completed", items: [{ id: "file", kind: "file_change", path: "app.go", changeType: "modified", diff: "+detail", truncated: true, totalBytes: 70000, digest: "digest" }] }] }, target.workspaceId, target.threadId));
      return controlResult(target.nodeId ?? "node-a", messageId);
    });
    return { messageId, result };
  }

  private async emit(value: YuanshuMessage) { await this.options.onEvent?.(value); }
}

function event(type: string, nodeId: string, correlationId: string, payload: Record<string, unknown>, workspaceId?: string, threadId?: string): YuanshuMessage {
  const sequence = eventSequence++;
  return { protocolVersion: "1.0", messageId: `event-${sequence}`, type, ownerId: "owner", nodeId, streamId: "node-events-v1", sequence, correlationId, sentAt: "2026-08-03T00:00:00Z", payload, ...(workspaceId ? { workspaceId } : {}), ...(threadId ? { threadId } : {}) };
}

let eventSequence = 0;

function controlResult(nodeId: string, correlationId: string): YuanshuMessage {
  return { protocolVersion: "1.0", messageId: `result-${correlationId}`, type: "control.result", ownerId: "owner", nodeId, streamId: "server-control-v1-client", sequence: 1, correlationId, sentAt: "2026-08-03T00:00:00Z", payload: { status: "confirmed" } };
}

async function waitFor(check: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (check()) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error("condition was not met");
}
