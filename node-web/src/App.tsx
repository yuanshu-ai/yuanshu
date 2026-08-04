import { useCallback, useEffect, useMemo, useState } from "react";

import { ConfigChange, LocalNodeAPI, NodeConfig, Overview } from "./api";
import { machineStatus } from "./status/catalog.generated";

type Section = "overview" | "connection" | "codex" | "workspaces" | "access" | "diagnostics";

const sections: Array<{ id: Section; label: string }> = [
  { id: "overview", label: "概览" },
  { id: "connection", label: "连接" },
  { id: "codex", label: "Codex" },
  { id: "workspaces", label: "工作区" },
  { id: "access", label: "访问权限" },
  { id: "diagnostics", label: "诊断" },
];

export function App() {
  const api = useMemo(() => new LocalNodeAPI(), []);
  const [section, setSection] = useState<Section>("overview");
  const [overview, setOverview] = useState<Overview>();
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [pairingUrl, setPairingUrl] = useState("");
  const [busy, setBusy] = useState(false);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    try {
      setOverview(await api.overview(signal));
      setError("");
    } catch (value) {
      if (!signal?.aborted) setError(errorText(value));
    }
  }, [api]);

  useEffect(() => {
    const controller = new AbortController();
    void api.connect().then(() => refresh(controller.signal)).catch((value) => setError(errorText(value)));
    return () => controller.abort();
  }, [api, refresh]);

  useEffect(() => {
    if (!overview) return;
    const timer = window.setInterval(() => void refresh(), 5000);
    return () => window.clearInterval(timer);
  }, [overview, refresh]);

  const run = async (command: string, fields: Record<string, unknown> = {}) => {
    setBusy(true);
    setMessage("");
    try {
      const result = await api.action(command, fields);
      if (typeof result.pairingUrl === "string") setPairingUrl(result.pairingUrl);
      setMessage("操作已提交");
      await refresh();
      return result;
    } catch (value) {
      setMessage(errorText(value));
      return {};
    } finally {
      setBusy(false);
    }
  };

  const updateConfig = async (changes: Record<string, unknown>) => {
    const revision = overview?.config?.revision;
    if (!revision) throw new Error("配置 revision 不可用，请先刷新");
    await run("config_update", { baseRevision: revision, changes });
  };

  if (!overview) return <Gate error={error} />;
  if (overview.setup?.required) return <SetupWizard value={overview} run={run} busy={busy} error={error} message={message} />;
  const status = overview.status;
  return (
    <main className="shell">
      <aside className="sidebar">
        <div className="brand"><span className="brand-mark">枢</span><div><strong>Yuanshu Node</strong><small>本机控制中心</small></div></div>
        <nav aria-label="Node 设置">
          {sections.map((item) => <button key={item.id} className={section === item.id ? "active" : ""} onClick={() => setSection(item.id)}>{item.label}</button>)}
        </nav>
        <div className="sidebar-state"><StatusMark state={status.state} /><span>{stateLabel(status.state)}</span><small>{status.platform}</small></div>
      </aside>
      <section className="content">
        <header className="topbar"><div><h1>{sections.find((item) => item.id === section)?.label}</h1><p>{sectionDescription(section)}</p></div><button className="secondary" onClick={() => void refresh()} disabled={busy}>刷新</button></header>
        {error && <Notice tone="danger">{error}</Notice>}
        {message && <Notice>{message}</Notice>}
        {section === "overview" && <OverviewPanel value={overview} run={run} updateConfig={updateConfig} busy={busy} />}
        {section === "connection" && <ConnectionPanel config={overview.config} status={status} run={run} updateConfig={updateConfig} busy={busy} />}
        {section === "codex" && <CodexPanel config={overview.config} status={status} />}
        {section === "workspaces" && <WorkspacePanel config={overview.config} updateConfig={updateConfig} busy={busy} />}
        {section === "access" && <AccessPanel value={overview} pairingUrl={pairingUrl} run={run} busy={busy} />}
        {section === "diagnostics" && <DiagnosticsPanel value={overview} />}
      </section>
    </main>
  );
}

