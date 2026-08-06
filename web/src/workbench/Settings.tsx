import { useEffect, useState, type FormEvent } from "react";

import { Button as UiButton, Card } from "@yuanshu/ui/base";
import { Tabs as UiTabs, TabsList, TabsTrigger } from "@yuanshu/ui/tabs";

import { normalizeRuntimeSettings, type RuntimeSettings } from "../relay/runtime-config";
import { RELAY_SUBPROTOCOL } from "../relay/session";
import type { ControlStorage } from "../relay/storage";
import { Icon } from "./Icon";
import type { WorkbenchSession } from "./session";
import { machineStatus } from "../status/catalog.generated";

type SettingsSection = "basic" | "security" | "advanced";

export function SettingsView({ session, storage, settings, connectionState, selectedNodeId, onSettingsSaved, onDevices }: { session: WorkbenchSession; storage: ControlStorage; settings: RuntimeSettings; connectionState: string; selectedNodeId: string; onSettingsSaved: () => void; onDevices?: () => void }) {
  const [section, setSection] = useState<SettingsSection>("basic");
  return <section className="utility-view" aria-labelledby="settings-title">
    <div className="utility-heading"><div><p>设置</p><h1 id="settings-title">浏览器与安全</h1></div></div>
    <UiTabs value={section} onValueChange={(value) => setSection(value as SettingsSection)}>
      <TabsList className="settings-sections" aria-label="设置分类">{(["basic", "security", "advanced"] as const).map((value) => <TabsTrigger type="button" className={section === value ? "active" : ""} value={value} onClick={() => setSection(value)} key={value}>{settingsSectionLabel(value)}</TabsTrigger>)}</TabsList>
    </UiTabs>
    {section === "basic" && <div className="settings-overview"><Card className="settings-card"><div className="settings-card-heading"><div><p>当前浏览器</p><h2>{settings.displayName || "未命名浏览器"}</h2></div><Icon name="node" /></div><dl className="settings-facts"><div><dt>授权状态</dt><dd>{connectionState === "reauth_required" ? "需要重新配对" : "已保存控制端身份"}</dd></div><div><dt>实时连接</dt><dd>{connectionStateLabel(connectionState)}</dd></div><div><dt>显示偏好</dt><dd>跟随浏览器与系统设置</dd></div></dl><p className="settings-help">当前浏览器的控制端私钥保存在 IndexedDB 中，不会发送给 Server。</p></Card><Card className="settings-card"><div className="settings-card-heading"><div><p>下一步</p><h2>{connectionState === "connected" ? "工作台可以安全接续任务" : "检查当前连接"}</h2></div><Icon name={connectionState === "connected" ? "check" : "warning"} /></div><p className="settings-help">连接中断不会停止设备上正在执行的任务。恢复后会继续同步事件和控制结果。</p><div className="settings-links"><UiButton variant="secondary" type="button" onClick={() => void session.refreshAll()}><Icon name="refresh" />刷新状态</UiButton>{onDevices && <UiButton variant="secondary" type="button" onClick={onDevices}><Icon name="node" />设备与 Agent</UiButton>}{connectionState === "reauth_required" && <a className="button warning" href={settings.pairingUrl}>重新配对</a>}</div></Card></div>}
    {section === "security" && <div className="settings-overview"><Card className="settings-card"><div className="settings-card-heading"><div><p>安全</p><h2>控制端授权</h2></div><Icon name="lock" /></div><p className="settings-help">查看任务不会取得控制权。发送、纠偏、停止和审批仍需要当前任务的控制权。</p><div className="settings-links"><a className="button primary" href={settings.pairingUrl}>配对新的浏览器</a>{settings.adminEnabled && <a className="button secondary" href={settings.adminUrl ?? "/admin"}>管理已授权控制端</a>}</div></Card><Card className="settings-card"><div className="settings-card-heading"><div><p>边界</p><h2>本地环境保持在设备内</h2></div><Icon name="node" /></div><p className="settings-help">工作区真实路径、Codex 凭据、SSH/Git 凭据和控制端私钥不会在普通工作台中展示。扩大工作区权限或开启网络仍需设备本地确认。</p></Card></div>}
    {section === "advanced" && <><div className="settings-columns"><ConnectionSettings initial={settings} storage={storage} onSaved={onSettingsSaved} />{selectedNodeId ? <NodeSettings session={session} nodeId={selectedNodeId} /> : <div className="state-panel"><Icon name="node" /><b>尚未选择设备</b><p>选择一台设备后，可以读取它的脱敏高级配置。</p></div>}</div><div className="settings-links">{settings.adminEnabled && <a href={settings.adminUrl ?? "/admin"}>打开 Server 管理</a>}</div></>}
  </section>;
}

