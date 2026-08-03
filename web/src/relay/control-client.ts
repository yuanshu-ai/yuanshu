import { CURRENT_VERSION, KNOWN_EVENT_TYPES, type ControlType } from "../protocol/v1/catalog.generated";
import { controlSigningInput } from "../protocol/v1/signing";
import type { YuanshuMessage } from "../protocol/v1/types.generated";
import { RELAY_SUBPROTOCOL, sessionSigningInput, type RelayChallenge } from "./session";
import {
  IndexedDBControlStorage,
  type ControlSequenceKey,
  type ControlStorage,
  type CursorKey,
  type StoredNodeBinding,
} from "./storage";

export type ControlClientState = "idle" | "connecting" | "authenticating" | "connected" | "reconnecting" | "paused" | "closed" | "reauth_required";
export type ControlActionState = "sent" | "confirmed" | "rejected" | "ambiguous" | "unknown" | "offline";

export interface RecoveryTarget {
  nodeId: string;
  workspaceId: string;
  threadId: string;
}

export interface ControlClientIdentity {
  ownerId: string;
  clientId: string;
  keyId: string;
  privateKey: CryptoKey;
  /** @deprecated PF-010 identity compatibility; use registerNode instead. */
  nodeId?: string;
}

export type NodeBinding = StoredNodeBinding;

export interface ControlAction {
  messageId: string;
  nodeId: string;
  type: string;
  state: ControlActionState;
  workspaceId?: string;
  threadId?: string;
  turnId?: string;
  itemId?: string;
  errorCode?: string;
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
  nodes?: NodeBinding[];
  storage?: ControlStorage;
  websocketFactory?: (url: string, protocol: string) => RelaySocket;
  now?: () => Date;
  random?: () => Uint8Array;
  onState?: (state: ControlClientState) => void;
  onNode?: (node: NodeBinding) => void;
  onEvent?: (event: YuanshuMessage) => void | Promise<void>;
  onControlResult?: (event: YuanshuMessage) => void;
  onControlAction?: (action: ControlAction) => void;
  onUnknownControl?: (control: { messageId: string; nodeId: string; type: string }) => void;
}

export interface ControlTarget {
  nodeId?: string;
  workspaceId?: string;
  threadId?: string;
  turnId?: string;
  itemId?: string;
}

interface PendingControl {
  action: ControlAction;
  resolve: (event: YuanshuMessage) => void;
  reject: (error: Error) => void;
  result: Promise<YuanshuMessage>;
}

interface ReplayState {
  nodeId: string;
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
  private readonly nodes = new Map<string, NodeBinding>();
  private readonly cursors = new Map<string, number>();
  private readonly pendingEvents = new Map<string, Map<number, YuanshuMessage>>();
  private readonly gapStreams = new Set<string>();
  private readonly historyGapStreams = new Set<string>();
  private readonly recoveryTargets = new Map<string, RecoveryTarget>();
  private readonly snapshotRequests = new Set<string>();
  private readonly pendingControls = new Map<string, PendingControl>();
  private readonly completedResults = new Map<string, YuanshuMessage>();
  private readonly replays = new Map<string, ReplayState>();
  private socket?: RelaySocket;
  private desired = false;
  private stateValue: ControlClientState = "idle";
  private selectedNodeId?: string;
  private reconnectTimer?: ReturnType<typeof setTimeout>;
  private reconnectAttempt = 0;
  private authFailures = 0;
  private openedAt = 0;
  private eventChain = Promise.resolve();
  private readonly restorePromise: Promise<void>;

  constructor(options: ControlClientOptions) {
    if (!options.url || !options.identity.ownerId || !options.identity.clientId || !options.identity.keyId || !options.identity.privateKey) {
      throw new Error("control client configuration is invalid");
    }
    this.options = options;
    this.storage = options.storage ?? new IndexedDBControlStorage();
    this.now = options.now ?? (() => new Date());
    this.random = options.random ?? (() => crypto.getRandomValues(new Uint8Array(16)));
    this.websocketFactory = options.websocketFactory ?? ((url, protocol) => new WebSocket(url, protocol) as unknown as RelaySocket);
    for (const node of options.nodes ?? []) this.registerNode(node, false);
    if (options.identity.nodeId) {
      this.registerNode({ ownerId: options.identity.ownerId, nodeId: options.identity.nodeId, online: true });
      const { nodeId: _legacyNodeId, ...ownerIdentity } = options.identity;
      void this.storage.putActiveIdentity(ownerIdentity).catch(() => undefined);
    }
    this.restorePromise = this.restoreNodes();
  }

  /** Resolves after persisted Node bindings have been merged into this session. */
  get ready(): Promise<void> {
    return this.restorePromise;
  }

