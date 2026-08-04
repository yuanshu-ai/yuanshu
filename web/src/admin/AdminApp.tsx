import { Children, useCallback, useEffect, useRef, useState, type ReactNode } from "react";

import { IndexedDBControlStorage } from "../relay/storage";
import { AdminClient, type AdminAccessRequest, type AdminAudit, type AdminConfig, type AdminControlClient, type AdminLease, type AdminNode, type AdminNodeDetail, type AdminOverview } from "./admin-client";
import { machineStatus } from "../status/catalog.generated";
import "./admin.css";

type Section = "overview" | "nodes" | "clients" | "access" | "security";
type AdminData = { overview?: AdminOverview; nodes: AdminNode[]; nodeDetails: Record<string, AdminNodeDetail>; clients: AdminControlClient[]; requests: AdminAccessRequest[]; leases: AdminLease[]; audit: AdminAudit[]; config?: AdminConfig; diagnostics?: Record<string, unknown> };
type Confirmation = { title: string; detail: string; confirmLabel: string; requiredText?: string; run: () => Promise<void> };

const emptyData: AdminData = { nodes: [], nodeDetails: {}, clients: [], requests: [], leases: [], audit: [] };

export function AdminApp() {
  const [section, setSection] = useState<Section>("overview");
  const [client, setClient] = useState<AdminClient>();
  const [data, setData] = useState<AdminData>(emptyData);
  const [state, setState] = useState<"authenticating" | "ready" | "error">("authenticating");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirmation, setConfirmation] = useState<Confirmation>();
  const alive = useRef(true);

  const refresh = useCallback(async (active: AdminClient, quiet = false) => {
    if (!quiet) setMessage("正在刷新 Server 状态");
    try {
      const results = await Promise.allSettled([
        active.get<AdminOverview>("/v1/admin/overview"),
        active.get<{ nodes: AdminNode[] }>("/v1/admin/nodes"),
        active.get<{ controlClients: AdminControlClient[] }>("/v1/admin/control-clients"),
        active.get<{ requests: AdminAccessRequest[] }>("/v1/admin/access-requests"),
        active.get<{ leases: AdminLease[] }>("/v1/admin/leases"),
        active.get<{ audit: AdminAudit[] }>("/v1/admin/audit"),
        active.get<AdminConfig>("/v1/admin/config"),
        active.get<Record<string, unknown>>("/v1/admin/diagnostics"),
      ]);
      if (!alive.current) return;
      const failed = results.filter((item) => item.status === "rejected").length;
      setData((previous) => ({
        ...previous,
        overview: settledValue<AdminOverview>(results[0]) ?? previous.overview,
        nodes: settledValue<{ nodes: AdminNode[] }>(results[1])?.nodes ?? previous.nodes,
        clients: settledValue<{ controlClients: AdminControlClient[] }>(results[2])?.controlClients ?? previous.clients,
        requests: settledValue<{ requests: AdminAccessRequest[] }>(results[3])?.requests ?? previous.requests,
        leases: settledValue<{ leases: AdminLease[] }>(results[4])?.leases ?? previous.leases,
        audit: settledValue<{ audit: AdminAudit[] }>(results[5])?.audit ?? previous.audit,
        config: settledValue<AdminConfig>(results[6]) ?? previous.config,
        diagnostics: settledValue<Record<string, unknown>>(results[7]) ?? previous.diagnostics,
      }));
      if (results[0].status === "fulfilled") setState("ready");
      else setState((current) => current === "ready" ? current : "error");
      if (!quiet) setMessage(failed ? `${failed} 个管理分区暂时无法读取，其余数据已保留` : "状态已更新");
    } catch (error) {
      if (!alive.current) return;
      setMessage(error instanceof Error ? error.message : "管理状态读取失败");
      if (!data.overview) setState("error");
    }
  }, []);

  useEffect(() => {
    alive.current = true;
    const boot = async () => {
      try {
        const active = new AdminClient(new IndexedDBControlStorage());
        await active.authenticate();
        if (!alive.current) return;
        setClient(active);
        await refresh(active);
      } catch (error) {
        if (!alive.current) return;
        setState("error");
        setMessage(error instanceof Error ? error.message : "管理身份认证失败");
      }
    };
    void boot();
    return () => { alive.current = false; };
  }, []);

  useEffect(() => {
    if (!client || state !== "ready") return;
    const interval = window.setInterval(() => { if (document.visibilityState === "visible") void refresh(client, true); }, 10_000);
    const onVisibility = () => { if (document.visibilityState === "visible") void refresh(client, true); };
    document.addEventListener("visibilitychange", onVisibility);
    return () => { window.clearInterval(interval); document.removeEventListener("visibilitychange", onVisibility); };
  }, [client, state, refresh]);

  const run = async (action: () => Promise<void>) => {
    if (!client) return;
    setBusy(true); setMessage("");
    try { await action(); setConfirmation(undefined); await refresh(client); }
    catch (error) { setMessage(error instanceof Error ? error.message : "管理操作失败"); }
    finally { setBusy(false); }
  };

  const inspectNode = async (nodeId: string) => {
    if (!client) return;
    setMessage("正在读取 Node 故障详情");
    try {
      const detail = await client.get<AdminNodeDetail>(`/v1/admin/nodes/${encodeURIComponent(nodeId)}`);
      setData((current) => ({ ...current, nodeDetails: { ...current.nodeDetails, [nodeId]: detail } }));
      setMessage("Node 详情已更新");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Node 详情读取失败");
    }
  };

  if (state === "authenticating") return <AdminGate title="正在验证管理身份" detail="使用本浏览器已配对的控制端密钥建立短期管理会话。" />;
  if (state === "error" && !data.overview) return <AdminGate title="无法进入 Server 管理" detail={message} action={<a href="/pair">打开配对页面</a>} />;

  const requestConfirmation = (value: Confirmation) => setConfirmation(value);
  const revokeNode = (node: AdminNode) => requestConfirmation({ title: `撤销 ${node.name}`, detail: "该 Node 的连接凭据将立即失效，正在运行的本机任务不会由 Server 接管。至少需要保留一个 active Node。", requiredText: node.name, confirmLabel: "撤销 Node", run: () => client!.highRisk("POST", `/v1/admin/nodes/${encodeURIComponent(node.id)}/revoke`, {}) });
  const revokeClient = (control: AdminControlClient) => requestConfirmation({ title: `撤销 ${control.name}`, detail: control.current ? "这是当前管理控制端。成功后当前管理会话会立即退出。" : "该控制端的 WSS 与管理会话会立即失效。", requiredText: control.name, confirmLabel: "撤销控制端", run: () => client!.highRisk("POST", `/v1/admin/control-clients/${encodeURIComponent(control.id)}/revoke`, {}) });
  const cancelRequest = (item: AdminAccessRequest) => requestConfirmation({ title: "取消接入申请", detail: "申请会被标记为已过期，不能再继续 claim 或批准。", confirmLabel: "取消申请", run: () => client!.mutate("POST", item.kind === "node" ? `/v1/admin/node-enrollments/${encodeURIComponent(item.id)}/cancel` : `/v1/admin/pairings/${encodeURIComponent(item.id)}/cancel`, {}) });
  const releaseLease = (lease: AdminLease) => requestConfirmation({ title: "释放 Thread 控制权", detail: "当前控制端会立即失去租约，旧 epoch 的后续操作将被拒绝。任务本身不会停止。", confirmLabel: "释放租约", run: () => client!.mutate("POST", "/v1/admin/leases/release", { ...lease.scope, expectedEpoch: lease.epoch }) });
  const changeAdmission = (pairing: boolean, enrollment: boolean) => {
    const current = data.config?.admission;
    if (!current) return;
    requestConfirmation({ title: "更新新接入策略", detail: "该设置只影响新的配对和 Node enrollment，不会断开现有连接。", confirmLabel: "应用接入策略", run: () => client!.highRisk("PUT", "/v1/admin/security/admission", { controlPairingEnabled: pairing, nodeEnrollmentEnabled: enrollment, baseRevision: current.revision }) });
  };

  return <main className="admin-shell">
    <aside className="admin-nav" aria-label="Server 管理导航">
      <a className="admin-brand" href="/"><span className="brand-mark">枢</span><span><strong>远枢</strong><small>Server 管理</small></span></a>
      <nav>{(["overview", "nodes", "clients", "access", "security"] as Section[]).map((item) => <button key={item} className={section === item ? "active" : ""} onClick={() => setSection(item)}>{sectionLabel(item)}<span>{sectionCount(item, data)}</span></button>)}</nav>
      <div className="admin-nav-foot"><a href="/">返回工作台</a><button onClick={() => void client?.close().then(() => window.location.reload())}>退出管理会话</button></div>
    </aside>
    <section className="admin-main">
      <header className="admin-topbar"><div><h1>{sectionLabel(section)}</h1><p>{sectionDescription(section)}</p></div><div className="admin-live"><span className={`semantic-dot ${data.overview?.status === "ready" ? "ok" : "warn"}`} />{data.overview?.status === "ready" ? "Server 正常" : "需要检查"}</div></header>
      {message && <div className="admin-message" role="status">{message}<button onClick={() => setMessage("")} aria-label="关闭提示">关闭</button></div>}
      {section === "overview" && <Overview data={data} />}
      {section === "nodes" && <Nodes items={data.nodes} details={data.nodeDetails} onInspect={inspectNode} onRevoke={revokeNode} />}
      {section === "clients" && <Clients items={data.clients} onRevoke={revokeClient} />}
      {section === "access" && <Access requests={data.requests} leases={data.leases} config={data.config} onCancel={cancelRequest} onRelease={releaseLease} onAdmission={changeAdmission} />}
      {section === "security" && <Security data={data} onDownload={() => downloadDiagnostics(data.diagnostics ?? {})} />}
    </section>
    {confirmation && <ConfirmDialog value={confirmation} busy={busy} onCancel={() => !busy && setConfirmation(undefined)} onConfirm={() => void run(confirmation.run)} />}
  </main>;
}