function settingsSectionLabel(value: SettingsSection) {
  return ({ basic: "基础", security: "安全", advanced: "高级" } as const)[value];
}

function connectionStateLabel(value: string) {
  if (value === "connected") return machineStatus("online")!.title;
  if (value === "reconnecting") return machineStatus("reconnecting")!.title;
  return ({ connected: "实时连接", connecting: "正在连接", authenticating: "正在安全认证", reconnecting: "正在重连", reauth_required: "需要重新配对", paused: "连接已暂停", closed: "连接已关闭", idle: "尚未连接" } as Record<string, string>)[value] ?? "状态未知";
}

export function ConnectionSettings({ initial, storage, compact = false, onSaved }: { initial: RuntimeSettings; storage: ControlStorage; compact?: boolean; onSaved: () => void }) {
  const [relayUrl, setRelayUrl] = useState(initial.relayUrl);
  const [pairingUrl, setPairingUrl] = useState(initial.pairingUrl);
  const [displayName, setDisplayName] = useState(initial.displayName ?? "");
  const [error, setError] = useState("");
  const [testStatus, setTestStatus] = useState("");
  const [saving, setSaving] = useState(false);

  const save = async (event: FormEvent) => {
    event.preventDefault();
    setError("");
    try {
      const value = normalizeRuntimeSettings({ relayUrl, pairingUrl, displayName });
      setSaving(true);
      await storage.putRuntimeSettings(value);
      onSaved();
    } catch (value) {
      setError(value instanceof Error ? value.message : "连接设置无效");
      setSaving(false);
    }
  };

  const testConnection = () => {
    setError("");
    setTestStatus("");
    let value: RuntimeSettings;
    try {
      value = normalizeRuntimeSettings({ relayUrl, pairingUrl, displayName });
    } catch (error) {
      setError(error instanceof Error ? error.message : "连接设置无效");
      return;
    }
    if (typeof WebSocket === "undefined") {
      setTestStatus("当前浏览器不支持 WebSocket");
      return;
    }
    setTestStatus("正在测试 WSS 和 TLS");
    let finished = false;
    const socket = new WebSocket(value.relayUrl, RELAY_SUBPROTOCOL);
    const timer = window.setTimeout(() => {
      if (finished) return;
      finished = true;
      socket.close();
      setTestStatus("连接超时，请检查 IP、端口和证书信任");
    }, 5_000);
    socket.onopen = () => {
      if (finished) return;
      finished = true;
      window.clearTimeout(timer);
      socket.close();
      setTestStatus("WSS 和 TLS 可达，保存后会完成身份认证");
    };
    socket.onerror = () => {
      if (finished) return;
      finished = true;
      window.clearTimeout(timer);
      setTestStatus("连接失败，请检查证书 SAN、Origin 和网络");
    };
  };

  const reset = async () => {
    setError("");
    await storage.removeRuntimeSettings();
    onSaved();
  };

  return <section className={`settings-card ${compact ? "compact" : ""}`} aria-label="连接设置">
    <div className="settings-card-heading"><div><p>浏览器连接</p><h2>Relay 与配对地址</h2></div><Icon name="lock" /></div>
    <p className="settings-help">远程 Relay 必须使用 <code>wss://</code>，配对页必须使用 <code>https://</code>；只有字面量 <code>127.0.0.1</code> 或 <code>::1</code> 可使用本机明文连接。</p>
    <form onSubmit={(event) => void save(event)}>
      <label><span>Relay URL</span><input value={relayUrl} onChange={(event) => setRelayUrl(event.target.value)} placeholder="wss://192.168.1.20:9527/web/connect" inputMode="url" /></label>
      <label><span>Pairing URL</span><input value={pairingUrl} onChange={(event) => setPairingUrl(event.target.value)} placeholder="https://192.168.1.20:9527/pair" inputMode="url" /></label>
      <label><span>设备显示名称（可选）</span><input value={displayName} onChange={(event) => setDisplayName(event.target.value)} maxLength={128} /></label>
      {error && <small className="form-error" role="alert">{error}</small>}
      {testStatus && <small className="form-status">{testStatus}</small>}
      <div className="form-actions"><button className="button secondary" type="button" onClick={testConnection}>测试连接</button><button className="button quiet" type="button" onClick={() => void reset()}>恢复默认</button><button className="button primary" type="submit" disabled={saving}>{saving ? "保存中" : "保存并重连"}</button></div>
    </form>
  </section>;
}

