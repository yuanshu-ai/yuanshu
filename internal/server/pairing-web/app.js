import { base64url, sessionSigningInput } from '/pair/session.js';

const button = document.querySelector('#pair');
const statusText = document.querySelector('#status');
const result = document.querySelector('#result');
const details = document.querySelector('#details');
const fingerprint = document.querySelector('#fingerprint');
let activeSocket;
let realtimeTimer;
let realtimeAttempt = 0;
let realtimeAuthFailures = 0;
let realtimeGeneration = 0;

const setStatus = (text, state = '') => {
  statusText.textContent = text;
  result.className = `result ${state}`;
};
const randomID = prefix => `${prefix}_${base64url(crypto.getRandomValues(new Uint8Array(18)))}`;
const parseSecret = () => {
  const raw = location.hash.slice(1);
  const split = raw.indexOf('.');
  if (split < 1) return null;
  return { pairingId: raw.slice(0, split), secret: raw.slice(split + 1) };
};
const openDB = () => new Promise((resolve, reject) => {
  const request = indexedDB.open('yuanshu-control-client', 3);
  request.onupgradeneeded = () => {
    const database = request.result;
    if (!database.objectStoreNames.contains('keys')) database.createObjectStore('keys');
    if (!database.objectStoreNames.contains('event-cursors')) database.createObjectStore('event-cursors');
    if (!database.objectStoreNames.contains('control-sequences')) database.createObjectStore('control-sequences');
    if (!database.objectStoreNames.contains('node-bindings')) database.createObjectStore('node-bindings');
  };
  request.onsuccess = () => resolve(request.result);
  request.onerror = () => reject(new Error('storage unavailable'));
});
const withStore = async (storeName, mode, operation) => {
  const db = await openDB();
  try {
    return await new Promise((resolve, reject) => {
      const tx = db.transaction(storeName, mode);
      const request = operation(tx.objectStore(storeName));
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(new Error('storage unavailable'));
    });
  } finally {
    db.close();
  }
};
const storeKey = (id, value) => withStore('keys', 'readwrite', store => store.put(value, id));
const getKey = id => withStore('keys', 'readonly', store => store.get(id));
const deleteKey = id => withStore('keys', 'readwrite', store => store.delete(id));
const storeNodeBinding = binding => withStore('node-bindings', 'readwrite', store => store.put(binding, `${binding.ownerId}\u001f${binding.nodeId}`));

const connectRealtime = client => {
  if (!client?.clientId || !client?.privateKey) return Promise.reject(new Error('identity unavailable'));
  const generation = ++realtimeGeneration;
  if (realtimeTimer) clearTimeout(realtimeTimer);
  if (activeSocket) activeSocket.close();
  realtimeAttempt = 0;
  realtimeAuthFailures = 0;
  let settled = false;
  let firstConnection;
  const firstConnected = new Promise((resolve, reject) => { firstConnection = { resolve, reject }; });
  const schedule = () => {
    if (generation !== realtimeGeneration || realtimeAuthFailures >= 10 || realtimeTimer) return;
    setStatus(realtimeAuthFailures >= 10 ? '需要重新配对' : '实时连接已断开，正在重连', 'waiting');
    const base = Math.min(30000, 500 * 2 ** realtimeAttempt++);
    const jitter = 0.8 + crypto.getRandomValues(new Uint8Array(1))[0] / 255 * 0.4;
    realtimeTimer = setTimeout(() => {
      realtimeTimer = undefined;
      attempt();
    }, Math.max(1, Math.floor(base * jitter)));
  };
  const attempt = () => {
    if (generation !== realtimeGeneration) return;
    setStatus(realtimeAttempt ? '正在重连安全实时连接' : '正在建立安全实时连接', 'waiting');
    const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:';
    let socket;
    try {
      socket = new WebSocket(`${scheme}//${location.host}/web/connect?clientId=${encodeURIComponent(client.clientId)}`, 'yuanshu-relay-v1');
    } catch {
      schedule();
      return;
    }
    activeSocket = socket;
    let authenticated = false;
    let challenged = false;
    socket.onerror = () => {};
    socket.onclose = () => {
      if (activeSocket === socket) activeSocket = undefined;
      if (!authenticated) realtimeAuthFailures += 1;
      if (!settled) {
        if (realtimeAuthFailures >= 10) {
          settled = true;
          firstConnection.reject(new Error('reauthentication required'));
          setStatus('需要重新配对', 'error');
        } else {
          schedule();
        }
      } else {
        schedule();
      }
    };
    socket.onmessage = async event => {
      try {
        const message = JSON.parse(event.data);
        if (!challenged) {
          if (message.version !== '1' || message.type !== 'challenge' || message.role !== 'control' || message.subjectId !== client.clientId) throw new Error('invalid challenge');
          const input = sessionSigningInput(message);
          const signature = await crypto.subtle.sign({ name: 'Ed25519' }, client.privateKey, input);
          socket.send(JSON.stringify({ version: '1', type: 'authenticate', signature: base64url(signature) }));
          challenged = true;
          return;
        }
        if (message.version !== '1' || message.type !== 'authenticated') throw new Error('authentication failed');
        authenticated = true;
        realtimeAuthFailures = 0;
        realtimeAttempt = 0;
        if (!settled) {
          settled = true;
          firstConnection.resolve();
        }
        setStatus('HTTPS/WSS 已安全连接', 'done');
      } catch {
        socket.close();
      }
    };
  };
  attempt();
  return firstConnected;
};

