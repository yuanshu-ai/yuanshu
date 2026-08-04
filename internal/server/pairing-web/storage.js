export const CONTROL_DATABASE_NAME = 'yuanshu-control-client';
export const CONTROL_DATABASE_VERSION = 4;
export const CONTROL_STORES = Object.freeze({
  keys: 'keys',
  cursors: 'event-cursors',
  sequences: 'control-sequences',
  nodes: 'node-bindings',
  runtimeSettings: 'runtime-settings',
});

export function openControlDatabase(databaseName = CONTROL_DATABASE_NAME, factory = globalThis.indexedDB) {
  if (!factory) return Promise.reject(new Error('IndexedDB is unavailable'));
  return new Promise((resolve, reject) => {
    const request = factory.open(databaseName, CONTROL_DATABASE_VERSION);
    request.onupgradeneeded = () => {
      const database = request.result;
      for (const storeName of Object.values(CONTROL_STORES)) {
        if (!database.objectStoreNames.contains(storeName)) database.createObjectStore(storeName);
      }
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error ?? new Error('IndexedDB could not be opened'));
    request.onblocked = () => reject(new Error('IndexedDB upgrade is blocked'));
  });
}

export function controlStorageKey(...parts) {
  return parts.map(part => encodeURIComponent(part)).join('\u001f');
}

export function requestValue(request) {
  return new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error ?? new Error('IndexedDB request failed'));
  });
}

export function transactionComplete(transaction) {
  return new Promise((resolve, reject) => {
    transaction.oncomplete = () => resolve();
    transaction.onerror = () => reject(transaction.error ?? new Error('IndexedDB transaction failed'));
    transaction.onabort = () => reject(transaction.error ?? new Error('IndexedDB transaction aborted'));
  });
}

export async function withControlStore(storeName, mode, operation, databaseName = CONTROL_DATABASE_NAME) {
  const database = await openControlDatabase(databaseName);
  try {
    const transaction = database.transaction(storeName, mode);
    const value = await requestValue(operation(transaction.objectStore(storeName)));
    if (mode === 'readwrite') await transactionComplete(transaction);
    return value;
  } finally {
    database.close();
  }
}