  get state(): ControlClientState {
    return this.stateValue;
  }

  get selectedNode(): string | undefined {
    return this.selectedNodeId;
  }

  registerNode(node: NodeBinding, persist = true): void {
    if (!node.ownerId || node.ownerId !== this.options.identity.ownerId || !node.nodeId) throw new Error("node binding is invalid");
    const normalized: NodeBinding = { ...node, online: node.online ?? true };
    this.nodes.set(node.nodeId, normalized);
    this.options.onNode?.({ ...normalized });
    if (persist) void this.storage.putNodeBinding(normalized).catch(() => undefined);
  }

  unregisterNode(nodeId: string): void {
    if (!nodeId) return;
    this.nodes.delete(nodeId);
    if (this.selectedNodeId === nodeId) this.selectedNodeId = undefined;
    this.replays.delete(nodeId);
    for (const key of [...this.recoveryTargets.keys()]) if (key.startsWith(`${nodeId}\u001f`)) this.recoveryTargets.delete(key);
    void this.storage.removeNodeBinding(this.options.identity.ownerId, nodeId).catch(() => undefined);
  }

  listNodes(): NodeBinding[] {
    return [...this.nodes.values()].map((node) => ({ ...node }));
  }

  selectNode(nodeId: string): void {
    if (!this.nodes.has(nodeId)) throw new Error("node is not registered");
    this.selectedNodeId = nodeId;
  }

  connect(): void {
    this.desired = true;
    this.clearReconnectTimer();
    if (this.stateValue === "reauth_required" || this.stateValue === "paused" || this.stateValue === "closed") this.authFailures = 0;
    if (this.socket) return;
    if (this.nodes.size > 0) {
      this.open();
      return;
    }
    void this.restorePromise.then(() => {
      if (this.desired && !this.socket) this.open();
    }).catch(() => this.setState("paused"));
  }

  close(): void {
    this.desired = false;
    this.clearReconnectTimer();
    this.markUnknownControls();
    const socket = this.socket;
    this.socket = undefined;
    this.replays.clear();
    if (socket) socket.close();
    this.setState("closed");
  }

  registerRecoveryTarget(nodeId: string, workspaceId: string, threadId: string): () => void;
  /** @deprecated PF-010 compatibility for a single registered Node. */
  registerRecoveryTarget(workspaceId: string, threadId: string): () => void;
  registerRecoveryTarget(first: string, second: string, third?: string): () => void {
    const nodeId = third === undefined ? this.resolveNodeId({}) : first;
    const workspaceId = third === undefined ? first : second;
    const threadId = third === undefined ? second : third;
    if (!nodeId || !workspaceId || !threadId) throw new Error("recovery target is invalid");
    const key = `${nodeId}\u001f${workspaceId}\u001f${threadId}`;
    this.recoveryTargets.set(key, { nodeId, workspaceId, threadId });
    return () => this.recoveryTargets.delete(key);
  }

