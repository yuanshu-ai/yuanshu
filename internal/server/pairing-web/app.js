import { base64url, sessionSigningInput } from '/pair/session.js';

const button = document.querySelector('#pair');
const statusText = document.querySelector('#status');
const result = document.querySelector('#result');
const details = document.querySelector('#details');
const fingerprint = document.querySelector('#fingerprint');
let activeSocket;

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
  const request = indexedDB.open('yuanshu-control-client', 1);
  request.onupgradeneeded = () => request.result.createObjectStore('keys');
  request.onsuccess = () => resolve(request.result);
  request.onerror = () => reject(new Error('storage unavailable'));
});
const withStore = async (mode, operation) => {
  const db = await openDB();
  try {
    return await new Promise((resolve, reject) => {
      const tx = db.transaction('keys', mode);
      const request = operation(tx.objectStore('keys'));
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(new Error('storage unavailable'));
    });
  } finally {
    db.close();
  }
};
const storeKey = (id, value) => withStore('readwrite', store => store.put(value, id));
const getKey = id => withStore('readonly', store => store.get(id));
const deleteKey = id => withStore('readwrite', store => store.delete(id));

const connectRealtime = client => new Promise((resolve, reject) => {
  if (!client?.clientId || !client?.privateKey) return reject(new Error('identity unavailable'));
  if (activeSocket) activeSocket.close();
  const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const socket = new WebSocket(`${scheme}//${location.host}/web/connect?clientId=${encodeURIComponent(client.clientId)}`, 'yuanshu-relay-v1');
  activeSocket = socket;
  let challenged = false;
  const fail = () => reject(new Error('realtime unavailable'));
  socket.onerror = fail;
  socket.onclose = event => {
    if (!challenged || event.code !== 1000) fail();
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
      setStatus('HTTPS/WSS 已安全连接', 'done');
      resolve();
    } catch {
      socket.close();
      fail();
    }
  };
});

const poll = async (pairing, client) => {
  for (let attempt = 0; attempt < 150; attempt++) {
    await new Promise(resolve => setTimeout(resolve, 2000));
    const response = await fetch(`/v1/control-client-pairings/${encodeURIComponent(pairing.pairingId)}/status`, {
      headers: { Authorization: `Bearer ${pairing.secret}` }, cache: 'no-store'
    });
    if (!response.ok) throw new Error('status unavailable');
    const value = await response.json();
    if (value.status === 'approved') {
      const active = { ...client, ownerId: value.ownerId, nodeId: value.nodeId, nodePublicKey: value.nodePublicKey, proof: value.proof };
      await storeKey('active', active);
      await deleteKey(`pending:${pairing.pairingId}`);
      location.hash = '';
      setStatus('正在建立安全实时连接', 'waiting');
      await connectRealtime(active);
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
    const keys = await crypto.subtle.generateKey({ name: 'Ed25519' }, false, ['sign', 'verify']);
    const publicKey = base64url(await crypto.subtle.exportKey('raw', keys.publicKey));
    const client = { clientId: randomID('cli'), keyId: randomID('key'), name, publicKey, privateKey: keys.privateKey };
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
      return connectRealtime(active);
    }
  }).catch(() => setStatus('请从办公室电脑生成配对链接', 'error'));
}