type NodeConfigView = {
  revision: string;
  host?: { name?: string };
  relay?: { url?: string; proxyUrl?: string; connectTimeoutSeconds?: number; credentialConfigured?: boolean };
  events?: { maxAgeHours?: number; maxSizeMiB?: number };
  adapter?: { codexEnabled?: boolean; runtimeMode?: string };
  workspaces?: Array<{ id: string; name?: string; permissionProfile?: string; allowNetwork?: boolean }>;
  pendingChanges?: number;
  pendingChangeSummaries?: ConfigChangeView[];
};

type ConfigChangeView = { id: string; risk?: string; relayReconnect?: boolean; permissionChange?: string; expiresAt?: string; expired?: boolean; details?: Array<{ category: string; before: string; after: string; risk: string }> };

function NodeSettings({ session, nodeId }: { session: WorkbenchSession; nodeId: string }) {
  const [view, setView] = useState<NodeConfigView>();
  const [hostName, setHostName] = useState("");
  const [relayUrl, setRelayUrl] = useState("");
  const [proxyUrl, setProxyUrl] = useState("");
  const [timeout, setTimeoutValue] = useState(30);
  const [maxAge, setMaxAge] = useState(168);
  const [maxSize, setMaxSize] = useState(256);
  const [status, setStatus] = useState("正在读取 Node 配置");
  const [saving, setSaving] = useState(false);

  const applyView = (value: NodeConfigView) => {
    setView(value);
    setHostName(value.host?.name ?? "");
    setRelayUrl(value.relay?.url ?? "");
    setProxyUrl(value.relay?.proxyUrl ?? "");
    setTimeoutValue(value.relay?.connectTimeoutSeconds ?? 30);
    setMaxAge(value.events?.maxAgeHours ?? 168);
    setMaxSize(value.events?.maxSizeMiB ?? 256);
  };

  const read = async () => {
    if (session.client.state !== "connected") {
      setStatus("Node 当前离线，连接恢复后可以读取配置");
      return;
    }
    setStatus("正在读取 Node 配置");
    try {
      const result = await session.request("config.read", {}, { nodeId });
      const payload = result.payload as { config?: NodeConfigView };
      if (!payload.config) throw new Error("Node 没有返回脱敏配置");
      applyView(payload.config);
      setStatus("已读取脱敏配置");
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "读取 Node 配置失败");
    }
  };

  useEffect(() => { void read(); }, [nodeId, session.client.state]);

  const save = async (event: FormEvent) => {
    event.preventDefault();
    if (!view) return;
    setSaving(true);
    try {
      const changes: Record<string, unknown> = {
        hostName: hostName.trim(),
        proxyUrl: proxyUrl.trim(),
        connectTimeoutSeconds: Number(timeout),
        eventsMaxAgeHours: Number(maxAge),
        eventsMaxSizeMiB: Number(maxSize),
      };
      if (relayUrl.trim()) changes.relayUrl = relayUrl.trim();
      const result = await session.request("config.update", { baseRevision: view.revision, changes }, { nodeId });
      const payload = result.payload as { config?: NodeConfigView; change?: ConfigChangeView; requiresLocalConfirmation?: boolean; applied?: boolean };
      if (payload.config) applyView(payload.config);
      setStatus(payload.requiresLocalConfirmation ? "已提交，Relay 或代理变更需要 Node 本机确认" : payload.applied ? "已应用，Node 正在安全重载" : "更新已提交");
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "更新失败，请重新读取 revision");
    } finally {
      setSaving(false);
    }
  };

  return <section className="settings-card" aria-label="Node 设置">
    <div className="settings-card-heading"><div><p>Node {nodeId.slice(0, 10)}</p><h2>脱敏配置与本机确认</h2></div><button className="icon-action" type="button" onClick={() => void read()} aria-label="重新读取 Node 配置"><Icon name="refresh" /></button></div>
    <p className="settings-help">配置 revision：<code>{view?.revision ?? "未读取"}</code>。Relay、代理和工作区安全边界变更需要本机确认。凭据、路径、Server 监听和 TLS 私钥不可远程修改。</p>
    <form onSubmit={(event) => void save(event)}>
      <label><span>Node 显示名称</span><input value={hostName} onChange={(event) => setHostName(event.target.value)} maxLength={128} /></label>
      <div className="settings-grid"><label><span>Relay URL（需本机确认）</span><input value={relayUrl} onChange={(event) => setRelayUrl(event.target.value)} inputMode="url" /></label><label><span>Relay Proxy URL（需本机确认）</span><input value={proxyUrl} onChange={(event) => setProxyUrl(event.target.value)} inputMode="url" /></label></div>
      <div className="settings-grid three"><label><span>连接超时（秒）</span><input type="number" min={1} max={300} value={timeout} onChange={(event) => setTimeoutValue(Number(event.target.value))} /></label><label><span>事件保留（小时）</span><input type="number" min={1} max={8760} value={maxAge} onChange={(event) => setMaxAge(Number(event.target.value))} /></label><label><span>事件上限（MiB）</span><input type="number" min={1} max={16384} value={maxSize} onChange={(event) => setMaxSize(Number(event.target.value))} /></label></div>
      {view?.adapter && <div className="config-facts"><span>Codex：{view.adapter.codexEnabled ? "已启用" : "未启用"}</span><span>运行时：{view.adapter.runtimeMode || "默认"}</span><span>凭据：{view.relay?.credentialConfigured ? "已配置，不展示" : "未配置"}</span><span>待确认：{view.pendingChanges ?? 0}</span></div>}
      {view?.pendingChangeSummaries?.map((change) => <article className="pending-config-summary" key={change.id}><b>{change.expired ? "配置变更已过期" : machineStatus("config_pending")!.title}</b>{change.details?.map((detail) => <span key={detail.category}>{detail.category}：{detail.before} → {detail.after}</span>)}<small>{change.relayReconnect ? "会重连 Relay" : "不重连 Relay"} · 权限{change.permissionChange === "reduced" ? "缩小" : change.permissionChange === "expanded" ? "扩大" : "不变"}</small></article>)}
      {view?.workspaces?.map((workspace) => <div className="workspace-fact" key={workspace.id}><b>{workspace.name || workspace.id}</b><span>{workspace.permissionProfile === "workspace-write" ? "可写" : "只读"}</span><span>{workspace.allowNetwork ? "网络开启" : "网络关闭"}</span></div>)}
      <small className={/失败|离线/.test(status) ? "form-error" : "form-status"}>{status}</small>
      <div className="form-actions"><button className="button primary" type="submit" disabled={saving || !view || session.client.state !== "connected"}>{saving ? "保存中" : "保存 Node 配置"}</button></div>
    </form>
  </section>;
}
