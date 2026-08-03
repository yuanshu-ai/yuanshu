export type NodeStatus = {
  version: number;
  state: string;
  platform: string;
  config: string;
  identity: string;
  database: string;
  workspaces: number;
  codex: string;
  authentication: string;
  recovery: string;
  remoteControl: string;
  relayLastError?: string;
  relayLastSeen?: string;
  compatibility?: string;
  workspaceStatus?: string;
  credential?: string;
  autostart: string;
};

export type NodeConfig = {
  revision?: string;
  host?: { name?: string };
  transport?: { mode?: string };
  relay?: { url?: string; proxyUrl?: string; connectTimeoutSeconds?: number; credentialConfigured?: boolean };
  adapter?: { codexEnabled?: boolean; binaryConfigured?: boolean; homeConfigured?: boolean; runtimeMode?: string };
  events?: { maxAgeHours?: number; maxSizeMiB?: number };
  workspaces?: Array<{ id: string; name?: string; permissionProfile?: string; allowNetwork?: boolean }>;
  pendingChanges?: number;
};

export type ConfigChange = {
  id: string;
  baseRevision: string;
  state: string;
  createdAt: string;
  errorCode?: string;
  fields?: string[];
};

export type Overview = {
  status: NodeStatus;
  config?: NodeConfig;
  configChanges?: ConfigChange[];
  pairings?: Array<{ PairingID: string; Name: string; Fingerprint: string; ExpiresAt: string }>;
  clients?: Array<{ ClientID: string; KeyID: string; Fingerprint: string; Status: string }>;
  enrollments?: Array<{ EnrollmentID: string; Name: string; OS: string; Fingerprint: string; ExpiresAt: string }>;
  devices?: Array<{ NodeID: string; Name: string; OS: string; Version: string; Status: string; Fingerprint: string; Online: boolean }>;
};

type SessionResponse = { session: string };

export class LocalNodeAPI {
  private session = "";

  async connect(fragment = window.location.hash.slice(1)): Promise<void> {
    if (!fragment) throw new Error("请从 Yuanshu 菜单栏重新打开本机控制中心");
    history.replaceState(null, "", window.location.pathname);
    const response = await fetch("/api/v1/session", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token: fragment }),
      cache: "no-store",
    });
    const value = await readJSON<SessionResponse>(response);
    if (!value.session) throw new Error("本机会话不可用");
    this.session = value.session;
  }

  overview(signal?: AbortSignal): Promise<Overview> {
    return this.request<Overview>("/api/v1/overview", { signal });
  }

  action(command: string, fields: Record<string, unknown> = {}): Promise<Record<string, unknown>> {
    return this.request<Record<string, unknown>>("/api/v1/action", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ command, ...fields }),
    });
  }

  private async request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const headers = new Headers(init.headers);
    headers.set("Authorization", `YuanshuLocal ${this.session}`);
    const response = await fetch(path, { ...init, headers, cache: "no-store" });
    return readJSON<T>(response);
  }
}

async function readJSON<T>(response: Response): Promise<T> {
  const value = await response.json().catch(() => ({})) as T & { error?: string };
  if (!response.ok) throw new Error(value.error || `本机请求失败 (${response.status})`);
  return value;
}