function SetupWizard({ value, run, busy, error, message }: { value: Overview; run: (command: string, fields?: Record<string, unknown>) => Promise<Record<string, unknown>>; busy: boolean; error: string; message: string }) {
  const setup = value.setup!;
  const [name, setName] = useState(setup.defaultName);
  const [relayUrl, setRelayUrl] = useState("");
  const [codexBinary, setCodexBinary] = useState(setup.defaultCodex || "codex");
  const [enrollmentMode, setEnrollmentMode] = useState<"bootstrap" | "join">("bootstrap");
  const [enrollmentSecret, setEnrollmentSecret] = useState("");
  const [workspaceToken, setWorkspaceToken] = useState("");
  const [workspaceName, setWorkspaceName] = useState("");
  const [permission, setPermission] = useState("read-only");
  const [allowNetwork, setAllowNetwork] = useState(false);
  const [relayTested, setRelayTested] = useState(false);

  const pick = async () => {
    const result = await run("setup_pick");
    if (typeof result.workspaceToken === "string") setWorkspaceToken(result.workspaceToken);
    if (typeof result.workspaceName === "string") setWorkspaceName(result.workspaceName);
  };
  const test = async () => {
    const result = await run("setup_test", { relayUrl });
    setRelayTested(Boolean(result.config));
  };
  const complete = async (event: React.FormEvent) => {
    event.preventDefault();
    const result = await run("setup_complete", {
      name, relayUrl, codexBinary, workspaceToken, workspaceName, permissionProfile: permission, allowNetwork,
      bootstrapSecret: enrollmentMode === "bootstrap" ? enrollmentSecret : undefined,
      joinUrl: enrollmentMode === "join" ? enrollmentSecret : undefined,
    });
    if (result.ok === true) setEnrollmentSecret("");
  };

  return <main className="setup-shell"><header className="setup-header"><div className="brand"><span className="brand-mark">枢</span><div><strong>设置 Yuanshu Node</strong><small>所有敏感步骤仅在这台电脑完成</small></div></div><span className="pending-badge">{setup.platform}</span></header>{error && <Notice tone="danger">{error}</Notice>}{message && <Notice>{message}</Notice>}<form className="setup-flow" onSubmit={(event) => void complete(event)}><section className="section-panel"><span className="step-label">01 · 设备</span><h2>这台电脑如何显示</h2><label><span>Node 名称</span><input value={name} maxLength={128} required onChange={(event) => setName(event.target.value)} /></label><label><span>Codex 可执行文件</span><input className="mono" value={codexBinary} required onChange={(event) => setCodexBinary(event.target.value)} /></label><p className="helper">Codex 版本只用于兼容提示；未验证版本仍会尝试启动。</p></section><section className="section-panel"><span className="step-label">02 · Relay</span><h2>连接个人 Server</h2><label><span>Relay WSS 地址</span><input className="mono" type="url" value={relayUrl} placeholder="wss://192.168.1.20:7444/node/connect" required onChange={(event) => { setRelayUrl(event.target.value); setRelayTested(false); }} /></label><div className="actions"><button type="button" className="secondary" disabled={busy || !relayUrl} onClick={() => void test()}>{relayTested ? "重新测试" : "测试 TLS 连接"}</button></div><div className="segmented" role="group" aria-label="绑定方式"><button type="button" className={enrollmentMode === "bootstrap" ? "active" : ""} onClick={() => { setEnrollmentMode("bootstrap"); setEnrollmentSecret(""); }}>首台 Node</button><button type="button" className={enrollmentMode === "join" ? "active" : ""} onClick={() => { setEnrollmentMode("join"); setEnrollmentSecret(""); }}>加入已有 Owner</button></div><label><span>{enrollmentMode === "bootstrap" ? "Server bootstrap secret" : "Enrollment join URL"}</span><input className="mono" type="password" autoComplete="off" value={enrollmentSecret} required onChange={(event) => setEnrollmentSecret(event.target.value)} /></label><p className="helper">秘密只用于本次本机请求，不会写入配置、日志或浏览器存储。</p></section><section className="section-panel"><span className="step-label">03 · 工作区</span><h2>授权一个本机目录</h2><p className="helper">浏览器不能输入路径。目录由系统选择器返回给 Node，并在保存前再次检查边界。</p><button type="button" className="secondary" disabled={busy || !setup.pickerAvailable} onClick={() => void pick()}>{workspaceToken ? "重新选择目录" : "选择工作区…"}</button>{!setup.pickerAvailable && <Notice tone="danger">当前环境没有可用的原生目录选择器，请改用本机 CLI。</Notice>}{workspaceToken && <div className="selection-summary"><strong>{workspaceName || "已选择工作区"}</strong><small>路径只保存在 Node 本机，不显示在此页面</small></div>}<label><span>显示名称</span><input value={workspaceName} required disabled={!workspaceToken} onChange={(event) => setWorkspaceName(event.target.value)} /></label><div className="settings-form two-columns"><label><span>权限</span><select value={permission} onChange={(event) => setPermission(event.target.value)}><option value="read-only">只读（推荐）</option><option value="workspace-write">允许工作区写入</option></select></label><label className="check"><input type="checkbox" checked={allowNetwork} onChange={(event) => setAllowNetwork(event.target.checked)} /><span>允许任务使用网络</span></label></div></section><section className="section-panel safety-review"><span className="step-label">04 · 确认</span><h2>安全摘要</h2><ul><li>Node 只向 Relay 建立出站 WSS。</li><li>Codex app-server 不会监听公网。</li><li>工作区默认为只读且禁止网络。</li><li>凭据保存于系统安全存储。</li></ul><button className="primary" type="submit" disabled={busy || !name.trim() || !relayUrl || !enrollmentSecret || !workspaceToken}>{busy ? "正在设置…" : "完成设置并启动 Node"}</button></section></form></main>;
}

