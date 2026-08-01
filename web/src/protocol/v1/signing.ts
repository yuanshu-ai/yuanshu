import canonicalize from "canonicalize";
import { CURRENT_VERSION, KNOWN_CONTROL_TYPES } from "./catalog.generated";
import type { YuanshuMessage } from "./types.generated";

export const CONTROL_SIGNING_DOMAIN = "yuanshu-control-v1\0";
export const OPERATION_DIGEST_DOMAIN = "yuanshu-operation-v1\0";

const encoder = new TextEncoder();
const knownControls = new Set<string>(KNOWN_CONTROL_TYPES);

export function controlSigningInput(message: YuanshuMessage): Uint8Array {
  validateControlForEncoding(message);
  assertIJson(message);
  const { signature: _signature, ...unsigned } = message;
  return domainSeparated(CONTROL_SIGNING_DOMAIN, canonicalJson(unsigned));
}

export async function approvalOperationDigest(message: YuanshuMessage): Promise<string> {
  validateApprovalForDigest(message);
  assertIJson(message);
  const { operationDigest: _digest, ...payload } = message.payload;
  const binding: Record<string, unknown> = {
    protocolVersion: message.protocolVersion,
    type: message.type,
    ownerId: message.ownerId,
    nodeId: message.nodeId,
    payload,
  };
  for (const field of ["workspaceId", "threadId", "turnId", "itemId"] as const) {
    if (message[field] !== undefined) binding[field] = message[field];
  }
  const input = domainSeparated(OPERATION_DIGEST_DOMAIN, canonicalJson(binding));
  const digest = await crypto.subtle.digest("SHA-256", input.slice().buffer as ArrayBuffer);
  return bytesToBase64Url(new Uint8Array(digest));
}

function validateControlForEncoding(message: YuanshuMessage): void {
  if (message.protocolVersion !== CURRENT_VERSION) throw new Error(`unsupported control protocol version ${message.protocolVersion}`);
  if (!knownControls.has(message.type)) throw new Error(`unsupported control type ${message.type}`);
  for (const field of ["messageId", "sentAt", "expiresAt", "ownerId", "nodeId", "streamId", "correlationId", "nonce"] as const) {
    if (typeof message[field] !== "string" || message[field].length === 0) throw new Error(`control message is missing ${field}`);
  }
  if (!message.signer || !message.signer.clientId || !message.signer.keyId) throw new Error("control message is missing signer");
  if (!Number.isSafeInteger(message.sequence) || message.sequence < 0) throw new Error("control sequence is outside the JavaScript safe-integer range");
  if (!isPlainObject(message.payload)) throw new Error("control payload must be an object");
}

function validateApprovalForDigest(message: YuanshuMessage): void {
  if (message.protocolVersion !== CURRENT_VERSION || message.type !== "approval.requested") {
    throw new Error("operation digest requires a Protocol v1 approval.requested event");
  }
  if (!message.ownerId || !message.nodeId || !isPlainObject(message.payload)) throw new Error("approval event is missing a required binding field");
  if (typeof message.payload.approvalId !== "string" || message.payload.approvalId.length === 0 || typeof message.payload.kind !== "string" || message.payload.kind.length === 0) {
    throw new Error("approval payload requires non-empty approvalId and kind");
  }
}

function canonicalJson(value: unknown): string {
  const result = canonicalize(value);
  if (result === undefined) throw new Error("value cannot be represented as canonical JSON");
  return result;
}

function domainSeparated(domain: string, canonical: string): Uint8Array {
  const prefix = encoder.encode(domain);
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
  if (typeof value !== "object") throw new Error(`unsupported I-JSON value type ${typeof value}`);
  if (seen.has(value)) throw new Error("cyclic value");
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
    } else if (unit >= 0xdc00 && unit <= 0xdfff) {
      return false;
    }
  }
  return true;
}

function bytesToBase64Url(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}
