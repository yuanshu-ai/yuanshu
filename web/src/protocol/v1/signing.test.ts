// @vitest-environment node

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import canonicalize from "canonicalize";
import { describe, expect, it } from "vitest";
import type { YuanshuMessage } from "./types.generated";
import {
  CONTROL_SIGNING_DOMAIN,
  OPERATION_DIGEST_DOMAIN,
  approvalOperationDigest,
  controlSigningInput,
} from "./signing";

interface SigningFixture {
  controlDomain: string;
  operationDomain: string;
  jcsCases: Array<{ name: string; inputJson: string; canonicalJson: string }>;
  control: {
    testOnlySeedHex: string;
    publicKeyBase64Url: string;
    message: YuanshuMessage;
    canonicalWithoutSignature: string;
    signingInputBase64Url: string;
    signatureBase64Url: string;
  };
  approval: {
    message: YuanshuMessage;
    canonicalBinding: string;
    operationDigest: string;
  };
}

const fixture = JSON.parse(
  readFileSync(resolve(process.cwd(), "..", "schemas", "yuanshu", "v1", "fixtures", "signing-vectors.json"), "utf8"),
) as SigningFixture;

describe("Protocol v1 signing encoding", () => {
  it("matches the shared RFC 8785 canonicalization vectors", () => {
    for (const testCase of fixture.jcsCases) {
      expect(canonicalize(JSON.parse(testCase.inputJson)), testCase.name).toBe(testCase.canonicalJson);
    }
  });

  it("produces the fixed control signing input and Ed25519 signature", async () => {
    expect(CONTROL_SIGNING_DOMAIN).toBe(fixture.controlDomain);
    expect(OPERATION_DIGEST_DOMAIN).toBe(fixture.operationDomain);
    const before = JSON.stringify(fixture.control.message);
    const input = controlSigningInput(fixture.control.message);
    expect(toBase64Url(input)).toBe(fixture.control.signingInputBase64Url);
    expect(new TextDecoder().decode(input.slice(new TextEncoder().encode(CONTROL_SIGNING_DOMAIN).length))).toBe(
      fixture.control.canonicalWithoutSignature,
    );
    expect(JSON.stringify(fixture.control.message)).toBe(before);

    const seed = fromHex(fixture.control.testOnlySeedHex);
    const pkcs8 = concat(fromHex("302e020100300506032b657004220420"), seed);
    const privateKey = await crypto.subtle.importKey("pkcs8", asArrayBuffer(pkcs8), { name: "Ed25519" }, false, ["sign"]);
    const signature = new Uint8Array(await crypto.subtle.sign("Ed25519", privateKey, asArrayBuffer(input)));
    expect(toBase64Url(signature)).toBe(fixture.control.signatureBase64Url);
    expect(fixture.control.message.signature).toBe(fixture.control.signatureBase64Url);

    const publicKey = await crypto.subtle.importKey(
      "raw",
      asArrayBuffer(fromBase64Url(fixture.control.publicKeyBase64Url)),
      { name: "Ed25519" },
      false,
      ["verify"],
    );
    await expect(crypto.subtle.verify("Ed25519", publicKey, asArrayBuffer(signature), asArrayBuffer(input))).resolves.toBe(true);
  });

  it("binds every control envelope group and nested payload but excludes signature", async () => {
    const baseline = controlSigningInput(fixture.control.message);
    const publicKey = await crypto.subtle.importKey(
      "raw",
      asArrayBuffer(fromBase64Url(fixture.control.publicKeyBase64Url)),
      { name: "Ed25519" },
      false,
      ["verify"],
    );
    const fixedSignature = fromBase64Url(fixture.control.signatureBase64Url);
    const mutations: Array<[string, (message: Record<string, any>) => void]> = [
      ["messageId", (message) => { message.messageId += "_changed"; }],
      ["type", (message) => { message.type = "turn.steer"; }],
      ["sentAt", (message) => { message.sentAt = "2026-08-01T06:00:01Z"; }],
      ["expiresAt", (message) => { message.expiresAt = "2026-08-01T06:02:00Z"; }],
      ["ownerId", (message) => { message.ownerId += "_changed"; }],
      ["nodeId", (message) => { message.nodeId += "_changed"; }],
      ["workspaceId", (message) => { message.workspaceId = "workspace_2"; }],
      ["threadId", (message) => { message.threadId = "thread_2"; }],
      ["turnId", (message) => { message.turnId = "turn_2"; }],
      ["itemId", (message) => { message.itemId = "item_2"; }],
      ["streamId", (message) => { message.streamId += "_changed"; }],
      ["sequence", (message) => { message.sequence -= 1; }],
      ["correlationId", (message) => { message.correlationId += "_changed"; }],
      ["nonce", (message) => { message.nonce = "different_nonce_1234"; }],
      ["signer", (message) => { message.signer.keyId += "_changed"; }],
      ["payload", (message) => { message.payload.input = "changed"; }],
    ];
    for (const [name, mutate] of mutations) {
      const message = structuredClone(fixture.control.message) as unknown as Record<string, any>;
      mutate(message);
      const changed = controlSigningInput(message as unknown as YuanshuMessage);
      expect(changed, name).not.toEqual(baseline);
      await expect(
        crypto.subtle.verify("Ed25519", publicKey, asArrayBuffer(fixedSignature), asArrayBuffer(changed)),
        name,
      ).resolves.toBe(false);
    }

    const signatureOnly = structuredClone(fixture.control.message) as unknown as Record<string, any>;
    signatureOnly.signature = "B".repeat(86);
    expect(controlSigningInput(signatureOnly as unknown as YuanshuMessage)).toEqual(baseline);
  });

  it("produces a replay-stable operation digest bound to targets and the full payload", async () => {
    const before = JSON.stringify(fixture.approval.message);
    const digest = await approvalOperationDigest(fixture.approval.message);
    expect(digest).toBe(fixture.approval.operationDigest);
    expect(JSON.stringify(fixture.approval.message)).toBe(before);

    const payload = { ...fixture.approval.message.payload };
    delete payload.operationDigest;
    const binding: Record<string, unknown> = {
      protocolVersion: fixture.approval.message.protocolVersion,
      type: fixture.approval.message.type,
      ownerId: fixture.approval.message.ownerId,
      nodeId: fixture.approval.message.nodeId,
      workspaceId: fixture.approval.message.workspaceId,
      threadId: fixture.approval.message.threadId,
      turnId: fixture.approval.message.turnId,
      itemId: fixture.approval.message.itemId,
      payload,
    };
    expect(canonicalize(binding)).toBe(fixture.approval.canonicalBinding);

    const replayMutations: Array<(message: Record<string, any>) => void> = [
      (message) => { message.messageId = "replayed_message"; },
      (message) => { message.sentAt = "2026-08-01T07:00:00Z"; },
      (message) => { message.streamId = "replay_stream"; },
      (message) => { message.sequence = 999; },
      (message) => { message.correlationId = "replay_request"; },
      (message) => { message.payload.operationDigest = "B".repeat(43); },
    ];
    for (const mutate of replayMutations) {
      const message = structuredClone(fixture.approval.message) as unknown as Record<string, any>;
      mutate(message);
      await expect(approvalOperationDigest(message as unknown as YuanshuMessage)).resolves.toBe(digest);
    }

    const bindingMutations: Array<(message: Record<string, any>) => void> = [
      (message) => { message.nodeId = "node_2"; },
      (message) => { message.workspaceId = "workspace_2"; },
      (message) => { message.payload.approvalId = "approval_2"; },
      (message) => { message.payload.summary = "Changed summary"; },
      (message) => { message.payload.futureField = true; },
    ];
    for (const mutate of bindingMutations) {
      const message = structuredClone(fixture.approval.message) as unknown as Record<string, any>;
      mutate(message);
      await expect(approvalOperationDigest(message as unknown as YuanshuMessage)).resolves.not.toBe(digest);
    }
  });

  it("rejects unsupported and non-I-JSON inputs", async () => {
    const wrongVersion = structuredClone(fixture.control.message) as unknown as Record<string, any>;
    wrongVersion.protocolVersion = "1.1";
    expect(() => controlSigningInput(wrongVersion as unknown as YuanshuMessage)).toThrow(/unsupported/);

    const nonFinite = structuredClone(fixture.control.message) as unknown as Record<string, any>;
    nonFinite.payload.input = Number.POSITIVE_INFINITY;
    expect(() => controlSigningInput(nonFinite as unknown as YuanshuMessage)).toThrow(/finite/);

    const badUnicode = structuredClone(fixture.control.message) as unknown as Record<string, any>;
    badUnicode.ownerId = "\ud800";
    expect(() => controlSigningInput(badUnicode as unknown as YuanshuMessage)).toThrow(/surrogate/);

    await expect(approvalOperationDigest(fixture.control.message)).rejects.toThrow(/approval.requested/);
  });
});

function fromHex(value: string): Uint8Array {
  return Uint8Array.from(value.match(/../g) ?? [], (byte) => Number.parseInt(byte, 16));
}

function fromBase64Url(value: string): Uint8Array {
  return new Uint8Array(Buffer.from(value, "base64url"));
}

function toBase64Url(value: Uint8Array): string {
  return Buffer.from(value).toString("base64url");
}

function concat(first: Uint8Array, second: Uint8Array): Uint8Array {
  const result = new Uint8Array(first.length + second.length);
  result.set(first);
  result.set(second, first.length);
  return result;
}

function asArrayBuffer(value: Uint8Array): ArrayBuffer {
  return value.slice().buffer as ArrayBuffer;
}
