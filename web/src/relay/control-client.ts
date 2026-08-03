import { CURRENT_VERSION, KNOWN_EVENT_TYPES, type ControlType } from "../protocol/v1/catalog.generated";
import { controlSigningInput } from "../protocol/v1/signing";
import type { YuanshuMessage } from "../protocol/v1/types.generated";
import { RELAY_SUBPROTOCOL, sessionSigningInput, type RelayChallenge } from "./session";
import {
  IndexedDBControlStorage,
  type ControlSequenceKey,
  type ControlStorage,
  type CursorKey,
} from "./storage";

export type ControlClientState = "idle" | "connecting" | "authenticating" | "connected" | "reconnecting" | "paused" | "closed" | "reauth_required";

export interface RecoveryTarget {
  workspaceId: string;
  threadId: string;
}

export interface ControlClientIdentity {
  ownerId: string;
  nodeId: string;
  clientId: string;
  keyId: string;
  privateKey: CryptoKey;
}

export interface RelaySocket {
  onopen: (() => void) | null;
  onmessage: ((event: { data: string | ArrayBuffer | Blob }) => void) | null;
  onerror: (() => void) | null;
  onclose: (() => void) | null;
  send(data: string): void;
  close(): void;
}

export interface ControlClientOptions {
  url: string;
  identity: ControlClientIdentity;
  storage?: ControlStorage;
  websocketFactory?: (url: string, protocol: string) => RelaySocket;
  now?: () => Date;
  random?: () => Uint8Array;
  onState?: (state: ControlClientState) => void;
  onEvent?: (event: YuanshuMessage) => void | Promise<void>;
  onControlResult?: (event: YuanshuMessage) => void;
  onUnknownControl?: (control: { messageId: string; type: string }) => void;
}

export interface ControlTarget {
  workspaceId?: string;
  threadId?: string;
  turnId?: string;
  itemId?: string;
}

interface PendingControl {
  messageId: string;
  type: string;
}

interface ReplayState {
  correlationId: string;
  count: number;
  resetAttempted: boolean;
}

const CONTROL_STREAM_ID = "control-stream";
const EVENT_STREAM_ID = "node-events-v1";
const REPLAY_PAGE_SIZE = 256;
const PENDING_LIMIT = 512;
const RECONNECT_INITIAL = 500;
const RECONNECT_MAX = 30_000;
const STABLE_WINDOW = 5_000;
const MAX_AUTH_FAILURES = 10;
const MUTATING_CONTROLS = new Set(["turn.start", "turn.steer", "turn.interrupt", "approval.resolve"]);
const knownEvents = new Set<string>(KNOWN_EVENT_TYPES);

export class ControlClient {
  private readonly options: ControlClientOptions;
  private readonly storage: ControlStorage;
  private readonly now: () => Date;
  private readonly random: () => Uint8Array;
  private readonly websocketFactory: (url: string, protocol: string) => RelaySocket;
  private socket?: RelaySocket;
  private desired = false;
  private stateValue: ControlClientState = "idle";
  private reconnectTimer?: ReturnType<typeof setTimeout>;
  private reconnectAttempt = 0;
  private authFailures = 0;
  private openedAt = 0;
  private eventChain = Promise.resolve();
  private readonly cursors = new Map<string, number>();
  private readonly pendingEvents = new Map<string, Map<number, YuanshuMessage>>();
  private readonly gapStreams = new Set<string>();
  private readonly recoveryTargets = new Map<string, RecoveryTarget>();
  private readonly snapshotRequests = new Set<string>();
  private readonly pendingControls = new Map<string, PendingControl>();
  private replay?: ReplayState;