function Gate({ error }: { error: string }) {
  return <main className="gate"><span className="brand-mark">枢</span><h1>{error ? "无法打开本机控制中心" : "正在连接 Yuanshu Node"}</h1><p>{error || "正在建立仅限本机的短期管理会话。"}</p>{error && <small>请关闭此页面，然后从菜单栏、托盘或 `yuanshu node ui` 重新打开。</small>}</main>;
}

function OverviewPanel({ value, run, updateConfig, busy }: { value: Overview; run: (command: string, fields?: Record<string, unknown>) => Promise<unknown>; updateConfig: (changes: Record<string, unknown>) => Promise<void>; busy: boolean }) {
  const status = value.status;
  const next = nextAction(status);
  const [name, setName] = useState(value.config?.host?.name ?? "");
  useEffect(() => setName(value.config?.host?.name ?? ""), [value.config?.revision, value.config?.host?.name]);
  return <div className="stack"><section className="hero-panel"><div><StatusMark state={status.state} large /><h2>{stateLabel(status.state)}</h2><p>{next.detail}</p></div><button className="primary" disabled={busy} onClick={() => void run(next.command)}>{next.label}</button></section><section className="status-grid"><StatusRow label="Relay" value={status.remoteControl} /><StatusRow label="Codex" value={status.codex} /><StatusRow label="身份" value={status.identity} /><StatusRow label="恢复" value={status.recovery} /><StatusRow label="工作区" value={String(status.workspaces)} /><StatusRow label="登录启动" value={status.autostart} /></section><section className="section-panel"><h2>Node 名称</h2><p className="helper">名称会显示在你自己的控制端中，不包含设备路径或身份凭据。</p><form className="settings-form" onSubmit={(event) => { event.preventDefault(); void updateConfig({ hostName: name }); }}><label><span>显示名称</span><input value={name} maxLength={128} required onChange={(event) => setName(event.target.value)} /></label><div className="actions"><button className="secondary" type="button" disabled={busy || status.autostart === "unavailable"} onClick={() => void run("autostart_set", { enabled: status.autostart !== "enabled" })}>{status.autostart === "enabled" ? "关闭登录启动" : "启用登录启动"}</button><button className="primary" disabled={busy || !name.trim()} type="submit">保存名称</button></div></form></section></div>;
}

