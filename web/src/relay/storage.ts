import {
  CONTROL_DATABASE_NAME,
  CONTROL_STORES,
  controlStorageKey,
  openControlDatabase,
  requestValue,
  transactionComplete,
} from "../../../internal/server/pairing-web/storage.js";

export interface CursorKey {
  ownerId: string;
  nodeId: string;
  streamId: string;
}
export interface ControlSequenceKey {
  ownerId: string;
  nodeId: string;
  clientId: string;
  keyId: string;
}

export interface StoredNodeBinding {
  ownerId: string;
  nodeId: string;
  name?: string;
  version?: string;
  status?: string;
  pairedAt?: string;
  lastSeen?: string;
  online?: boolean;
  discovered?: boolean;
}

export interface StoredControlIdentity {
  ownerId: string;
  /** @deprecated Kept only so identities written by PF-010 can migrate. */
  nodeId?: string;
  clientId: string;
  keyId: string;
  privateKey: CryptoKey;
}

export interface StoredRuntimeSettings {
  relayUrl: string;
  pairingUrl: string;
  displayName?: string;
}

export interface ControlStorage {
  getEventCursor(key: CursorKey): Promise<number>;
  putEventCursor(key: CursorKey, sequence: number): Promise<void>;
  nextControlSequence(key: ControlSequenceKey): Promise<number>;
  getActiveIdentity(): Promise<StoredControlIdentity | undefined>;
  putActiveIdentity(identity: StoredControlIdentity): Promise<void>;
  listNodeBindings(ownerId: string): Promise<StoredNodeBinding[]>;
  putNodeBinding(binding: StoredNodeBinding): Promise<void>;
  removeNodeBinding(ownerId: string, nodeId: string): Promise<void>;
  getRuntimeSettings(): Promise<StoredRuntimeSettings | undefined>;
  putRuntimeSettings(settings: StoredRuntimeSettings): Promise<void>;
  removeRuntimeSettings(): Promise<void>;
}

const KEYS_STORE = CONTROL_STORES.keys;
const CURSORS_STORE = CONTROL_STORES.cursors;
const SEQUENCES_STORE = CONTROL_STORES.sequences;
const NODES_STORE = CONTROL_STORES.nodes;
const RUNTIME_SETTINGS_STORE = CONTROL_STORES.runtimeSettings;

export function cursorStorageKey(key: CursorKey): string {
  return controlStorageKey(key.ownerId, key.nodeId, key.streamId);
}

export function controlSequenceStorageKey(key: ControlSequenceKey): string {
  return controlStorageKey(key.ownerId, key.nodeId, key.clientId, key.keyId);
}

export function nodeBindingStorageKey(ownerId: string, nodeId: string): string {
  return controlStorageKey(ownerId, nodeId);
}

export class IndexedDBControlStorage implements ControlStorage {
  private readonly database: Promise<IDBDatabase>;

  constructor(databaseName = CONTROL_DATABASE_NAME) {
    if (typeof indexedDB === "undefined") throw new Error("IndexedDB is unavailable");
    this.database = openControlDatabase(databaseName);
  }

  async getEventCursor(key: CursorKey): Promise<number> {
    const database = await this.database;
    const value = await requestValue<number | undefined>(database.transaction(CURSORS_STORE, "readonly").objectStore(CURSORS_STORE).get(cursorStorageKey(key)));
    return Number.isSafeInteger(value) && (value as number) >= 0 ? (value as number) : 0;
  }

  async putEventCursor(key: CursorKey, sequence: number): Promise<void> {
    if (!Number.isSafeInteger(sequence) || sequence < 0) throw new Error("event cursor is invalid");
    const database = await this.database;
    const transaction = database.transaction(CURSORS_STORE, "readwrite");
    transaction.objectStore(CURSORS_STORE).put(sequence, cursorStorageKey(key));
    await transactionComplete(transaction);
  }

  async nextControlSequence(key: ControlSequenceKey): Promise<number> {
    const database = await this.database;
    const transaction = database.transaction(SEQUENCES_STORE, "readwrite");
    const store = transaction.objectStore(SEQUENCES_STORE);
    const current = await requestValue<number | undefined>(store.get(controlSequenceStorageKey(key)));
    const next = Number.isSafeInteger(current) && (current as number) >= 0 ? (current as number) + 1 : 1;
    if (!Number.isSafeInteger(next)) throw new Error("control sequence is exhausted");
    store.put(next, controlSequenceStorageKey(key));
    await transactionComplete(transaction);
    return next;
  }

  async getActiveIdentity(): Promise<StoredControlIdentity | undefined> {
    const database = await this.database;
    const transaction = database.transaction(KEYS_STORE, "readonly");
    const value = await requestValue<StoredControlIdentity | undefined>(transaction.objectStore(KEYS_STORE).get("active"));
    return value;
  }

  async putActiveIdentity(identity: StoredControlIdentity): Promise<void> {
    const database = await this.database;
    const transaction = database.transaction(KEYS_STORE, "readwrite");
    transaction.objectStore(KEYS_STORE).put(identity, "active");
    await transactionComplete(transaction);
  }