  constructor(options: ControlClientOptions) {
    if (!options.url || !options.identity.ownerId || !options.identity.nodeId || !options.identity.clientId || !options.identity.keyId || !options.identity.privateKey) {
      throw new Error("control client configuration is invalid");
    }
    this.options = options;
    this.storage = options.storage ?? new IndexedDBControlStorage();
    this.now = options.now ?? (() => new Date());
    this.random = options.random ?? (() => crypto.getRandomValues(new Uint8Array(16)));
    this.websocketFactory = options.websocketFactory ?? ((url, protocol) => new WebSocket(url, protocol) as unknown as RelaySocket);
  }

  get state(): ControlClientState {
    return this.stateValue;
  }

  connect(): void {
    this.desired = true;
    this.clearReconnectTimer();
    if (this.stateValue === "reauth_required" || this.stateValue === "paused" || this.stateValue === "closed") {
      this.authFailures = 0;
    }
    if (!this.socket) this.open();
  }

  close(): void {
    this.desired = false;
    this.clearReconnectTimer();
    this.markUnknownControls();
    const socket = this.socket;
    this.socket = undefined;
    this.replay = undefined;
    if (socket) socket.close();
    this.setState("closed");
  }

  registerRecoveryTarget(workspaceId: string, threadId: string): () => void {
    if (!workspaceId || !threadId) throw new Error("recovery target is invalid");
    const key = `${workspaceId}\u001f${threadId}`;
    this.recoveryTargets.set(key, { workspaceId, threadId });
    return () => this.recoveryTargets.delete(key);
  }

  async sendControl(type: ControlType, payload: Record<string, unknown>, target: ControlTarget = {}): Promise<string> {
    if (!this.socket || this.stateValue !== "connected") throw new Error("control client is not connected");
    const messageId = randomID(this.random());
    const sequenceKey: ControlSequenceKey = {
      ownerId: this.options.identity.ownerId,
      nodeId: this.options.identity.nodeId,
      clientId: this.options.identity.clientId,
      keyId: this.options.identity.keyId,
    };
    const sequence = await this.storage.nextControlSequence(sequenceKey);
    const sentAt = this.now().toISOString();
    const expiresAt = new Date(this.now().getTime() + 120_000).toISOString();
    const message: YuanshuMessage = {
      protocolVersion: CURRENT_VERSION,
      messageId,
      type,
      sentAt,
      expiresAt,
      ownerId: this.options.identity.ownerId,
      nodeId: this.options.identity.nodeId,
      streamId: CONTROL_STREAM_ID,
      sequence,
      correlationId: messageId,
      nonce: randomID(this.random()),
      signer: { clientId: this.options.identity.clientId, keyId: this.options.identity.keyId },
      payload,
      ...(target.workspaceId ? { workspaceId: target.workspaceId } : {}),
      ...(target.threadId ? { threadId: target.threadId } : {}),
      ...(target.turnId ? { turnId: target.turnId } : {}),
      ...(target.itemId ? { itemId: target.itemId } : {}),
    };
    const input = controlSigningInput(message);
    const signature = await crypto.subtle.sign("Ed25519", this.options.identity.privateKey, input as unknown as ArrayBuffer);
    const signed: YuanshuMessage = { ...message, signature: bytesToBase64Url(new Uint8Array(signature)) };
    const socket = this.socket;
    if (!socket || this.stateValue !== "connected") throw new Error("control client disconnected before send");
    this.pendingControls.set(messageId, { messageId, type });
    socket.send(JSON.stringify(signed));
    return messageId;
  }

  sendRecoveryControl(type: "events.replay" | "snapshot.request", payload: Record<string, unknown>, target: ControlTarget = {}): Promise<string> {
    return this.sendControl(type, payload, target);
  }

  private open(): void {
    if (!this.desired || this.socket) return;
    this.setState(this.reconnectAttempt === 0 ? "connecting" : "reconnecting");
    let socket: RelaySocket;
    try {
      socket = this.websocketFactory(this.options.url, RELAY_SUBPROTOCOL);
    } catch {
      this.scheduleReconnect();
      return;
    }
    this.socket = socket;
    socket.onopen = () => {
      this.openedAt = this.now().getTime();
      this.setState("authenticating");
    };
    socket.onmessage = (event) => {
      this.eventChain = this.eventChain.then(() => this.handleMessage(event.data, socket)).catch(() => undefined);
    };
    socket.onerror = () => undefined;
    socket.onclose = () => this.handleClose(socket);
  }

