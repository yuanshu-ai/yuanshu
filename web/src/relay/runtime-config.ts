import type { ControlStorage, StoredRuntimeSettings } from "./storage";

export interface RuntimeSettings extends StoredRuntimeSettings {
  adminEnabled?: boolean;
  adminUrl?: string;
}

const DEFAULT_RUNTIME_CONFIG_PATH = "/yuanshu.config.json";

export function validateRelayURL(value: string): string | undefined {
  return validateSecureOrLoopbackURL(value, "wss:", "ws:", "Relay URL");
}

export function validatePairingURL(value: string): string | undefined {
	if (value.startsWith("/") && !value.startsWith("//") && !value.includes("?") && !value.includes("#")) return undefined;
	return validateSecureOrLoopbackURL(value, "https:", "http:", "Pairing URL");
}

export function normalizeRuntimeSettings(value: Partial<RuntimeSettings>): RuntimeSettings {
  const relayUrl = value.relayUrl?.trim() ?? "";
  const pairingUrl = value.pairingUrl?.trim() ?? "";
  const relayError = validateRelayURL(relayUrl);
  const pairingError = validatePairingURL(pairingUrl);
  if (relayError || pairingError) throw new Error(relayError ?? pairingError ?? "连接地址无效");
  return {
    relayUrl,
    pairingUrl,
    ...(value.displayName?.trim() ? { displayName: value.displayName.trim() } : {}),
    ...(typeof value.adminEnabled === "boolean" ? { adminEnabled: value.adminEnabled } : {}),
    ...(value.adminUrl?.startsWith("/") && !value.adminUrl.startsWith("//") ? { adminUrl: value.adminUrl } : {}),
  };
}

export async function loadRuntimeSettings(storage: ControlStorage, fetcher?: typeof fetch): Promise<RuntimeSettings> {
  const stored = await storage.getRuntimeSettings().catch(() => undefined);
	const runtime = await loadRuntimeFile(fetcher ?? (typeof globalThis.fetch === "function" ? globalThis.fetch.bind(globalThis) : undefined));
  if (stored && !validateRelayURL(stored.relayUrl) && !validatePairingURL(stored.pairingUrl)) return { ...stored, adminEnabled: runtime?.adminEnabled, adminUrl: runtime?.adminUrl };
  if (runtime) return runtime;

  const relayUrl = import.meta.env.VITE_YUANSHU_RELAY_URL?.trim() ?? "";
  const pairingUrl = import.meta.env.VITE_YUANSHU_PAIRING_URL?.trim() || "/pair";
  return {
    relayUrl: validateRelayURL(relayUrl) ? "" : relayUrl,
    pairingUrl: pairingUrl.startsWith("/") || !validatePairingURL(pairingUrl) ? pairingUrl : "/pair",
  };
}

async function loadRuntimeFile(fetcher: typeof fetch | undefined): Promise<RuntimeSettings | undefined> {
	if (!fetcher) return undefined;
  try {
    const response = await fetcher(DEFAULT_RUNTIME_CONFIG_PATH, { cache: "no-store" });
    if (!response.ok) return undefined;
    const value = await response.json() as Partial<RuntimeSettings>;
    if (typeof value.relayUrl !== "string" || typeof value.pairingUrl !== "string") return undefined;
    return normalizeRuntimeSettings(value);
  } catch {
    return undefined;
  }
}

function validateSecureOrLoopbackURL(value: string, secureScheme: string, loopbackScheme: string, label: string): string | undefined {
  if (!value) return `${label}不能为空`;
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    return `${label}格式无效`;
  }
  if (parsed.protocol !== secureScheme && !(parsed.protocol === loopbackScheme && isLiteralLoopback(parsed.hostname))) {
    return `${label}必须使用 ${secureScheme}//；仅本机 127.0.0.1/::1 可使用 ${loopbackScheme}//`;
  }
  if (!parsed.hostname || parsed.username || parsed.password || parsed.search || parsed.hash) return `${label}不能包含凭据、查询参数或片段`;
  if (parsed.port && (!/^\d+$/.test(parsed.port) || Number(parsed.port) < 1 || Number(parsed.port) > 65535)) return `${label}端口无效`;
  return undefined;
}

function isLiteralLoopback(hostname: string): boolean {
  return hostname === "127.0.0.1" || hostname === "[::1]" || hostname === "::1";
}