function Overview({ data }: { data: AdminData }) {
  const overview = data.overview;
  if (!overview) return <AdminSkeleton />;
  const backup = overview.backup ?? { available: false, integrity: "unavailable" as const, operation: "local_cli_only" as const };
  const metrics = [
    ["在线 Node", overview.connections.nodes, `${overview.counts.activeNodes} 个 active`],
    ["在线控制端", overview.connections.controlClients, `${overview.counts.activeControlClients} 个 active`],
    ["活跃租约", overview.counts.activeLeases, "Thread 控制权"],
    ["待处理接入", overview.counts.pendingPairings + overview.counts.pendingEnrollments, "配对和 Node"],
  ];
  const certificate = overview.tls.expiryWarning ? machineStatus(overview.tls.expiryWarning) : undefined;
  const backupCode = !backup.available ? "backup_unavailable" : backup.integrity === "invalid" ? "backup_invalid" : "";
  const backupStatus = backupCode ? machineStatus(backupCode) : undefined;
  return <div className="admin-stack">{certificate && <StatusNotice value={certificate} />}{backupStatus && <StatusNotice value={backupStatus} />}<section className="metric-grid">{metrics.map(([label, value, detail]) => <article key={String(label)}><span>{label}</span><strong>{value}</strong><small>{detail}</small></article>)}</section><section className="admin-split"><article className="admin-block"><h2>运行状态</h2><dl><DataRow label="运行时间" value={formatDuration(overview.uptimeSeconds)} /><DataRow label="构建版本" value={overview.build.version || shortID(overview.build.revision) || "开发构建"} /><DataRow label="Go Runtime" value={overview.build.goVersion} /><DataRow label="SQLite schema" value={String(overview.database.schemaVersion)} /><DataRow label="数据库检查" value={overview.database.quickCheck} /><DataRow label="数据库大小" value={formatBytes(overview.database.sizeBytes)} /></dl></article><article className="admin-block"><h2>安全与恢复</h2><dl><DataRow label="TLS" value={overview.tls.configured ? "已启用" : "仅 loopback"} /><DataRow label="证书到期" value={formatDate(overview.tls.notAfter)} /><DataRow label="最近备份" value={backup.available ? formatDate(backup.lastBackupAt) : "尚无备份"} /><DataRow label="备份完整性" value={backup.integrity === "valid" ? "已验证" : backup.integrity === "invalid" ? "校验失败" : "不可用"} /><DataRow label="最近失败" value={`${overview.counts.recentFailures} 项`} /><DataRow label="未读通知" value={`${overview.counts.unreadNotifications} 项`} /></dl></article></section><RecentAudit items={data.audit.slice(0, 6)} /></div>;
}

