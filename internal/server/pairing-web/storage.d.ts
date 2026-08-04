export const CONTROL_DATABASE_NAME: string;
export const CONTROL_DATABASE_VERSION: number;
export const CONTROL_STORES: Readonly<{
  keys: string;
  cursors: string;
  sequences: string;
  nodes: string;
  runtimeSettings: string;
}>;

export function openControlDatabase(databaseName?: string, factory?: IDBFactory): Promise<IDBDatabase>;
export function controlStorageKey(...parts: string[]): string;
export function requestValue<T>(request: IDBRequest<T>): Promise<T>;
export function transactionComplete(transaction: IDBTransaction): Promise<void>;
export function withControlStore<T>(storeName: string, mode: IDBTransactionMode, operation: (store: IDBObjectStore) => IDBRequest<T>, databaseName?: string): Promise<T>;
