export const SESSION_SIGNING_DOMAIN = 'yuanshu-relay-session-v1\0';

export const base64url = bytes => {
  let binary = '';
  for (const byte of new Uint8Array(bytes)) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '');
};

export const canonicalChallenge = challenge => {
  const keys = ['connectionId', 'expiresAt', 'nonce', 'role', 'subjectId', 'type', 'version'];
  const value = {};
  for (const key of keys) {
    if (typeof challenge[key] !== 'string') throw new Error('invalid challenge');
    value[key] = challenge[key];
  }
  if (Object.keys(challenge).length !== keys.length) throw new Error('invalid challenge');
  return JSON.stringify(value);
};

export const sessionSigningInput = challenge => {
  const encoder = new TextEncoder();
  const domain = encoder.encode(SESSION_SIGNING_DOMAIN);
  const canonical = encoder.encode(canonicalChallenge(challenge));
  const input = new Uint8Array(domain.length + canonical.length);
  input.set(domain);
  input.set(canonical, domain.length);
  return input;
};
