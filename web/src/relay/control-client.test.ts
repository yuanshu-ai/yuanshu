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
    expect(mutation.sequence).toBe(2);
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
});

function challenge(): Record<string, unknown> {
  return {
    version: "1", type: "challenge", role: "control", connectionId: "connection", subjectId: "client",
    nonce: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", expiresAt: "2099-01-01T00:00:00Z",
  };
}

function event(type: string, sequence: number, correlationId: string): Record<string, unknown> {
  return {
    protocolVersion: "1.0", messageId: `event-${sequence}`, type, ownerId: "owner", nodeId: "node", streamId: "node-events-v1",
    sequence, correlationId, sentAt: "2026-08-03T00:00:00Z", payload: { status: "confirmed" },
  };
}

async function tick(): Promise<void> {
  for (let attempt = 0; attempt < 10; attempt += 1) await Promise.resolve();
  await new Promise((resolve) => setTimeout(resolve, 0));
}
