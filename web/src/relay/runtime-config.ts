import type { ControlStorage, StoredRuntimeSettings } from "./storage";

export interface RuntimeSettings extends StoredRuntimeSettings {}

const DEFAULT_RUNTIME_CONFIG_PATH = "/yuanshu.config.json";

export function validateRelayURL(value: string): string | undefined {
  return validateURL(value, "wss:", "Relay URL");
}

export function validatePairingURL(value: string): string | undefined {
	if (value.startsWith("/") && !value.startsWith("//") && !value.includes("?") && !value.includes("#")) return undefined;
	return validateURL(value, "https:", "Pairing URL");
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
  };
}

export async function loadRuntimeSettings(storage: ControlStorage, fetcher?: typeof fetch): Promise<RuntimeSettings> {
  const stored = await storage.getRuntimeSettings().catch(() => undefined);
  if (stored && !validateRelayURL(stored.relayUrl) && !validatePairingURL(stored.pairingUrl)) return stored;

	const runtime = await loadRuntimeFile(fetcher ?? (typeof globalThis.fetch === "function" ? globalThis.fetch.bind(globalThis) : undefined));
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

function validateURL(value: string, scheme: string, label: string): string | undefined {
  if (!value) return `${label}不能为空`;
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    return `${label}格式无效`;
  }
  if (parsed.protocol !== scheme) return `${label}必须使用 ${scheme}//`;
  if (!parsed.hostname || parsed.username || parsed.password || parsed.search || parsed.hash) return `${label}不能包含凭据、查询参数或片段`;
  if (parsed.port && (!/^\d+$/.test(parsed.port) || Number(parsed.port) < 1 || Number(parsed.port) > 65535)) return `${label}端口无效`;
  return undefined;
}
