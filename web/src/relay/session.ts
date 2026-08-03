import canonicalize from "canonicalize";

export const RELAY_SUBPROTOCOL = "yuanshu-relay-v1";
export const SESSION_SIGNING_DOMAIN = "yuanshu-relay-session-v1\0";

export interface RelayChallenge {
  version: string;
  type: string;
  role: "node" | "control";
  connectionId: string;
  subjectId: string;
  nonce: string;
  expiresAt: string;
}

export function sessionSigningInput(challenge: RelayChallenge): Uint8Array {
  if (challenge.version !== "1" || challenge.type !== "challenge" || challenge.role !== "control" || !challenge.connectionId || !challenge.subjectId || !challenge.nonce || !challenge.expiresAt) {
    throw new Error("relay challenge is invalid");
  }
  const canonical = canonicalize(challenge);
  if (canonical === undefined) throw new Error("relay challenge is invalid");
  const prefix = new TextEncoder().encode(SESSION_SIGNING_DOMAIN);
  const body = new TextEncoder().encode(canonical);
  const result = new Uint8Array(prefix.length + body.length);
  result.set(prefix);
  result.set(body, prefix.length);
  return result;
}