  private async handleMessage(data: string | ArrayBuffer | Blob, socket: RelaySocket): Promise<void> {
    if (socket !== this.socket) return;
    const raw = typeof data === "string" ? data : data instanceof Blob ? await data.text() : new TextDecoder().decode(data);
    let message: Record<string, unknown>;
    try {
      const parsed: unknown = JSON.parse(raw);
      if (!isPlainObject(parsed)) throw new Error("message is not an object");
      message = parsed;
    } catch {
      socket.close();
      return;
    }
    if (this.stateValue === "authenticating") {
      if (message.type === "challenge") {
        await this.authenticate(message);
        return;
      }
      if (message.type === "authenticated") {
        await this.authenticated();
        return;
      }
      socket.close();
      return;
    }
    if (message.type === "authenticated") {
      await this.authenticated();
      return;
    }
    if (!isPlainObject(message.payload) || typeof message.type !== "string") return;
    await this.handleEvent(message as unknown as YuanshuMessage);
  }

  private async authenticate(message: Record<string, unknown>): Promise<void> {
    const challenge = message as unknown as RelayChallenge;
    try {
      if (challenge.subjectId !== this.options.identity.clientId || Number.isNaN(Date.parse(challenge.expiresAt)) || this.now().getTime() >= Date.parse(challenge.expiresAt)) throw new Error("relay challenge is not for this control client");
      const input = sessionSigningInput(challenge);
      const signature = await crypto.subtle.sign("Ed25519", this.options.identity.privateKey, input as unknown as ArrayBuffer);
      this.socket?.send(JSON.stringify({ version: "1", type: "authenticate", signature: bytesToBase64Url(new Uint8Array(signature)) }));
    } catch {
      this.authFailures += 1;
      this.socket?.close();
    }
  }

  private async authenticated(): Promise<void> {
    this.authFailures = 0;
    this.reconnectAttempt = 0;
    this.setState("connected");
    await this.startReplay();
  }

  private handleClose(socket: RelaySocket): void {
    if (socket !== this.socket) return;
    this.socket = undefined;
    if (this.openedAt > 0 && this.now().getTime() - this.openedAt >= STABLE_WINDOW) this.reconnectAttempt = 0;
    if (this.stateValue === "authenticating") this.authFailures += 1;
    if (!this.desired) return;
    this.markUnknownControls();
    this.replay = undefined;
    if (this.authFailures >= MAX_AUTH_FAILURES) {
      this.setState("reauth_required");
      return;
    }
    this.setState("reconnecting");
    this.scheduleReconnect();
  }

