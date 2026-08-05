import canonicalize from "canonicalize";

import { CURRENT_VERSION, KNOWN_CONTROL_TYPES } from "./catalog.generated";
import type { YuanshuMessage } from "./types.generated";

export const CONTROL_SIGNING_DOMAIN = "yuanshu-control-v1.1\0";

const encoder = new TextEncoder();
const knownControls = new Set<string>(KNOWN_CONTROL_TYPES);

export function controlSigningInput(message: YuanshuMessage): Uint8Array {
  if (message.protocolVersion !== CURRENT_VERSION || !knownControls.has(message.type)) throw new Error("unsupported Protocol 1.1 control");
  for (const field of ["messageId", "sentAt", "expiresAt", "ownerId", "nodeId", "streamId", "correlationId", "nonce"] as const) {
    if (typeof message[field] !== "string" || message[field].length === 0) throw new Error(`control message is missing ${field}`);
  }
  if (!message.signer?.clientId || !message.signer.keyId || !Number.isSafeInteger(message.sequence) || message.sequence < 0 || !isPlainObject(message.payload)) {
    throw new Error("control message is invalid");
  }
  assertIJson(message);
  const { signature: _signature, ...unsigned } = message;
  const canonical = canonicalize(unsigned);
  if (canonical === undefined) throw new Error("control message cannot be canonicalized");
  const prefix = encoder.encode(CONTROL_SIGNING_DOMAIN);
  const body = encoder.encode(canonical);
  const result = new Uint8Array(prefix.length + body.length);
  result.set(prefix);
  result.set(body, prefix.length);
  return result;
}

function assertIJson(value: unknown, seen = new Set<object>()): void {
  if (value === null || typeof value === "boolean") return;
  if (typeof value === "string") {
    if (!isWellFormedUnicode(value)) throw new Error("I-JSON string contains an unpaired surrogate");
    return;
  }
  if (typeof value === "number") {
    if (!Number.isFinite(value)) throw new Error("I-JSON number must be finite");
    return;
  }
  if (typeof value !== "object" || seen.has(value)) throw new Error("control message is not I-JSON");
  seen.add(value);
  try {
    if (Array.isArray(value)) {
      for (const item of value) assertIJson(item, seen);
      return;
    }
    if (!isPlainObject(value)) throw new Error("I-JSON objects must use a plain object prototype");
    for (const [key, item] of Object.entries(value)) {
      if (!isWellFormedUnicode(key)) throw new Error("I-JSON object key contains an unpaired surrogate");
      assertIJson(item, seen);
    }
  } finally {
    seen.delete(value);
  }
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function isWellFormedUnicode(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const unit = value.charCodeAt(index);
    if (unit >= 0xd800 && unit <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false;
      index += 1;
    } else if (unit >= 0xdc00 && unit <= 0xdfff) return false;
  }
  return true;
}
