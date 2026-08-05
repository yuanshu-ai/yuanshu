// @vitest-environment node

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ControlClient, type RelaySocket } from "./control-client";
import { MemoryControlStorage } from "./storage";

class FakeSocket implements RelaySocket {
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string | ArrayBuffer | Blob }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;
  readonly sent: string[] = [];

  send(data: string): void {
    this.sent.push(data);
  }

  close(): void {
    this.onclose?.();
  }

  open(): void {
    this.onopen?.();
  }

  receive(message: Record<string, unknown>): void {
    this.onmessage?.({ data: JSON.stringify(message) });
  }
}

describe("ControlClient recovery", () => {
  afterEach(() => vi.useRealTimers());

  it("authenticates, persists the cursor, and replays from it after reconnect", async () => {
    vi.useRealTimers();
    const keyPair = await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"]);
    const storage = new MemoryControlStorage();
    const sockets: FakeSocket[] = [];
    let randomValue = 0;
    const client = new ControlClient({
      url: "wss://relay.test/web/connect?clientId=client",
      identity: { ownerId: "owner", nodeId: "node", clientId: "client", keyId: "key", privateKey: keyPair.privateKey },
      storage,
      random: () => new Uint8Array(16).fill(++randomValue),
      websocketFactory: () => {
        const socket = new FakeSocket();
        sockets.push(socket);
        return socket;
      },
    });

    client.connect();
    sockets[0].open();
    sockets[0].receive(challenge());
    await tick();
    expect(JSON.parse(sockets[0].sent[0]).type).toBe("authenticate");
    sockets[0].receive({ version: "1", type: "authenticated" });
    await tick();
    const initialReplay = JSON.parse(sockets[0].sent[1]);
    expect(initialReplay.type).toBe("events.replay");
    expect(initialReplay.payload.afterSequence).toBe(0);
    sockets[0].receive(event("control.result", 1, initialReplay.messageId));
    await tick();
    expect(await storage.getEventCursor({ ownerId: "owner", nodeId: "node", streamId: "node-events-v1" })).toBe(1);

    const acquire = client.acquireLease({ nodeId: "node", workspaceId: "workspace", threadId: "thread" });
    await tick();
    const acquireMessage = JSON.parse(sockets[0].sent.at(-1) as string);
    sockets[0].receive(serverResult("node", 1, acquireMessage.messageId, { state: "held", leaseId: "lease-1", holderClientId: "client", epoch: 1, expiresAt: "2099-01-01T00:01:00Z" }));
    await acquire;
    await client.sendControl("turn.start", { input: "do not resend" }, { workspaceId: "workspace", threadId: "thread" });
    const mutation = JSON.parse(sockets[0].sent.at(-1) as string);
    sockets[0].close();
    await new Promise((resolve) => setTimeout(resolve, 800));
    expect(sockets).toHaveLength(2);
    sockets[1].open();
    sockets[1].receive(challenge());
    await tick();
    sockets[1].receive({ version: "1", type: "authenticated" });
    await tick();

    const resentTypes = sockets[1].sent.map((value) => JSON.parse(value).type);
    expect(resentTypes).toContain("authenticate");
    expect(resentTypes).toContain("events.replay");
    expect(resentTypes).not.toContain("turn.start");
    expect(mutation.sequence).toBe(3);
    expect(JSON.parse(sockets[1].sent.at(-1) as string).payload.afterSequence).toBe(1);
    client.close();
  });

  it("does not reconnect after an explicit close", async () => {
    vi.useFakeTimers();
    const keyPair = await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"]);
    const sockets: FakeSocket[] = [];
    const client = new ControlClient({
      url: "wss://relay.test/web/connect?clientId=client",
      identity: { ownerId: "owner", nodeId: "node", clientId: "client", keyId: "key", privateKey: keyPair.privateKey },
      storage: new MemoryControlStorage(),
      websocketFactory: () => {
        const socket = new FakeSocket();
        sockets.push(socket);
        return socket;
      },
    });
    client.connect();
    client.close();
    await vi.advanceTimersByTimeAsync(10_000);
    expect(sockets).toHaveLength(1);
    expect(client.state).toBe("closed");
  });

  it("keeps Node bindings, cursors, and control sequences isolated", async () => {
    const keyPair = await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"]);
    const storage = new MemoryControlStorage();
    const sockets: FakeSocket[] = [];
    const received: Record<string, unknown>[] = [];
    const client = new ControlClient({
      url: "wss://relay.test/web/connect?clientId=client",
      identity: { ownerId: "owner", clientId: "client", keyId: "key", privateKey: keyPair.privateKey },
      nodes: [{ ownerId: "owner", nodeId: "node-a", name: "Office", online: true }, { ownerId: "owner", nodeId: "node-b", name: "Home", online: true }],
      storage,
      websocketFactory: () => {
        const socket = new FakeSocket();
        sockets.push(socket);
        return socket;
      },
      onEvent: (event) => { received.push(event as unknown as Record<string, unknown>); },
    });

    client.connect();
    sockets[0].open();
    sockets[0].receive(challenge());
    await tick();
    sockets[0].receive({ version: "1", type: "authenticated" });
    await tick();
    const replayMessages = sockets[0].sent.map((raw) => JSON.parse(raw)).filter((message) => message.type === "events.replay");
    expect(replayMessages).toHaveLength(2);
    expect(replayMessages.map((message) => message.nodeId).sort()).toEqual(["node-a", "node-b"]);
    for (const message of replayMessages) sockets[0].receive(eventFor(message.nodeId, "control.result", 1, message.messageId));
    await tick();

    await client.sendControl("device.sync", {}, { nodeId: "node-a" });
    await client.sendControl("device.sync", {}, { nodeId: "node-b" });
    const controls = sockets[0].sent.map((raw) => JSON.parse(raw)).filter((message) => message.type === "device.sync");
    expect(controls.map((message) => [message.nodeId, message.sequence])).toEqual([["node-a", 2], ["node-b", 2]]);

    sockets[0].receive(eventFor("node-a", "device.status", 2, "a-status", { status: "online", name: "Office" }));
    sockets[0].receive(eventFor("node-b", "device.status", 2, "b-status", { status: "offline", name: "Home" }));
    await tick();
    expect(received.filter((event) => event.type === "device.status").map((event) => event.nodeId)).toEqual(["node-a", "node-b"]);
    expect(await storage.getEventCursor({ ownerId: "owner", nodeId: "node-a", streamId: "node-events-v1" })).toBe(2);
    expect(await storage.getEventCursor({ ownerId: "owner", nodeId: "node-b", streamId: "node-events-v1" })).toBe(2);
    expect(client.listNodes().map((node) => [node.nodeId, node.name])).toEqual([["node-a", "Office"], ["node-b", "Home"]]);
    const refreshed = new ControlClient({
      url: "wss://relay.test/web/connect?clientId=client",
      identity: { ownerId: "owner", clientId: "client", keyId: "key", privateKey: keyPair.privateKey },
      storage,
    });
    await refreshed.ready;
    expect(refreshed.listNodes().map((node) => node.nodeId)).toEqual(["node-a", "node-b"]);
    client.close();
  });

  it("reports a registered offline Node without sending a side-effect", async () => {
    const keyPair = await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"]);
    const actions: string[] = [];
    const client = new ControlClient({
      url: "wss://relay.test/web/connect?clientId=client",
      identity: { ownerId: "owner", clientId: "client", keyId: "key", privateKey: keyPair.privateKey },
      nodes: [{ ownerId: "owner", nodeId: "offline", online: false }],
      storage: new MemoryControlStorage(),
      onControlAction: (action) => actions.push(`${action.nodeId}:${action.state}`),
    });
    await expect(client.sendControl("turn.start", { input: "do not send" }, { nodeId: "offline" })).rejects.toThrow("offline");
    expect(actions).toEqual(["offline:offline"]);
  });

  it("applies an Owner-scoped presence snapshot to registered Node bindings", async () => {
    const keyPair = await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"]);
    const storage = new MemoryControlStorage();
    const socket = new FakeSocket();
    const client = new ControlClient({
      url: "wss://relay.test/web/connect",
      identity: { ownerId: "owner", clientId: "client", keyId: "key", privateKey: keyPair.privateKey },
      nodes: [{ ownerId: "owner", nodeId: "node-a", online: true }, { ownerId: "owner", nodeId: "node-b", online: true }],
      storage,
      websocketFactory: () => socket,
    });
    client.connect();
    socket.open();
    socket.receive(challenge());
    await tick();
    socket.receive({ version: "1", type: "authenticated" });
    await tick();

    socket.receive({
      protocolVersion: "1.0", messageId: "presence", type: "control.result", ownerId: "owner", nodeId: "node-a",
      streamId: "server-control-v1-client", sequence: 1, correlationId: "notifications", sentAt: "2026-08-03T00:00:00Z",
      payload: { status: "confirmed", onlineNodeIds: ["node-a", "unknown-node"], notifications: [] },
    });
    await tick();

    expect(client.listNodes().map((node) => [node.nodeId, node.online])).toEqual([["node-a", true], ["node-b", false]]);
    expect((await storage.listNodeBindings("owner")).map((node) => [node.nodeId, node.online])).toEqual([["node-a", true], ["node-b", false]]);
    client.close();
  });

  it("exposes a request message ID before the correlated result resolves", async () => {
    const keyPair = await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"]);
    const socket = new FakeSocket();
    const actions: string[] = [];
    const client = new ControlClient({
      url: "wss://relay.test/web/connect",
      identity: { ownerId: "owner", nodeId: "node", clientId: "client", keyId: "key", privateKey: keyPair.privateKey },
      storage: new MemoryControlStorage(),
      websocketFactory: () => socket,
      onControlAction: (action) => actions.push(action.state),
    });
    client.connect();
    socket.open();
    socket.receive(challenge());
    await tick();
    socket.receive({ version: "1", type: "authenticated" });
    await tick();
    const replay = JSON.parse(socket.sent.at(-1) as string);
    socket.receive(event("control.result", 1, replay.messageId));
    await tick();

    const handle = await client.startRequest("device.sync", {}, { nodeId: "node" });
    expect(handle.messageId).toBeTruthy();
    let settled = false;
    void handle.result.then(() => { settled = true; });
    await Promise.resolve();
    expect(settled).toBe(false);
    socket.receive(eventFor("node", "control.result", 2, handle.messageId, { status: "dispatching" }));
    await tick();
    expect(settled).toBe(false);
    expect(actions.at(-1)).toBe("executing");
    socket.receive(event("control.result", 3, handle.messageId));
    await expect(handle.result).resolves.toMatchObject({ correlationId: handle.messageId });
    client.close();
  });

  it("uses the Protocol 1.1 event stream without mixing legacy cursors", async () => {
    const keyPair = await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"]);
    const storage = new MemoryControlStorage();
    const socket = new FakeSocket();
    const received: string[] = [];
    const client = new ControlClient({
      url: "wss://relay.test/web/connect",
      protocolVersion: "1.1",
      identity: { ownerId: "owner", clientId: "client", keyId: "key", privateKey: keyPair.privateKey },
      nodes: [{ ownerId: "owner", nodeId: "node", online: true }],
      storage,
      websocketFactory: () => socket,
      onEvent: (value) => { received.push(value.type); },
    });

    client.connect();
    socket.open();
    socket.receive(challenge());
    await tick();
    socket.receive({ version: "1", type: "authenticated" });
    await tick();

    const replay = socket.sent.map((raw) => JSON.parse(raw)).find((message) => message.type === "events.replay");
    expect(replay).toMatchObject({ protocolVersion: "1.1", nodeId: "node", payload: { afterSequence: 0 } });
    socket.receive(eventForV11("node", "agent.snapshot", 1, "agent-snapshot", { agents: [] }));
    socket.receive(eventForV11("node", "control.result", 2, replay.messageId));
    socket.receive(eventFor("node", "device.status", 3, "legacy", { status: "online" }));
    await tick();

    expect(received).toEqual(["agent.snapshot", "control.result"]);
    expect(await storage.getEventCursor({ ownerId: "owner", nodeId: "node", streamId: "node-events-v1.1" })).toBe(2);
    expect(await storage.getEventCursor({ ownerId: "owner", nodeId: "node", streamId: "node-events-v1" })).toBe(0);
    await client.sendControl("agent.list", {}, { nodeId: "node" });
    expect(JSON.parse(socket.sent.at(-1) as string)).toMatchObject({ protocolVersion: "1.1", type: "agent.list", nodeId: "node" });
    client.close();
  });
});