function ConnectionPanel({ config, status, run, updateConfig, busy }: { config?: NodeConfig; status: Overview["status"]; run: (command: string) => Promise<unknown>; updateConfig: (changes: Record<string, unknown>) => Promise<void>; busy: boolean }) {
  const [relayUrl, setRelayUrl] = useState(config?.relay?.url ?? "");
  const [proxyUrl, setProxyUrl] = useState(config?.relay?.proxyUrl ?? "");
  const [timeout, setTimeoutValue] = useState(config?.relay?.connectTimeoutSeconds ?? 15);
  const [maxAge, setMaxAge] = useState(config?.events?.maxAgeHours ?? 168);
  const [maxSize, setMaxSize] = useState(config?.events?.maxSizeMiB ?? 256);
  useEffect(() => {
    setRelayUrl(config?.relay?.url ?? ""); setProxyUrl(config?.relay?.proxyUrl ?? ""); setTimeoutValue(config?.relay?.connectTimeoutSeconds ?? 15);
    setMaxAge(config?.events?.maxAgeHours ?? 168); setMaxSize(config?.events?.maxSizeMiB ?? 256);
  }, [config?.revision]);
  return <div className="stack"><section className="section-panel"><h2>Relay</h2><p className="helper">Node 只建立出站 WSS，不公开 Codex app-server。Relay 或代理变更会等待原生确认。</p><dl><Info label="状态" value={status.remoteControl} /><Info label="最后错误" value={status.relayLastError || "无"} /><Info label="凭据" value={config?.relay?.credentialConfigured ? "已配置，内容不显示" : "未配置"} /></dl><form className="settings-form" onSubmit={(event) => { event.preventDefault(); void updateConfig({ relayUrl, proxyUrl: proxyUrl || null, connectTimeoutSeconds: timeout }); }}><label><span>Relay WSS 地址</span><input className="mono" value={relayUrl} required inputMode="url" onChange={(event) => setRelayUrl(event.target.value)} /></label><label><span>代理地址，可留空</span><input className="mono" value={proxyUrl} inputMode="url" onChange={(event) => setProxyUrl(event.target.value)} /></label><label><span>连接超时，秒</span><input type="number" min="1" max="300" value={timeout} onChange={(event) => setTimeoutValue(Number(event.target.value))} /></label><div className="actions"><button className="secondary" type="button" disabled={busy} onClick={() => void run("reload")}>重新加载</button><button className="primary" disabled={busy} type="submit">保存连接设置</button></div></form></section><section className="section-panel"><h2>事件日志保留</h2><p className="helper">事件正文只保存在 Node 本机。调小限制会减少磁盘占用，也会缩短断线恢复窗口。</p><form className="settings-form two-columns" onSubmit={(event) => { event.preventDefault(); void updateConfig({ eventsMaxAgeHours: maxAge, eventsMaxSizeMiB: maxSize }); }}><label><span>最长保留，小时</span><input type="number" min="1" value={maxAge} onChange={(event) => setMaxAge(Number(event.target.value))} /></label><label><span>最大容量，MiB</span><input type="number" min="1" value={maxSize} onChange={(event) => setMaxSize(Number(event.target.value))} /></label><div className="actions"><button className="primary" disabled={busy} type="submit">保存保留策略</button></div></form></section></div>;
}

function CodexPanel({ config, status }: { config?: NodeConfig; status: Overview["status"] }) {
  return <section className="section-panel"><h2>本机 Codex</h2><p className="helper">版本检测只提供兼容建议，未知版本仍会尝试启动。</p><dl><Info label="状态" value={status.codex} /><Info label="兼容性" value={status.compatibility || "未检测"} /><Info label="认证" value={status.authentication} /><Info label="运行模式" value={config?.adapter?.runtimeMode || "stdio"} /><Info label="Binary" value={config?.adapter?.binaryConfigured ? "已配置" : "使用默认 PATH"} /></dl></section>;
}

function WorkspacePanel({ config, updateConfig, busy }: { config?: NodeConfig; updateConfig: (changes: Record<string, unknown>) => Promise<void>; busy: boolean }) {
  const items = config?.workspaces ?? [];
  return <section className="section-panel"><h2>已授权工作区</h2><p className="helper">远程任务只能引用这里登记的 workspace ID。绝对路径不会发送到 Server。新增目录、扩大写权限或开启网络必须使用本机 CLI。</p>{items.length ? <div className="workspace-list">{items.map((item) => <WorkspaceEditor key={item.id} item={item} busy={busy} save={(changes) => updateConfig({ workspaces: [{ id: item.id, ...changes }] })} />)}</div> : <Empty title="还没有工作区" detail="请使用本机 CLI 登记目录并确认权限边界。" />}</section>;
}