function Nodes({ items, details, onInspect, onRevoke }: { items: AdminNode[]; details: Record<string, AdminNodeDetail>; onInspect: (id: string) => void; onRevoke: (item: AdminNode) => void }) { return <AdminList empty="还没有已注册 Node">{items.map((item) => <div className="node-detail-group" key={item.id}><article className="admin-row"><div><span className={`semantic-dot ${item.online ? "ok" : "quiet"}`} /><strong>{item.name}</strong><small>{item.os} / {item.version}</small></div><div className="row-meta"><span>{item.online ? machineStatus("online")!.title : machineStatus("offline")!.title}</span><span>最近连接 {formatDate(item.lastSeenAt)}</span></div><div className="row-actions"><button className="secondary-button" onClick={() => void onInspect(item.id)}>详情</button><button className="danger-button" disabled={item.status !== "active"} onClick={() => onRevoke(item)}>撤销</button></div></article>{details[item.id] && <NodeRuntimeDetail value={details[item.id]} />}</div>)}</AdminList>; }

function NodeRuntimeDetail({ value }: { value: AdminNodeDetail }) { const runtime = value.node.runtime; return <section className="node-runtime-detail"><DataRow label="Relay" value={runtime.relayStatus || "未报告"} /><DataRow label="本次连接" value={formatDate(runtime.connectedAt)} /><DataRow label="Runtime" value={runtime.runtimeStatus || "未报告"} /><DataRow label="恢复状态" value={runtime.recoveryStatus || "无"} /><DataRow label="工作区数量" value={String(runtime.workspaceCount ?? 0)} /><DataRow label="最近 frame" value={formatDate(runtime.lastFrameAt)} /><DataRow label="最近事件" value={formatDate(runtime.lastEventAt)} /><DataRow label="最近错误" value={runtime.lastErrorCode || "无"} /><DataRow label="关闭原因" value={runtime.lastCloseReason || "无"} /></section>; }
function Clients({ items, onRevoke }: { items: AdminControlClient[]; onRevoke: (item: AdminControlClient) => void }) { return <AdminList empty="还没有已配对控制端">{items.map((item) => <article className="admin-row" key={item.id}><div><span className={`semantic-dot ${item.online ? "ok" : "quiet"}`} /><strong>{item.name}{item.current ? "（当前）" : ""}</strong><small>{shortID(item.id)}</small></div><div className="row-meta"><span>{item.status === "active" ? (item.online ? "在线" : "未连接") : "已撤销"}</span><span>最近连接 {formatDate(item.lastSeenAt)}</span></div><button className="danger-button" disabled={item.status !== "active"} onClick={() => onRevoke(item)}>撤销</button></article>)}</AdminList>; }

