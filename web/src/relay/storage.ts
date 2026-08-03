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

export interface ControlStorage {
  getEventCursor(key: CursorKey): Promise<number>;
  putEventCursor(key: CursorKey, sequence: number): Promise<void>;
  nextControlSequence(key: ControlSequenceKey): Promise<number>;
  getActiveIdentity(): Promise<StoredControlIdentity | undefined>;
  putActiveIdentity(identity: StoredControlIdentity): Promise<void>;
  listNodeBindings(ownerId: string): Promise<StoredNodeBinding[]>;
  putNodeBinding(binding: StoredNodeBinding): Promise<void>;
  removeNodeBinding(ownerId: string, nodeId: string): Promise<void>;
}

const DATABASE_NAME = "yuanshu-control-client";
const DATABASE_VERSION = 3;
const KEYS_STORE = "keys";
const CURSORS_STORE = "event-cursors";
const SEQUENCES_STORE = "control-sequences";
const NODES_STORE = "node-bindings";

export function cursorStorageKey(key: CursorKey): string {
  return [key.ownerId, key.nodeId, key.streamId].map(encodeURIComponent).join("\u001f");
}

export function controlSequenceStorageKey(key: ControlSequenceKey): string {
  return [key.ownerId, key.nodeId, key.clientId, key.keyId].map(encodeURIComponent).join("\u001f");
}

export function nodeBindingStorageKey(ownerId: string, nodeId: string): string {
  return [ownerId, nodeId].map(encodeURIComponent).join("\u001f");
}

export class IndexedDBControlStorage implements ControlStorage {
  private readonly database: Promise<IDBDatabase>;

  constructor(databaseName = DATABASE_NAME) {
    if (typeof indexedDB === "undefined") throw new Error("IndexedDB is unavailable");
    this.database = openDatabase(databaseName);
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
}

function openDatabase(databaseName: string): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(databaseName, DATABASE_VERSION);
    request.onupgradeneeded = () => {
      const database = request.result;
      if (!database.objectStoreNames.contains(KEYS_STORE)) database.createObjectStore(KEYS_STORE);
      if (!database.objectStoreNames.contains(CURSORS_STORE)) database.createObjectStore(CURSORS_STORE);
      if (!database.objectStoreNames.contains(SEQUENCES_STORE)) database.createObjectStore(SEQUENCES_STORE);
      if (!database.objectStoreNames.contains(NODES_STORE)) database.createObjectStore(NODES_STORE);
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error ?? new Error("IndexedDB could not be opened"));
    request.onblocked = () => reject(new Error("IndexedDB upgrade is blocked"));
  });
}

function validateNodeBinding(binding: StoredNodeBinding): void {
  if (!binding.ownerId || !binding.nodeId || (binding.online !== undefined && typeof binding.online !== "boolean")) {
    throw new Error("node binding is invalid");
  }
}

function copyNodeBinding(binding: StoredNodeBinding): StoredNodeBinding {
  return { ...binding };
}

function requestValue<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error ?? new Error("IndexedDB request failed"));
  });
}

function transactionComplete(transaction: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    transaction.oncomplete = () => resolve();
    transaction.onerror = () => reject(transaction.error ?? new Error("IndexedDB transaction failed"));
    transaction.onabort = () => reject(transaction.error ?? new Error("IndexedDB transaction aborted"));
  });
}