const poll = async (pairing, client) => {
  for (let attempt = 0; attempt < 150; attempt++) {
    await new Promise(resolve => setTimeout(resolve, 2000));
    const response = await fetch(`/v1/control-client-pairings/${encodeURIComponent(pairing.pairingId)}/status`, {
      headers: { Authorization: `Bearer ${pairing.secret}` }, cache: 'no-store'
    });
    if (!response.ok) throw new Error('status unavailable');
    const value = await response.json();
    if (value.status === 'approved') {
      const identity = { ...client, ownerId: value.ownerId };
      await storeKey('active', identity);
      await storeNodeBinding({
        ownerId: value.ownerId,
        nodeId: value.nodeId,
        name: value.nodeName || nameFromClient(client),
        version: value.version,
        status: 'paired',
        pairedAt: new Date().toISOString(),
        online: true,
      });
      await deleteKey(`pending:${pairing.pairingId}`);
      location.hash = '';
      setStatus('正在建立安全实时连接', 'waiting');
      await connectRealtime(identity);
      return;
    }
    if (value.status === 'declined' || value.status === 'expired') {
      await deleteKey(`pending:${pairing.pairingId}`);
      throw new Error(value.status);
    }
  }
  throw new Error('expired');
};

button.addEventListener('click', async () => {
  const pairing = parseSecret();
  if (!pairing) {
    setStatus('请从办公室电脑重新生成配对链接', 'error');
    return;
  }
  const name = document.querySelector('#name').value.trim();
  if (!name) {
    setStatus('请输入设备名称', 'error');
    return;
  }
  button.disabled = true;
  let claimed = false;
  try {
    if (!crypto.subtle || !indexedDB) throw new Error('unsupported');
    const existing = await getKey('active');
    const client = existing?.clientId && existing?.keyId && existing?.publicKey && existing?.privateKey
      ? stripLegacyNodeFields(existing, name)
      : await createClientIdentity(name);
    await storeKey(`pending:${pairing.pairingId}`, client);
    const response = await fetch(`/v1/control-client-pairings/${encodeURIComponent(pairing.pairingId)}/claim`, {
      method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${pairing.secret}` },
      body: JSON.stringify({ clientId: client.clientId, keyId: client.keyId, name, publicKey })
    });
    if (!response.ok) throw new Error('claim failed');
    claimed = true;
    const value = await response.json();
    fingerprint.textContent = value.fingerprint;
    details.hidden = false;
    setStatus('等待办公室电脑确认此指纹', 'waiting');
    await poll(pairing, client);
  } catch (error) {
    if (!claimed) await deleteKey(`pending:${pairing.pairingId}`).catch(() => {});
    const text = error instanceof Error && error.message === 'declined' ? '办公室电脑拒绝了请求' : '配对未完成，请重新生成链接';
    setStatus(text, 'error');
    button.disabled = false;
  }
});

if (parseSecret()) {
  setStatus('配对链接已就绪');
} else {
  getKey('active').then(active => {
    if (active) {
      button.disabled = true;
      setStatus('正在恢复安全实时连接', 'waiting');
      return connectRealtime(stripLegacyNodeFields(active, nameFromClient(active)));
    }
  }).catch(() => setStatus('请从办公室电脑生成配对链接', 'error'));
}

async function createClientIdentity(name) {
  const keys = await crypto.subtle.generateKey({ name: 'Ed25519' }, false, ['sign', 'verify']);
  const publicKey = base64url(await crypto.subtle.exportKey('raw', keys.publicKey));
  return { clientId: randomID('cli'), keyId: randomID('key'), name, publicKey, privateKey: keys.privateKey };
}

function nameFromClient(client) {
  return typeof client.name === 'string' && client.name ? client.name : 'Yuanshu 控制端';
}

function stripLegacyNodeFields(identity, name) {
  const { nodeId: _nodeId, nodePublicKey: _nodePublicKey, proof: _proof, ...ownerIdentity } = identity;
  return { ...ownerIdentity, name };
}