function challenge(): Record<string, unknown> {
  return {
    version: "1", type: "challenge", role: "control", connectionId: "connection", subjectId: "client",
    nonce: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", expiresAt: "2099-01-01T00:00:00Z",
  };
}

function event(type: string, sequence: number, correlationId: string): Record<string, unknown> {
  return eventFor("node", type, sequence, correlationId);
}

function eventFor(nodeId: string, type: string, sequence: number, correlationId: string, payload: Record<string, unknown> = { status: "confirmed" }): Record<string, unknown> {
  return {
    protocolVersion: "1.0", messageId: `${nodeId}-event-${sequence}`, type, ownerId: "owner", nodeId, streamId: "node-events-v1",
    sequence, correlationId, sentAt: "2026-08-03T00:00:00Z", payload,
  };
}

function eventForV11(nodeId: string, type: string, sequence: number, correlationId: string, payload: Record<string, unknown> = { status: "confirmed" }): Record<string, unknown> {
  return {
    protocolVersion: "1.1", messageId: `${nodeId}-v11-event-${sequence}`, type, ownerId: "owner", nodeId, streamId: "node-events-v1.1",
    sequence, correlationId, sentAt: "2026-08-03T00:00:00Z", payload,
  };
}

function serverResult(nodeId: string, sequence: number, correlationId: string, lease: Record<string, unknown>): Record<string, unknown> {
  return {
    protocolVersion: "1.0", messageId: `server-result-${sequence}`, type: "control.result", ownerId: "owner", nodeId,
    streamId: "server-control-v1-client", sequence, correlationId, sentAt: "2026-08-03T00:00:00Z", payload: { status: "confirmed", lease },
  };
}

async function tick(): Promise<void> {
  for (let attempt = 0; attempt < 20; attempt += 1) await Promise.resolve();
  await new Promise((resolve) => setTimeout(resolve, 10));
}