function Access({ requests, leases, config, onCancel, onRelease, onAdmission }: { requests: AdminAccessRequest[]; leases: AdminLease[]; config?: AdminConfig; onCancel: (item: AdminAccessRequest) => void; onRelease: (item: AdminLease) => void; onAdmission: (pairing: boolean, enrollment: boolean) => void }) {
  const admission = config?.admission;
  return <div className="admin-stack"><section className="admin-block"><div className="block-heading"><div><h2>新接入策略</h2><p>关闭后只阻止新的 claim，不影响已认证连接。</p></div><span>revision {admission?.revision ?? "-"}</span></div><div className="switch-list"><SettingSwitch label="控制端配对" checked={admission?.controlPairingEnabled ?? false} disabled={!admission} onChange={(value) => onAdmission(value, admission?.nodeEnrollmentEnabled ?? false)} /><SettingSwitch label="Node enrollment" checked={admission?.nodeEnrollmentEnabled ?? false} disabled={!admission} onChange={(value) => onAdmission(admission?.controlPairingEnabled ?? false, value)} /></div></section><section className="admin-block"><h2>待处理接入</h2><AdminList empty="没有待处理申请">{requests.map((item) => <article className="admin-row compact" key={item.id}><div><strong>{item.name || (item.kind === "node" ? "新 Node" : "新控制端")}</strong><small>{item.kind === "node" ? `${item.os || "unknown"} / ${item.version || "unreported"}` : "控制端配对"}</small></div><div className="row-meta"><span>{item.status}</span><span>{formatDate(item.expiresAt)} 过期</span></div><button className="secondary-button" onClick={() => onCancel(item)}>取消</button></article>)}</AdminList></section><section className="admin-block"><h2>活跃租约</h2><AdminList empty="当前没有 Thread 控制租约">{leases.map((item) => <article className="admin-row compact" key={`${item.scope.nodeId}:${item.scope.workspaceId}:${item.scope.threadId}`}><div><strong>{shortID(item.scope.threadId)}</strong><small>Node {shortID(item.scope.nodeId)} / epoch {item.epoch}</small></div><div className="row-meta"><span>持有者 {shortID(item.holderClientId)}</span><span>{formatDate(item.expiresAt)} 过期</span></div><button className="secondary-button" onClick={() => onRelease(item)}>释放</button></article>)}</AdminList></section></div>;
}

