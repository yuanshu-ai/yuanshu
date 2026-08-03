import { describe, expect, it } from "vitest";
import canonicalize from "canonicalize";

import { MemoryControlStorage } from "../relay/storage";
import { AdminClient } from "./admin-client";

describe("AdminClient", () => {
  it("requires a paired browser identity", async () => {
    const client = new AdminClient(new MemoryControlStorage(), async () => new Response(null, { status: 401 }));
    await expect(client.authenticate()).rejects.toThrow("尚未配对");
  });

  it("signs the dedicated admin challenge with the non-exportable paired key", async () => {
    const storage = new MemoryControlStorage();
    const keyPair = await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"]);
    await storage.putActiveIdentity({ ownerId: "owner-a", clientId: "client-a", keyId: "key-a", privateKey: keyPair.privateKey });
    const challenge = { version: "1", type: "admin.challenge", challengeId: "adm_test", clientId: "client-a", keyId: "key-a", origin: "https://server.test", nonce: "nonce", expiresAt: "2026-08-03T12:00:00Z" };
    let verified = false;
    const fetcher = async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      if (path === "/v1/admin/auth/session" && (!init?.method || init.method === "GET")) return new Response(null, { status: 401 });
      if (path === "/v1/admin/auth/challenge") return Response.json(challenge, { status: 201 });
      if (path === "/v1/admin/auth/session" && init?.method === "POST") {
        const body = JSON.parse(String(init.body)) as { challengeId: string; signature: string };
        const canonical = canonicalize(challenge);
        const inputBytes = new TextEncoder().encode(`yuanshu-admin-session-v1\0${canonical}`);
        verified = await crypto.subtle.verify("Ed25519", keyPair.publicKey, asArrayBuffer(decodeBase64URL(body.signature)), asArrayBuffer(inputBytes));
        expect(body.challengeId).toBe(challenge.challengeId);
        return Response.json({ csrfToken: "csrf-test" }, { status: 201 });
      }
      return new Response(null, { status: 404 });
    };
    const client = new AdminClient(storage, fetcher as typeof fetch);
    await client.authenticate();
    expect(verified).toBe(true);
  });
});

function decodeBase64URL(value: string): Uint8Array {
  const padded = value.replaceAll("-", "+").replaceAll("_", "/").padEnd(Math.ceil(value.length / 4) * 4, "=");
  return Uint8Array.from(atob(padded), (character) => character.charCodeAt(0));
}

function asArrayBuffer(value: Uint8Array): ArrayBuffer {
  return value.slice().buffer as ArrayBuffer;
}