  async sendControl(type: ControlType, payload: Record<string, unknown>, target: ControlTarget = {}): Promise<string> {
    const nodeId = this.resolveNodeId(target);
    const node = this.nodes.get(nodeId);
    if (!node) throw new Error("node is not registered");
    const messageId = randomID(this.random());
    if (node.online === false) {
      const action: ControlAction = { messageId, nodeId, type, state: "offline", ...targetWithoutNode(target) };
      this.options.onControlAction?.(action);
      if (MUTATING_CONTROLS.has(type)) this.options.onUnknownControl?.({ messageId, nodeId, type });
      throw new Error("node is offline");
    }
    if (!this.socket || this.stateValue !== "connected") throw new Error("control client is not connected");
    const sequenceKey: ControlSequenceKey = {
      ownerId: this.options.identity.ownerId,
      nodeId,
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
      nodeId,
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
    let resolve!: (event: YuanshuMessage) => void;
    let reject!: (error: Error) => void;
    const result = new Promise<YuanshuMessage>((resolveResult, rejectResult) => { resolve = resolveResult; reject = rejectResult; });
    // sendControl callers do not necessarily wait for a result. Keep the
    // internal request promise from becoming an unhandled rejection when a
    // relay disappears before the result is known.
    void result.catch(() => undefined);
    const action: ControlAction = { messageId, nodeId, type, state: "sent", ...targetWithoutNode(target) };
    this.pendingControls.set(messageId, { action, resolve, reject, result });
    this.options.onControlAction?.({ ...action });
    try {
      socket.send(JSON.stringify(signed));
    } catch (error) {
      this.pendingControls.delete(messageId);
      reject(error instanceof Error ? error : new Error("control send failed"));
      throw error;
    }
    return messageId;
  }

  /** Sends a control and waits for its correlated control.result. */
  async request(type: ControlType, payload: Record<string, unknown>, target: ControlTarget = {}): Promise<YuanshuMessage> {
    const messageId = await this.sendControl(type, payload, target);
    const pending = this.pendingControls.get(messageId);
    if (!pending) {
      const completed = this.completedResults.get(messageId);
      if (completed) {
        this.completedResults.delete(messageId);
        return completed;
      }
      throw new Error("control request was finalized before it could be observed");
    }
    return pending.result;
  }

  sendRecoveryControl(type: "events.replay" | "snapshot.request", payload: Record<string, unknown>, target: ControlTarget = {}): Promise<string> {
    return this.sendControl(type, payload, target);
  }

  private async restoreNodes(): Promise<void> {
    const stored = await this.storage.listNodeBindings(this.options.identity.ownerId);
    for (const node of stored) if (!this.nodes.has(node.nodeId)) this.registerNode(node, false);
  }

  private open(): void {
    if (!this.desired || this.socket) return;
    this.setState(this.reconnectAttempt === 0 ? "connecting" : "reconnecting");
    let socket: RelaySocket;
    try {
      socket = this.websocketFactory(this.relayURL(), RELAY_SUBPROTOCOL);
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
    this.replays.clear();
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
    if (!this.nodes.has(event.nodeId)) {
      this.registerNode({ ownerId: event.ownerId, nodeId: event.nodeId, status: "discovered", discovered: true, online: true });
    }
    const cursorKey = this.eventKey(event);
    const cursor = await this.cursor(cursorKey);
    if (event.sequence <= cursor) return;
    const replay = this.replays.get(event.nodeId);
    const isReplayResult = replay && event.type === "control.result" && event.correlationId === replay.correlationId;
    if (replay && !isReplayResult) replay.count += 1;
    if (isReplayResult) {
      await this.applyOrBuffer(event, cursorKey, true);
      await this.finishReplay(event.nodeId, event);
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
      await this.requestSnapshots(key.nodeId);
      return;
    }
    if (allowRecovery && !this.replays.has(key.nodeId)) await this.startReplayForNode(key.nodeId);
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
    this.observeNodeEvent(event);
    if (event.workspaceId && event.threadId) this.registerRecoveryTarget(event.nodeId, event.workspaceId, event.threadId);
    if (event.type === "history.gap") {
      this.gapStreams.add(storageKey);
      this.historyGapStreams.add(storageKey);
      await this.requestSnapshots(event.nodeId);
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
      if (pending.size === 0 && !this.historyGapStreams.has(storageKey)) this.gapStreams.delete(storageKey);
      return;
    }
  }

  private async startReplay(): Promise<void> {
    for (const node of this.listNodes()) await this.startReplayForNode(node.nodeId);
  }

  private async startReplayForNode(nodeId: string): Promise<void> {
    if (!this.socket || this.stateValue !== "connected" || this.replays.has(nodeId)) return;
    const node = this.nodes.get(nodeId);
    if (!node || node.online === false) return;
    const replay: ReplayState = { nodeId, correlationId: "pending", count: 0, resetAttempted: false };
    this.replays.set(nodeId, replay);
    try {
      const key: CursorKey = { ownerId: this.options.identity.ownerId, nodeId, streamId: EVENT_STREAM_ID };
      const sequence = await this.cursor(key);
      const messageId = await this.sendRecoveryControl("events.replay", { afterSequence: sequence }, { nodeId });
      const current = this.replays.get(nodeId);
      if (current) current.correlationId = messageId;
    } catch {
      this.replays.delete(nodeId);
      await this.requestSnapshots(nodeId);
    }
  }

  private async finishReplay(nodeId: string, result: YuanshuMessage): Promise<void> {
    const replay = this.replays.get(nodeId);
    if (!replay) return;
    const status = typeof result.payload.status === "string" ? result.payload.status : "rejected";
    const errorCode = typeof result.payload.errorCode === "string" ? result.payload.errorCode : "";
    const key: CursorKey = { ownerId: this.options.identity.ownerId, nodeId, streamId: EVENT_STREAM_ID };
    if (status !== "confirmed") {
      if (!replay.resetAttempted && (errorCode === "conflict" || errorCode === "history_gap")) {
        replay.resetAttempted = true;
        this.cursors.set(this.storageKey(key), 0);
        await this.storage.putEventCursor(key, 0);
        this.pendingFor(this.storageKey(key)).clear();
        this.replays.delete(nodeId);
        await this.startReplayForNode(nodeId);
        return;
      }
      this.replays.delete(nodeId);
      await this.requestSnapshots(nodeId);
      return;
    }
    await this.flushPending(key);
    const hasGap = this.gapStreams.has(this.storageKey(key));
    this.replays.delete(nodeId);
    if (replay.count >= REPLAY_PAGE_SIZE) {
      await this.startReplayForNode(nodeId);
      return;
    }
    if (hasGap) await this.requestSnapshots(nodeId);
  }

  private async requestSnapshots(nodeId: string): Promise<void> {
    if (!this.socket || this.stateValue !== "connected") return;
    for (const target of this.recoveryTargets.values()) {
      if (target.nodeId !== nodeId) continue;
      const key = `${target.nodeId}\u001f${target.workspaceId}\u001f${target.threadId}`;
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
    const pending = this.pendingControls.get(event.correlationId);
    if (!pending) return;
    const rawStatus = typeof event.payload.status === "string" ? event.payload.status : "rejected";
    const state: ControlActionState = rawStatus === "confirmed" || rawStatus === "rejected" || rawStatus === "ambiguous" ? rawStatus : "sent";
    const action = { ...pending.action, state, ...(typeof event.payload.errorCode === "string" ? { errorCode: event.payload.errorCode } : {}) };
    this.options.onControlAction?.(action);
    this.pendingControls.get(event.correlationId)?.resolve(event);
    if (pending.action.type === "snapshot.request" && state !== "confirmed" && event.workspaceId && event.threadId) {
      this.snapshotRequests.delete(`${pending.action.nodeId}\u001f${event.workspaceId}\u001f${event.threadId}`);
    }
    if (pending.action.type === "snapshot.request" && state === "confirmed" && event.workspaceId && event.threadId) {
      this.gapStreams.delete(this.storageKey(this.eventKey(event)));
      this.historyGapStreams.delete(this.storageKey(this.eventKey(event)));
      this.snapshotRequests.delete(`${pending.action.nodeId}\u001f${event.workspaceId}\u001f${event.threadId}`);
    }
    if (state !== "sent") {
      this.completedResults.set(event.correlationId, event);
      if (this.completedResults.size > 512) this.completedResults.delete(this.completedResults.keys().next().value as string);
      this.pendingControls.delete(event.correlationId);
    }
  }

  private markUnknownControls(): void {
    for (const [messageId, pending] of this.pendingControls) {
      const { action } = pending;
      if (MUTATING_CONTROLS.has(action.type)) {
        const unknown = { ...action, state: "unknown" as const };
        this.options.onControlAction?.(unknown);
        this.options.onUnknownControl?.({ messageId, nodeId: action.nodeId, type: action.type });
      }
      pending.reject(new Error("control result is unknown because the relay disconnected"));
      this.pendingControls.delete(messageId);
    }
    this.snapshotRequests.clear();
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
    return event.protocolVersion === CURRENT_VERSION && knownEvents.has(event.type) && event.ownerId === this.options.identity.ownerId && !!event.nodeId && event.streamId === EVENT_STREAM_ID && Number.isSafeInteger(event.sequence) && event.sequence > 0 && isPlainObject(event.payload);
  }

  private observeNodeEvent(event: YuanshuMessage): void {
    const node = this.nodes.get(event.nodeId);
    if (!node) return;
    const payload = event.payload;
    const status = typeof payload.status === "string" ? payload.status : undefined;
    const patch: NodeBinding = {
      ...node,
      online: status ? !new Set(["offline", "unavailable", "not_available"]).has(status) : true,
      lastSeen: event.sentAt,
    };
    if (event.type === "device.status" || event.type === "runtime.status") {
      if (typeof payload.status === "string") patch.status = payload.status;
      if (typeof payload.name === "string") patch.name = payload.name;
      if (typeof payload.version === "string") patch.version = payload.version;
    }
    this.registerNode(patch);
  }

  private resolveNodeId(target: ControlTarget): string {
    if (target.nodeId) return target.nodeId;
    if (this.nodes.size === 1) return [...this.nodes.keys()][0];
    throw new Error("node target is required when multiple Nodes are registered");
  }

  private setState(state: ControlClientState): void {
    this.stateValue = state;
    this.options.onState?.(state);
  }

  private relayURL(): string {
    const base = typeof location === "undefined" ? "http://localhost" : location.origin;
    const url = new URL(this.options.url, base);
    if (!url.searchParams.has("clientId")) url.searchParams.set("clientId", this.options.identity.clientId);
    return url.toString();
  }
}

function targetWithoutNode(target: ControlTarget): Pick<ControlTarget, "workspaceId" | "threadId" | "turnId" | "itemId"> {
  const { workspaceId, threadId, turnId, itemId } = target;
  return { ...(workspaceId ? { workspaceId } : {}), ...(threadId ? { threadId } : {}), ...(turnId ? { turnId } : {}), ...(itemId ? { itemId } : {}) };
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