function AccessPanel({ value, pairingUrl, run, busy }: { value: Overview; pairingUrl: string; run: (command: string, fields?: Record<string, unknown>) => Promise<unknown>; busy: boolean }) {
  const changes = value.configChanges ?? [];
  const [decision, setDecision] = useState<{ change: ConfigChange; approve: boolean }>();
  const decide = async () => {
    if (!decision) return;
    await run(decision.approve ? "config_approve" : "config_reject", { changeId: decision.change.id });
    setDecision(undefined);
  };
  return <div className="stack"><section className="section-panel"><h2>配对新的浏览器</h2><p className="helper">配对链接包含一次性秘密，请只发送到你自己的设备。页面关闭后不会保存这个链接。</p><button className="primary" disabled={busy} onClick={() => void run("pairing_create")}>生成配对链接</button>{pairingUrl && <div className="secret-output"><code>{pairingUrl}</code><button className="secondary" onClick={() => void navigator.clipboard.writeText(pairingUrl)}>复制</button></div>}{value.pairings?.length ? <DirectoryList title="待审核配对" items={value.pairings.map((item) => ({ id: item.PairingID, title: item.Name || "未命名浏览器", detail: item.Fingerprint }))} /> : null}</section><section className="section-panel"><h2>受信控制端</h2>{value.clients?.length ? <DirectoryList items={value.clients.map((item) => ({ id: `${item.ClientID}:${item.KeyID}`, title: item.Fingerprint, detail: item.Status }))} /> : <Empty title="没有受信浏览器" detail="完成首次配对后会显示在这里。" />}</section><section className="section-panel"><h2>个人设备</h2>{value.devices?.length ? <DirectoryList items={value.devices.map((item) => ({ id: item.NodeID, title: item.Name || item.NodeID, detail: `${item.OS} · ${item.Online ? "在线" : "离线"}` }))} /> : <Empty title="没有其他 Node" detail="同一 Owner 下的设备会显示在这里。" />}</section><section className="section-panel"><h2>待确认配置</h2>{changes.length ? <><Notice>托盘只负责打开此页面。请在这里核对完整安全摘要后再决定，或使用本机 CLI。</Notice><div className="change-list">{changes.map((change) => <article className="config-change-card" key={change.id}><div><strong>{change.expired ? "变更已过期" : change.risk === "high" ? "高风险配置变更" : "配置变更"}</strong><small>{change.id} · {new Date(change.createdAt).toLocaleString()}</small>{change.details?.map((item) => <dl key={item.category}><Info label={configCategoryLabel(item.category)} value={`${item.before} → ${item.after}`} /></dl>)}<small>{change.relayReconnect ? "应用后会重连 Relay" : "不触发 Relay 重连"} · 权限 {permissionChangeLabel(change.permissionChange)}</small></div><div className="config-change-actions"><span className="pending-badge">{change.expired ? "已过期" : "等待本机确认"}</span><button className="secondary" disabled={busy} onClick={() => setDecision({ change, approve: false })}>{change.expired ? "清除" : "拒绝"}</button><button className="primary" disabled={busy || change.expired} onClick={() => setDecision({ change, approve: true })}>批准</button></div></article>)}</div></> : <Empty title="没有待确认变更" detail="Relay、工作区权限等安全边界修改会显示在这里。" />}</section>{decision && <ConfigDecisionDialog decision={decision} busy={busy} onCancel={() => setDecision(undefined)} onConfirm={() => void decide()} />}</div>;
}

function ConfigDecisionDialog({ decision, busy, onCancel, onConfirm }: { decision: { change: ConfigChange; approve: boolean }; busy: boolean; onCancel: () => void; onConfirm: () => void }) {
  const change = decision.change;
  return <div className="local-dialog-layer" role="presentation"><section className="local-dialog" role="alertdialog" aria-modal="true" aria-labelledby="config-decision-title"><h2 id="config-decision-title">{decision.approve ? "批准安全配置变更" : "拒绝安全配置变更"}</h2><p>{decision.approve ? "仅当你本人发起并理解下列影响时批准。" : "拒绝后该变更不会写入正式配置。"}</p><dl>{change.details?.map((item) => <Info key={item.category} label={configCategoryLabel(item.category)} value={`${item.before} → ${item.after}`} />)}</dl><ul><li>{change.relayReconnect ? "会停止旧 Relay supervisor 并使用新配置重连；Runtime 保持运行。" : "不会触发 Relay 重连。"}</li><li>权限变化：{permissionChangeLabel(change.permissionChange)}</li><li>有效期：{change.expiresAt ? new Date(change.expiresAt).toLocaleString() : "未报告"}</li></ul><div className="actions"><button className="secondary" disabled={busy} onClick={onCancel}>继续检查</button><button className={decision.approve ? "primary" : "danger"} disabled={busy} onClick={onConfirm}>{busy ? "处理中…" : decision.approve ? "确认批准" : "确认拒绝"}</button></div></section></div>;
}

function configCategoryLabel(value: string) { return ({ relay: "Relay 地址", relay_proxy: "Relay 代理", workspace_policy: "工作区权限" } as Record<string, string>)[value] ?? "受保护设置"; }
function permissionChangeLabel(value?: string) { return value === "reduced" ? "缩小" : value === "expanded" ? "扩大" : "不变"; }

function DiagnosticsPanel({ value }: { value: Overview }) {
  const diagnostics = JSON.stringify(value.status, null, 2);
  return <section className="section-panel"><h2>脱敏诊断</h2><p className="helper">诊断不包含凭据、Prompt、完整命令、Diff 或绝对工作区路径。</p><pre>{diagnostics}</pre><button className="secondary" onClick={() => void navigator.clipboard.writeText(diagnostics)}>复制诊断</button></section>;
}