function Security({ data, onDownload }: { data: AdminData; onDownload: () => void }) { const config = data.config; const backup = data.overview?.backup; return <div className="admin-stack"><section className="admin-split"><article className="admin-block"><div className="block-heading"><h2>脱敏 Server 配置</h2><span>只读</span></div><dl><DataRow label="部署模式" value={config?.deploymentMode || "未报告"} /><DataRow label="Listen" value={config?.listen || "未报告"} /><DataRow label="Public URL" value={config?.publicUrl || "loopback"} /><DataRow label="Web" value={config?.webEnabled ? "启用" : "关闭"} /><DataRow label="Admin" value={config?.adminEnabled ? "启用" : "关闭"} /><DataRow label="TLS" value={config?.tls.configured ? "已配置" : "未配置"} /><DataRow label="证书来源" value={config?.tls.provider || "无"} /></dl>{config?.tls.trustUrl && <p><a href={config.tls.trustUrl}>打开根 CA 安装说明</a> · <a href={config.tls.caDownloadUrl}>下载公开根证书</a></p>}</article><article className="admin-block"><div className="block-heading"><div><h2>诊断摘要</h2><p>不包含凭据、任务正文或绝对路径。</p></div><button className="secondary-button" onClick={onDownload}>下载 JSON</button></div><dl><DataRow label="配置 revision" value={shortID(config?.configRevision)} /><DataRow label="允许 Origin" value={String(config?.allowedControlOrigins.length ?? 0)} /><DataRow label="TLS SAN" value={config?.tls.san?.join(", ") || "无"} /><DataRow label="证书指纹" value={shortID(config?.tls.fingerprint)} /><DataRow label="根 CA 指纹" value={shortID(config?.tls.caFingerprint)} /><DataRow label="证书到期" value={formatDate(config?.tls.notAfter)} /><DataRow label="下次续期" value={formatDate(config?.tls.nextRenewal)} /><DataRow label="CA 恢复包" value={config?.tls.caBackupAt ? `最近导出 ${formatDate(config.tls.caBackupAt)}` : "尚未记录；请在 Server 本机导出"} /><DataRow label="最近证书错误" value={config?.tls.lastErrorCode || "无"} /><DataRow label="最近备份" value={backup?.available ? `${formatDate(backup.lastBackupAt)} · ${formatBytes(backup.sizeBytes ?? 0)}` : "请在 Server 本机运行 backup"} /></dl></article></section><RecentAudit items={data.audit} /></div>; }

function RecentAudit({ items }: { items: AdminAudit[] }) { return <section className="admin-block"><h2>安全审计</h2><AdminList empty="还没有管理操作记录">{items.map((item) => <article className="audit-row" key={item.id}><time>{formatDate(item.createdAt)}</time><strong>{auditLabel(item.action)}</strong><span>{item.resourceType} / {shortID(item.resourceRef)}</span><em className={item.result}>{item.result === "succeeded" ? "成功" : item.errorCode || "拒绝"}</em></article>)}</AdminList></section>; }

