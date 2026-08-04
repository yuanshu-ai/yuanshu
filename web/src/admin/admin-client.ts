import canonicalize from "canonicalize";

import type { ControlStorage, StoredControlIdentity } from "../relay/storage";

const SESSION_DOMAIN = "yuanshu-admin-session-v1\0";
const ACTION_DOMAIN = "yuanshu-admin-action-v1\0";

export interface AdminOverview {
  status: string;
  uptimeSeconds: number;
  build: { goVersion: string; version?: string; revision?: string; modified?: string };
  database: { schemaVersion: number; quickCheck: string; sizeBytes: number };
  connections: { nodes: number; controlClients: number };
  counts: { activeNodes: number; activeControlClients: number; pendingPairings: number; pendingEnrollments: number; activeLeases: number; unreadNotifications: number; recentFailures: number };
  tls: { configured: boolean; san?: string[]; notAfter?: string; fingerprint?: string; expiryWarning?: string };
  backup: { available: boolean; lastBackupAt?: string; sizeBytes?: number; integrity: "valid" | "invalid" | "unavailable"; operation: "local_cli_only" };
}

export interface AdminNode { id: string; name: string; os: string; version: string; status: string; online: boolean; createdAt: string; lastSeenAt?: string }
export interface AdminNodeDetail { node: AdminNode & { runtime: { nodeId: string; online: boolean; connectedAt?: string; lastFrameAt?: string; lastEventAt?: string; runtimeStatus?: string; relayStatus?: string; recoveryStatus?: string; lastErrorCode?: string; lastCloseReason?: string; workspaceCount: number } } }
export interface AdminControlClient { id: string; name: string; status: string; online: boolean; current: boolean; createdAt: string; lastSeenAt?: string; revokedAt?: string }
export interface AdminAccessRequest { id: string; kind: "control_client" | "node"; nodeId: string; name?: string; os?: string; version?: string; status: string; createdAt: string; expiresAt: string }
export interface AdminLease { scope: { nodeId: string; workspaceId: string; threadId: string }; leaseId: string; holderClientId: string; epoch: number; acquiredAt: string; expiresAt: string; updatedAt: string }
export interface AdminAudit { id: string; actorClientId: string; action: string; resourceType: string; resourceRef: string; result: string; errorCode?: string; correlationId: string; createdAt: string }
export interface AdminConfig {
  listen: string;
  publicUrl: string;
  allowedControlOrigins: string[];
  webEnabled: boolean;
  adminEnabled: boolean;
  dataDirConfigured: boolean;
  configRevision?: string;
  tls: { configured: boolean; san?: string[]; notAfter?: string; fingerprint?: string; expiryWarning?: string };
  admission: { controlPairingEnabled: boolean; nodeEnrollmentEnabled: boolean; revision: number; updatedAt: string };
}

export class AdminClient {
  private csrfToken = "";
  private identity?: StoredControlIdentity;

  constructor(private readonly storage: ControlStorage, private readonly fetcher: typeof fetch = globalThis.fetch.bind(globalThis)) {}

  async authenticate(): Promise<void> {
    this.identity = await this.storage.getActiveIdentity();
    if (!this.identity) throw new Error("当前浏览器尚未配对控制端身份");
    const existing = await this.fetcher("/v1/admin/auth/session", { credentials: "same-origin", cache: "no-store" });
    if (existing.ok) {
      const value = await existing.json() as { csrfToken: string };
      if (value.csrfToken) { this.csrfToken = value.csrfToken; return; }
    }
    const challenge = await this.json<{ version: string; type: string; challengeId: string; clientId: string; keyId: string; origin: string; nonce: string; expiresAt: string }>("/v1/admin/auth/challenge", {
      method: "POST", headers: jsonHeaders(), body: JSON.stringify({ clientId: this.identity.clientId, keyId: this.identity.keyId }), credentials: "same-origin",
    });
    const signature = await sign(this.identity.privateKey, domainSeparated(SESSION_DOMAIN, challenge));
    const session = await this.json<{ csrfToken: string }>("/v1/admin/auth/session", {
      method: "POST", headers: jsonHeaders(), body: JSON.stringify({ challengeId: challenge.challengeId, signature }), credentials: "same-origin",
    });
    if (!session.csrfToken) throw new Error("管理会话缺少 CSRF 保护");
    this.csrfToken = session.csrfToken;
  }

