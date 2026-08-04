import { base64url, sessionSigningInput } from '/pair/session.js';
import { catalogs } from '/pair/catalog.generated.js';
import { CONTROL_STORES, controlStorageKey, withControlStore } from '/pair/storage.js';

const button = document.querySelector('#pair');
const statusText = document.querySelector('#status');
const result = document.querySelector('#result');
const details = document.querySelector('#details');
const fingerprint = document.querySelector('#fingerprint');
const continueLink = document.querySelector('#continue');
let activeSocket;
let realtimeTimer;
let realtimeAttempt = 0;
let realtimeAuthFailures = 0;
let realtimeGeneration = 0;
let locale = 'zh-CN';
const text = key => catalogs[locale]?.[key] ?? catalogs['zh-CN'][key] ?? key;

const setLocale = async value => {
  locale = value === 'en-US' ? 'en-US' : 'zh-CN';
  document.documentElement.lang = locale;
  document.querySelectorAll('[data-i18n]').forEach(element => { element.textContent = text(element.dataset.i18n); });
  document.querySelectorAll('[data-locale]').forEach(element => element.classList.toggle('active', element.dataset.locale === locale));
  await withControlStore(CONTROL_STORES.preferences, 'readwrite', store => store.put(locale, 'language')).catch(() => {});
};
document.querySelectorAll('[data-locale]').forEach(button => button.addEventListener('click', () => void setLocale(button.dataset.locale)));
withControlStore(CONTROL_STORES.preferences, 'readonly', store => store.get('language')).then(value => setLocale(value)).catch(() => setLocale('zh-CN'));

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
const stopRealtime = () => {
  realtimeGeneration += 1;
  if (realtimeTimer) clearTimeout(realtimeTimer);
  realtimeTimer = undefined;
  const socket = activeSocket;
  activeSocket = undefined;
  socket?.close();
};
const storeKey = (id, value) => withControlStore(CONTROL_STORES.keys, 'readwrite', store => store.put(value, id));
const getKey = id => withControlStore(CONTROL_STORES.keys, 'readonly', store => store.get(id));
const deleteKey = id => withControlStore(CONTROL_STORES.keys, 'readwrite', store => store.delete(id));
const storeNodeBinding = binding => withControlStore(CONTROL_STORES.nodes, 'readwrite', store => store.put(binding, controlStorageKey(binding.ownerId, binding.nodeId)));

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
    setStatus(realtimeAuthFailures >= 10 ? text('pair.reauth') : text('pair.reconnecting'), 'waiting');
    const base = Math.min(30000, 500 * 2 ** realtimeAttempt++);
    const jitter = 0.8 + crypto.getRandomValues(new Uint8Array(1))[0] / 255 * 0.4;
    realtimeTimer = setTimeout(() => {
      realtimeTimer = undefined;
      attempt();
    }, Math.max(1, Math.floor(base * jitter)));
  };
  const attempt = () => {
    if (generation !== realtimeGeneration) return;
    setStatus(realtimeAttempt ? text('pair.reconnecting') : text('pair.connecting'), 'waiting');
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
          firstConnection.reject(new Error('reauthentication_required'));
          setStatus(text('pair.reauth'), 'error');
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
        setStatus(text('pair.connected'), 'done');
        continueLink.hidden = false;
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
    if (!response.ok) throw new Error(await responseErrorCode(response, 'status_unavailable'));
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
      history.replaceState(null, '', `${location.pathname}${location.search}`);
      setStatus(text('pair.connecting'), 'waiting');
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
    continueLink.hidden = true;
    const existing = await getKey('active');
    const pending = await getKey(`pending:${pairing.pairingId}`);
    const client = pending?.clientId && pending?.keyId && pending?.publicKey && pending?.privateKey
      ? stripLegacyNodeFields(pending, name)
      : existing?.clientId && existing?.keyId && existing?.publicKey && existing?.privateKey
      ? stripLegacyNodeFields(existing, name)
      : await createClientIdentity(name);
    await storeKey(`pending:${pairing.pairingId}`, client);
    const response = await fetch(`/v1/control-client-pairings/${encodeURIComponent(pairing.pairingId)}/claim`, {
      method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${pairing.secret}` },
      body: JSON.stringify({ clientId: client.clientId, keyId: client.keyId, name, publicKey: client.publicKey })
    });
    if (!response.ok) throw new Error(await responseErrorCode(response, 'claim_failed'));
    claimed = true;
    const value = await response.json();
    fingerprint.textContent = value.fingerprint;
    details.hidden = false;
    setStatus('等待办公室电脑确认此指纹', 'waiting');
    await poll(pairing, client);
  } catch (error) {
    if (!claimed) await deleteKey(`pending:${pairing.pairingId}`).catch(() => {});
    setStatus(pairingErrorMessage(error), 'error');
    button.disabled = false;
  }
});

const initializeFromLocation = () => {
  if (parseSecret()) {
    stopRealtime();
    button.disabled = false;
    continueLink.hidden = true;
    details.hidden = true;
    fingerprint.textContent = '';
    setStatus(text('pair.ready'));
    return;
  }
  getKey('active').then(active => {
    if (active) {
      button.disabled = true;
      setStatus('正在恢复安全实时连接', 'waiting');
      return connectRealtime(stripLegacyNodeFields(active, nameFromClient(active)));
    }
    button.disabled = false;
    setStatus('请从办公室电脑生成配对链接', 'error');
  }).catch(error => setStatus(pairingErrorMessage(error), 'error'));
};

window.addEventListener('hashchange', initializeFromLocation);
initializeFromLocation();

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

async function responseErrorCode(response, fallback) {
  try {
    const value = await response.json();
    return typeof value?.code === 'string' ? value.code : typeof value?.error === 'string' ? value.error : fallback;
  } catch {
    return fallback;
  }
}

function pairingErrorMessage(error) {
  const code = error instanceof Error ? error.message : 'unknown';
  return ({
    declined: '办公室电脑拒绝了请求',
    expired: '配对链接已过期，请从办公室电脑重新生成',
    unauthorized: '配对链接无效或已经失效',
    pairing_disabled: 'Server 当前已关闭新的控制端配对',
    conflict: '配对状态已变化，请从办公室电脑重新生成链接',
    status_unavailable: '暂时无法读取确认状态，请检查网络后重试',
    unsupported: '当前浏览器不支持安全密钥存储',
    reauthentication_required: '控制端身份未通过认证，请重新配对',
  })[code] ?? '配对未完成，请检查网络或重新生成链接';
}