function ConfirmDialog({ value, busy, onCancel, onConfirm }: { value: Confirmation; busy: boolean; onCancel: () => void; onConfirm: () => void }) {
  const [typed, setTyped] = useState("");
  const dialogRef = useRef<HTMLElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const allowed = !value.requiredText || typed === value.requiredText;
  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : undefined;
    (inputRef.current ?? cancelRef.current)?.focus();
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !busy) { event.preventDefault(); onCancel(); return; }
      if (event.key !== "Tab" || !dialogRef.current) return;
      const focusable = [...dialogRef.current.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled])')];
      if (!focusable.length) return;
      const first = focusable[0]; const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => { document.removeEventListener("keydown", onKeyDown); previous?.focus(); };
  }, [busy, onCancel]);
  return <div className="dialog-layer" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget && !busy) onCancel(); }}><section ref={dialogRef} className="confirm-dialog" role="alertdialog" aria-modal="true" aria-labelledby="confirm-title" aria-describedby="confirm-detail"><h2 id="confirm-title">{value.title}</h2><p id="confirm-detail">{value.detail}</p>{value.requiredText && <label>输入 <strong>{value.requiredText}</strong> 继续<input ref={inputRef} value={typed} onChange={(event) => setTyped(event.target.value)} /></label>}<div><button ref={cancelRef} className="secondary-button" disabled={busy} onClick={onCancel}>取消</button><button className="danger-button solid" disabled={busy || !allowed} onClick={onConfirm}>{busy ? "处理中" : value.confirmLabel}</button></div></section></div>;
}
function AdminGate({ title, detail, action }: { title: string; detail: string; action?: ReactNode }) { return <main className="admin-gate"><span className="brand-mark">枢</span><h1>{title}</h1><p>{detail}</p>{action}</main>; }
function AdminSkeleton() { return <div className="admin-skeleton" aria-label="正在读取管理状态"><i /><i /><i /><i /></div>; }
function AdminList({ children, empty }: { children: ReactNode; empty: string }) { return <div className="admin-list">{Children.count(children) ? children : <p className="admin-empty">{empty}</p>}</div>; }
function DataRow({ label, value }: { label: string; value: string }) { return <div><dt>{label}</dt><dd>{value}</dd></div>; }
function SettingSwitch({ label, checked, disabled, onChange }: { label: string; checked: boolean; disabled: boolean; onChange: (value: boolean) => void }) { return <label className="setting-switch"><span>{label}</span><input type="checkbox" checked={checked} disabled={disabled} onChange={(event) => onChange(event.target.checked)} /><i aria-hidden="true" /></label>; }
function StatusNotice({ value }: { value: NonNullable<ReturnType<typeof machineStatus>> }) { return <section className={`admin-status-notice ${value.severity}`} role="status"><strong>{value.title}</strong><span>{value.description}</span><small>{value.action}</small></section>; }
function settledValue<T>(value: PromiseSettledResult<unknown>): T | undefined { return value.status === "fulfilled" ? value.value as T : undefined; }

function sectionLabel(section: Section): string { return ({ overview: "概览", nodes: "Node", clients: "控制端", access: "接入与租约", security: "安全与诊断" })[section]; }
function sectionDescription(section: Section): string { return ({ overview: "Server、连接和数据存储的当前状态", nodes: "管理已注册电脑和连接凭据", clients: "查看和撤销浏览器控制端身份", access: "控制新接入并处理过期请求和租约", security: "检查脱敏配置、诊断和管理审计" })[section]; }
function sectionCount(section: Section, data: AdminData): string { if (section === "nodes") return String(data.nodes.length); if (section === "clients") return String(data.clients.length); if (section === "access") return String(data.requests.length + data.leases.length); if (section === "security") return String(data.audit.length); return ""; }
function shortID(value?: string): string { if (!value) return "-"; return value.length > 18 ? `${value.slice(0, 8)}...${value.slice(-6)}` : value; }
function formatDate(value?: string): string { if (!value) return "从未"; const date = new Date(value); return Number.isNaN(date.getTime()) ? "未知" : date.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }); }
function formatDuration(seconds: number): string { const hours = Math.floor(seconds / 3600); const minutes = Math.floor((seconds % 3600) / 60); return hours ? `${hours} 小时 ${minutes} 分钟` : `${minutes} 分钟`; }
function formatBytes(bytes: number): string { if (bytes < 1024 * 1024) return `${Math.max(0, Math.round(bytes / 1024))} KiB`; return `${(bytes / 1024 / 1024).toFixed(1)} MiB`; }
function auditLabel(value: string): string { return ({ "node.revoke": "撤销 Node", "control_client.revoke": "撤销控制端", "pairing.cancel": "取消配对", "node_enrollment.cancel": "取消 Node 接入", "lease.release": "释放租约", "security.admission.update": "更新接入策略" } as Record<string, string>)[value] ?? value; }
function downloadDiagnostics(value: Record<string, unknown>) { const blob = new Blob([JSON.stringify(value, null, 2)], { type: "application/json" }); const url = URL.createObjectURL(blob); const link = document.createElement("a"); link.href = url; link.download = "yuanshu-server-diagnostics.json"; link.click(); URL.revokeObjectURL(url); }