  get<T>(path: string): Promise<T> { return this.json<T>(path, { credentials: "same-origin", cache: "no-store" }); }

  async mutate<T = void>(method: "POST" | "PUT" | "DELETE", path: string, payload: Record<string, unknown>): Promise<T> {
    return this.json<T>(path, { method, headers: this.mutationHeaders(), body: JSON.stringify(payload), credentials: "same-origin" });
  }

  async highRisk<T = void>(method: "POST" | "PUT", path: string, payload: Record<string, unknown>): Promise<T> {
    if (!this.identity) throw new Error("管理身份不可用");
    const bodyDigest = await digest(payload);
    const challenge = await this.json<{ version: string; type: string; challengeId: string; clientId: string; method: string; path: string; bodyDigest: string; nonce: string; expiresAt: string }>("/v1/admin/auth/action-challenge", {
      method: "POST", headers: this.mutationHeaders(), body: JSON.stringify({ method, path, bodyDigest }), credentials: "same-origin",
    });
    const signature = await sign(this.identity.privateKey, domainSeparated(ACTION_DOMAIN, challenge));
    return this.json<T>(path, { method, headers: this.mutationHeaders(), body: JSON.stringify({ request: payload, proof: { challengeId: challenge.challengeId, signature } }), credentials: "same-origin" });
  }

  async close(): Promise<void> {
    if (!this.csrfToken) return;
    await this.fetcher("/v1/admin/auth/session", { method: "DELETE", headers: this.mutationHeaders(), credentials: "same-origin" });
    this.csrfToken = "";
  }

  private mutationHeaders(): Headers {
    const headers = jsonHeaders();
    headers.set("X-Yuanshu-CSRF", this.csrfToken);
    headers.set("Idempotency-Key", crypto.randomUUID());
    headers.set("X-Correlation-ID", `web_${crypto.randomUUID()}`);
    return headers;
  }

  private async json<T>(path: string, init: RequestInit): Promise<T> {
    const response = await this.fetcher(path, init);
    if (!response.ok) {
      let code = `http_${response.status}`;
      try { code = ((await response.json()) as { code?: string }).code ?? code; } catch { /* generic status is sufficient */ }
      throw new Error(adminErrorMessage(code));
    }
    if (response.status === 204) return undefined as T;
    return response.json() as Promise<T>;
  }
}

function jsonHeaders(): Headers { const headers = new Headers(); headers.set("Content-Type", "application/json"); return headers; }

function domainSeparated(domain: string, value: unknown): Uint8Array {
  const canonical = canonicalize(value);
  if (canonical === undefined) throw new Error("管理签名内容无法规范化");
  const prefix = new TextEncoder().encode(domain);
  const body = new TextEncoder().encode(canonical);
  const result = new Uint8Array(prefix.length + body.length);
  result.set(prefix); result.set(body, prefix.length); return result;
}

async function digest(value: unknown): Promise<string> {
  const canonical = canonicalize(value);
  if (canonical === undefined) throw new Error("管理操作内容无法规范化");
  const raw = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(canonical));
  return base64Url(new Uint8Array(raw));
}

async function sign(key: CryptoKey, input: Uint8Array): Promise<string> {
  const raw = await crypto.subtle.sign("Ed25519", key, input.slice().buffer as ArrayBuffer);
  return base64Url(new Uint8Array(raw));
}

function base64Url(value: Uint8Array): string {
  let binary = "";
  for (const byte of value) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}

function adminErrorMessage(code: string): string {
  return ({ admin_auth_required: "管理会话已失效，请重新认证", unauthorized: "控制端身份未获授权", forbidden: "当前来源不允许访问管理后台", action_proof_invalid: "高风险操作签名已失效，请重试", mutation_protection_failed: "管理操作安全校验失败", conflict: "状态已变化，请刷新后重试", pairing_disabled: "控制端配对已关闭", node_enrollment_disabled: "Node 接入已关闭", audit_failure: "操作结果不确定，请刷新状态确认" } as Record<string, string>)[code] ?? `管理请求失败 (${code})`;
}