  async listNodeBindings(ownerId: string): Promise<StoredNodeBinding[]> {
    const database = await this.database;
    const transaction = database.transaction(NODES_STORE, "readonly");
    const values = await requestValue<StoredNodeBinding[]>(transaction.objectStore(NODES_STORE).getAll());
    return values.filter((binding) => binding?.ownerId === ownerId && typeof binding.nodeId === "string").map(copyNodeBinding);
  }

  async putNodeBinding(binding: StoredNodeBinding): Promise<void> {
    validateNodeBinding(binding);
    const database = await this.database;
    const transaction = database.transaction(NODES_STORE, "readwrite");
    transaction.objectStore(NODES_STORE).put(copyNodeBinding(binding), nodeBindingStorageKey(binding.ownerId, binding.nodeId));
    await transactionComplete(transaction);
  }

  async removeNodeBinding(ownerId: string, nodeId: string): Promise<void> {
    const database = await this.database;
    const transaction = database.transaction(NODES_STORE, "readwrite");
    transaction.objectStore(NODES_STORE).delete(nodeBindingStorageKey(ownerId, nodeId));
    await transactionComplete(transaction);
  }

  async getRuntimeSettings(): Promise<StoredRuntimeSettings | undefined> {
    const database = await this.database;
    const transaction = database.transaction(RUNTIME_SETTINGS_STORE, "readonly");
    const value = await requestValue<StoredRuntimeSettings | undefined>(transaction.objectStore(RUNTIME_SETTINGS_STORE).get("active"));
    return value ? { ...value } : undefined;
  }

  async putRuntimeSettings(settings: StoredRuntimeSettings): Promise<void> {
    validateRuntimeSettings(settings);
    const database = await this.database;
    const transaction = database.transaction(RUNTIME_SETTINGS_STORE, "readwrite");
    transaction.objectStore(RUNTIME_SETTINGS_STORE).put({ ...settings }, "active");
    await transactionComplete(transaction);
  }

  async removeRuntimeSettings(): Promise<void> {
    const database = await this.database;
    const transaction = database.transaction(RUNTIME_SETTINGS_STORE, "readwrite");
    transaction.objectStore(RUNTIME_SETTINGS_STORE).delete("active");
    await transactionComplete(transaction);
  }
}

export class MemoryControlStorage implements ControlStorage {
  private readonly cursors = new Map<string, number>();
  private readonly sequences = new Map<string, number>();
  private identity?: StoredControlIdentity;
  private readonly nodes = new Map<string, StoredNodeBinding>();

  getEventCursor(key: CursorKey): Promise<number> {
    return Promise.resolve(this.cursors.get(cursorStorageKey(key)) ?? 0);
  }

  putEventCursor(key: CursorKey, sequence: number): Promise<void> {
    this.cursors.set(cursorStorageKey(key), sequence);
    return Promise.resolve();
  }

  nextControlSequence(key: ControlSequenceKey): Promise<number> {
    const storageKey = controlSequenceStorageKey(key);
    const next = (this.sequences.get(storageKey) ?? 0) + 1;
    this.sequences.set(storageKey, next);
    return Promise.resolve(next);
  }

  getActiveIdentity(): Promise<StoredControlIdentity | undefined> {
    return Promise.resolve(this.identity);
  }

  putActiveIdentity(identity: StoredControlIdentity): Promise<void> {
    this.identity = identity;
    return Promise.resolve();
  }

  listNodeBindings(ownerId: string): Promise<StoredNodeBinding[]> {
    return Promise.resolve([...this.nodes.values()].filter((binding) => binding.ownerId === ownerId).map(copyNodeBinding));
  }

  putNodeBinding(binding: StoredNodeBinding): Promise<void> {
    validateNodeBinding(binding);
    this.nodes.set(nodeBindingStorageKey(binding.ownerId, binding.nodeId), copyNodeBinding(binding));
    return Promise.resolve();
  }

  removeNodeBinding(ownerId: string, nodeId: string): Promise<void> {
    this.nodes.delete(nodeBindingStorageKey(ownerId, nodeId));
    return Promise.resolve();
  }

  getRuntimeSettings(): Promise<StoredRuntimeSettings | undefined> {
    return Promise.resolve(this.runtimeSettings ? { ...this.runtimeSettings } : undefined);
  }

  putRuntimeSettings(settings: StoredRuntimeSettings): Promise<void> {
    validateRuntimeSettings(settings);
    this.runtimeSettings = { ...settings };
    return Promise.resolve();
  }

  removeRuntimeSettings(): Promise<void> {
    this.runtimeSettings = undefined;
    return Promise.resolve();
  }

  private runtimeSettings?: StoredRuntimeSettings;
}

function validateNodeBinding(binding: StoredNodeBinding): void {
  if (!binding.ownerId || !binding.nodeId || (binding.online !== undefined && typeof binding.online !== "boolean")) {
    throw new Error("node binding is invalid");
  }
}

function validateRuntimeSettings(settings: StoredRuntimeSettings): void {
  if (!settings || typeof settings.relayUrl !== "string" || typeof settings.pairingUrl !== "string" || settings.relayUrl.length > 2048 || settings.pairingUrl.length > 2048) {
    throw new Error("runtime settings are invalid");
  }
  if (settings.displayName !== undefined && (typeof settings.displayName !== "string" || settings.displayName.length > 128)) {
    throw new Error("runtime settings are invalid");
  }
}

function copyNodeBinding(binding: StoredNodeBinding): StoredNodeBinding {
  return { ...binding };
}