  private scheduleReconnect(): void {
    if (!this.desired || this.reconnectTimer || this.authFailures >= MAX_AUTH_FAILURES) return;
    this.setState("reconnecting");
    const base = Math.min(RECONNECT_MAX, RECONNECT_INITIAL * 2 ** this.reconnectAttempt);
    this.reconnectAttempt += 1;
    const jitter = 0.8 + (this.random()[0] / 255) * 0.4;
    const delay = Math.max(1, Math.floor(base * jitter));
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = undefined;
      this.open();
    }, delay);
  }

  private clearReconnectTimer(): void {
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = undefined;
  }

  private async handleEvent(event: YuanshuMessage): Promise<void> {
    if (!this.validEvent(event)) return;
    const cursorKey = this.eventKey(event);
    const cursor = await this.cursor(cursorKey);
    if (event.sequence <= cursor) return;
    const replay = this.replay;
    const isReplayResult = replay && event.type === "control.result" && event.correlationId === replay.correlationId;
    if (replay && !isReplayResult) replay.count += 1;
    if (isReplayResult) {
      await this.applyOrBuffer(event, cursorKey, true);
      await this.finishReplay(event);
      return;
    }
    await this.applyOrBuffer(event, cursorKey, !replay);
  }

  private async applyOrBuffer(event: YuanshuMessage, key: CursorKey, allowRecovery: boolean): Promise<void> {
    const storageKey = this.storageKey(key);
    const cursor = await this.cursor(key);
    if (event.sequence <= cursor) return;
    if (event.sequence === cursor + 1) {
      await this.applyEvent(event, key);
      await this.flushPending(key);
      return;
    }
    this.pendingFor(storageKey).set(event.sequence, event);
    this.gapStreams.add(storageKey);
    if (this.pendingFor(storageKey).size > PENDING_LIMIT) {
      this.pendingFor(storageKey).clear();
      await this.requestSnapshots();
      return;
    }
    if (allowRecovery && !this.replay) await this.startReplay();
  }

  private async applyEvent(event: YuanshuMessage, key: CursorKey): Promise<void> {
    try {
      await this.options.onEvent?.(event);
    } catch {
      this.gapStreams.add(this.storageKey(key));
      return;
    }
    const storageKey = this.storageKey(key);
    const current = await this.cursor(key);
    if (event.sequence <= current) return;
    await this.storage.putEventCursor(key, event.sequence);
    this.cursors.set(storageKey, event.sequence);
    if (event.workspaceId && event.threadId) this.registerRecoveryTarget(event.workspaceId, event.threadId);
    if (event.type === "history.gap") {
      this.gapStreams.add(storageKey);
      await this.requestSnapshots();
    }
    if (event.type === "control.result") {
      this.options.onControlResult?.(event);
      this.resolveControl(event);
    }
  }

  private async flushPending(key: CursorKey): Promise<void> {
    const storageKey = this.storageKey(key);
    const pending = this.pendingFor(storageKey);
    for (;;) {
      const cursor = await this.cursor(key);
      const next = pending.get(cursor + 1);
      if (next) {
        pending.delete(cursor + 1);
        await this.applyEvent(next, key);
        continue;
      }
      const first = [...pending.keys()].sort((left, right) => left - right)[0];
      if (first === undefined) return;
      this.gapStreams.add(storageKey);
      const nextPending = nextEvent(first, pending);
      pending.delete(first);
      await this.applyEvent(nextPending, key);
    }
  }

  private async startReplay(): Promise<void> {
    if (!this.socket || this.stateValue !== "connected" || this.replay) return;
    this.replay = { correlationId: "pending", count: 0, resetAttempted: false };
    try {
      const key: CursorKey = { ownerId: this.options.identity.ownerId, nodeId: this.options.identity.nodeId, streamId: EVENT_STREAM_ID };
      const sequence = await this.cursor(key);
      const messageId = await this.sendRecoveryControl("events.replay", { afterSequence: sequence }, {});
      if (!this.replay) return;
      this.replay.correlationId = messageId;
    } catch {
      this.replay = undefined;
      await this.requestSnapshots();
    }
  }

  private async finishReplay(result: YuanshuMessage): Promise<void> {
    const replay = this.replay;
    if (!replay) return;
    const status = typeof result.payload.status === "string" ? result.payload.status : "rejected";
    const errorCode = typeof result.payload.errorCode === "string" ? result.payload.errorCode : "";
    if (status !== "confirmed") {
      const key: CursorKey = { ownerId: this.options.identity.ownerId, nodeId: this.options.identity.nodeId, streamId: EVENT_STREAM_ID };
      if (!replay.resetAttempted && (errorCode === "conflict" || errorCode === "history_gap")) {
        replay.resetAttempted = true;
        this.cursors.set(this.storageKey(key), 0);
        await this.storage.putEventCursor(key, 0);
        this.pendingFor(this.storageKey(key)).clear();
        this.replay = undefined;
        await this.startReplay();
        return;
      }
      this.replay = undefined;
      await this.requestSnapshots();
      return;
    }
    const key: CursorKey = { ownerId: this.options.identity.ownerId, nodeId: this.options.identity.nodeId, streamId: EVENT_STREAM_ID };
    await this.flushPending(key);
    const hasGap = this.gapStreams.has(this.storageKey(key));
    if (replay.count >= REPLAY_PAGE_SIZE) {
      this.replay = undefined;
      await this.startReplay();
      return;
    }
    this.replay = undefined;
    if (hasGap) await this.requestSnapshots();
  }

  private async requestSnapshots(): Promise<void> {
    if (!this.socket || this.stateValue !== "connected") return;
    for (const target of this.recoveryTargets.values()) {
      const key = `${target.workspaceId}\u001f${target.threadId}`;
      if (this.snapshotRequests.has(key)) continue;
      this.snapshotRequests.add(key);
      try {
        await this.sendRecoveryControl("snapshot.request", {}, target);
      } catch {
        this.snapshotRequests.delete(key);
      }
    }
  }

  private resolveControl(event: YuanshuMessage): void {
    const correlationId = event.correlationId;
    const pending = this.pendingControls.get(correlationId);
    if (pending) this.pendingControls.delete(correlationId);
    if (pending?.type === "snapshot.request" && event.payload.status !== "confirmed" && event.workspaceId && event.threadId) {
      this.snapshotRequests.delete(`${event.workspaceId}\u001f${event.threadId}`);
    }
    if (pending?.type === "snapshot.request" && event.payload.status === "confirmed") {
      this.gapStreams.delete(this.storageKey(this.eventKey(event)));
    }
    if (event.workspaceId && event.threadId && event.type === "control.result" && event.payload.status === "confirmed") {
      this.snapshotRequests.delete(`${event.workspaceId}\u001f${event.threadId}`);
    }
  }

  private markUnknownControls(): void {
    for (const control of this.pendingControls.values()) {
      if (MUTATING_CONTROLS.has(control.type)) this.options.onUnknownControl?.({ messageId: control.messageId, type: control.type });
    }
    this.snapshotRequests.clear();
    this.pendingControls.clear();
  }

  private async cursor(key: CursorKey): Promise<number> {
    const storageKey = this.storageKey(key);
    const existing = this.cursors.get(storageKey);
    if (existing !== undefined) return existing;
    const value = await this.storage.getEventCursor(key);
    const normalized = Number.isSafeInteger(value) && value >= 0 ? value : 0;
    this.cursors.set(storageKey, normalized);
    return normalized;
  }

  private pendingFor(key: string): Map<number, YuanshuMessage> {
    let pending = this.pendingEvents.get(key);
    if (!pending) {
      pending = new Map();
      this.pendingEvents.set(key, pending);
    }
    return pending;
  }

  private eventKey(event: YuanshuMessage): CursorKey {
    return { ownerId: event.ownerId, nodeId: event.nodeId, streamId: event.streamId };
  }

  private storageKey(key: CursorKey): string {
    return `${key.ownerId}\u001f${key.nodeId}\u001f${key.streamId}`;
  }

  private validEvent(event: YuanshuMessage): boolean {
    return event.protocolVersion === CURRENT_VERSION && knownEvents.has(event.type) && event.ownerId === this.options.identity.ownerId && event.nodeId === this.options.identity.nodeId && event.streamId === EVENT_STREAM_ID && Number.isSafeInteger(event.sequence) && event.sequence > 0 && isPlainObject(event.payload);
  }

  private setState(state: ControlClientState): void {
    this.stateValue = state;
    this.options.onState?.(state);
  }
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function randomID(bytes: Uint8Array): string {
  return bytesToBase64Url(bytes);
}

function bytesToBase64Url(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}

function nextEvent(sequence: number, pending: Map<number, YuanshuMessage>): YuanshuMessage {
  const event = pending.get(sequence);
  if (!event) throw new Error("pending event is missing");
  return event;
}
