import { describe, expect, it } from "vitest";

import { loadRuntimeSettings, normalizeRuntimeSettings, validatePairingURL, validateRelayURL } from "./runtime-config";
import { MemoryControlStorage } from "./storage";

describe("runtime connection settings", () => {
  it("accepts IP, IPv6, and relative same-origin pairing URLs but rejects plaintext relay", () => {
    expect(validateRelayURL("wss://192.168.1.20:9527/web/connect")).toBeUndefined();
    expect(validateRelayURL("wss://[fd00::20]:9527/web/connect")).toBeUndefined();
    expect(validatePairingURL("https://192.168.1.20:9527/pair")).toBeUndefined();
    expect(validateRelayURL("ws://127.0.0.1:9527/web/connect")).toBeUndefined();
    expect(validatePairingURL("http://[::1]:9527/pair")).toBeUndefined();
    expect(validatePairingURL("/pair")).toBeUndefined();
    expect(validateRelayURL("ws://192.168.1.20:9527/web/connect")).toContain("wss://");
  });

  it("prefers persisted settings over runtime deployment settings", async () => {
    const storage = new MemoryControlStorage();
    await storage.putRuntimeSettings({ relayUrl: "wss://stored.example.test/web/connect", pairingUrl: "/pair" });
    const settings = await loadRuntimeSettings(storage, async () => new Response(JSON.stringify({ relayUrl: "wss://runtime.example.test/web/connect", pairingUrl: "https://runtime.example.test/pair", adminEnabled: true, adminUrl: "/admin" })));
    expect(settings.relayUrl).toBe("wss://stored.example.test/web/connect");
    expect(settings.pairingUrl).toBe("/pair");
    expect(settings.adminEnabled).toBe(true);
  });

  it("loads deployment JSON when no browser override exists", async () => {
    const settings = await loadRuntimeSettings(new MemoryControlStorage(), async () => new Response(JSON.stringify({ relayUrl: "wss://192.168.1.20:9527/web/connect", pairingUrl: "https://192.168.1.20:9527/pair", adminEnabled: true, adminUrl: "/admin" })));
    expect(settings).toEqual({ relayUrl: "wss://192.168.1.20:9527/web/connect", pairingUrl: "https://192.168.1.20:9527/pair", adminEnabled: true, adminUrl: "/admin" });
  });

  it("does not accept credentials or query strings in settings", () => {
    expect(() => normalizeRuntimeSettings({ relayUrl: "wss://user:secret@example.test/connect", pairingUrl: "https://example.test/pair" })).toThrow();
    expect(() => normalizeRuntimeSettings({ relayUrl: "wss://example.test/connect?token=secret", pairingUrl: "https://example.test/pair" })).toThrow();
  });
});