function WorkspaceEditor({ item, save, busy }: { item: NonNullable<NodeConfig["workspaces"]>[number]; save: (changes: Record<string, unknown>) => Promise<void>; busy: boolean }) {
  const [name, setName] = useState(item.name ?? item.id);
  const [permission, setPermission] = useState(item.permissionProfile ?? "read-only");
  const [network, setNetwork] = useState(Boolean(item.allowNetwork));
  useEffect(() => { setName(item.name ?? item.id); setPermission(item.permissionProfile ?? "read-only"); setNetwork(Boolean(item.allowNetwork)); }, [item.name, item.permissionProfile, item.allowNetwork, item.id]);
  return <article className="workspace-editor"><div><strong>{item.name || item.id}</strong><small>{item.id}</small></div><form className="settings-form" onSubmit={(event) => { event.preventDefault(); void save({ displayName: name, permissionProfile: permission, allowNetwork: network }); }}><label><span>显示名称</span><input value={name} required onChange={(event) => setName(event.target.value)} /></label><label><span>权限</span><select value={permission} onChange={(event) => setPermission(event.target.value)}><option value="read-only">只读</option>{item.permissionProfile === "workspace-write" && <option value="workspace-write">工作区写入</option>}</select></label><label className="check"><input type="checkbox" checked={network} disabled={!item.allowNetwork} onChange={(event) => setNetwork(event.target.checked)} /><span>允许网络，仅可在此关闭</span></label><div className="actions"><button className="secondary" type="submit" disabled={busy}>提交变更</button></div></form></article>;
}

function DirectoryList({ title, items }: { title?: string; items: Array<{ id: string; title: string; detail: string }> }) {
  return <div className="directory-block">{title && <h3>{title}</h3>}<div className="directory-list">{items.map((item) => <div key={item.id}><strong>{item.title}</strong><small>{item.detail}</small></div>)}</div></div>;
}

function StatusRow({ label, value }: { label: string; value: string }) { return <div className="status-row"><span>{label}</span><strong>{value || "unknown"}</strong></div>; }
function Info({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) { return <div><dt>{label}</dt><dd className={mono ? "mono" : ""}>{value}</dd></div>; }
function Empty({ title, detail }: { title: string; detail: string }) { return <div className="empty"><strong>{title}</strong><p>{detail}</p></div>; }
function Notice({ children, tone = "info" }: { children: string; tone?: "info" | "danger" }) { return <div className={`notice ${tone}`} role="status">{children}</div>; }
function StatusMark({ state, large = false }: { state: string; large?: boolean }) { return <span className={`status-mark ${large ? "large" : ""} ${statusTone(state)}`} aria-hidden="true" />; }

function nextAction(status: Overview["status"]): { label: string; detail: string; command: string } {
  if (status.config !== "ready" && status.config !== "recovered") return { label: "重新检查", detail: "Node 配置尚未就绪。请检查本机配置后重新加载。", command: "reload" };
  if (status.identity === "unpaired") return { label: "重新检查", detail: "Node 已启动，尚未绑定到个人 Server。", command: "reload" };
  if (status.remoteControl !== "online") return { label: "重新连接", detail: "本机 Runtime 保持可用，Relay 正在等待恢复。", command: "reload" };
  return { label: "刷新状态", detail: "Node、Codex 和远程控制均已就绪。", command: "reload" };
}
function statusTone(state: string): string { return state === "ready" || state === "online" ? "ok" : state === "needs_attention" || state === "unavailable" ? "danger" : "warning"; }
function stateLabel(state: string): string { const code = ({ ready: "online", starting: "reconnecting", recovering: "reconnecting", needs_attention: "setup_required" } as Record<string, string>)[state] ?? state; return machineStatus(code)?.title ?? ({ unpaired: "等待绑定" } as Record<string, string>)[state] ?? state; }
function sectionDescription(section: Section): string { return ({ overview: "这台电脑上的 Node 状态和下一步操作", connection: "Relay、凭据与出站连接", codex: "本机 Agent Runtime 和兼容状态", workspaces: "允许远程访问的本机目录", access: "浏览器、设备和安全变更", diagnostics: "可复制的脱敏运行信息" })[section]; }
function errorText(value: unknown): string { return value instanceof Error ? value.message : "本机操作失败"; }
