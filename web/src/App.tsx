import { FormEvent, useEffect, useMemo, useRef, useState } from "react";

import { ControlClient, type ControlClientState } from "./relay/control-client";
import { IndexedDBControlStorage, type ControlStorage, type StoredControlIdentity } from "./relay/storage";
import { DataProjection, threadKey, turnKey, type ProjectionState, type ThreadItemProjection } from "./state/projection";

type BootState =
  | { status: "loading" }
  | { status: "pairing"; reason?: string }
  | { status: "config"; identity: StoredControlIdentity; storage: ControlStorage }
  | { status: "ready"; client: ControlClient; projection: DataProjection };

const relayURL = import.meta.env.VITE_YUANSHU_RELAY_URL?.trim() ?? "";
const pairingURL = import.meta.env.VITE_YUANSHU_PAIRING_URL?.trim() || "/pair";

export function App() {
  const [boot, setBoot] = useState<BootState>({ status: "loading" });
  const [connectionState, setConnectionState] = useState<ControlClientState>("idle");
  const [revision, setRevision] = useState(0);
  const [selectedNodeId, setSelectedNodeId] = useState("");
  const [selectedWorkspaceId, setSelectedWorkspaceId] = useState("");
  const [selectedThreadId, setSelectedThreadId] = useState("");
  const [input, setInput] = useState("");
  const [composeMode, setComposeMode] = useState<"new" | "turn">("new");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const contextRestored = useRef(false);
  const loadedWorkspaces = useRef(new Set<string>());
  const loadedThreads = useRef(new Set<string>());

  useEffect(() => {
    let disposed = false;
    const bootstrap = async () => {
      try {
        const storage = new IndexedDBControlStorage();
        const identity = await storage.getActiveIdentity();
        if (!identity) {
          if (!disposed) setBoot({ status: "pairing", reason: "尚未找到控制端身份" });
          return;
        }
        if (!relayURL) {
          if (!disposed) setBoot({ status: "config", identity, storage });
          return;
        }
        const projection = new DataProjection();
        const client = new ControlClient({
          url: relayURL,
          identity,
          storage,
          onState: (state) => { if (!disposed) setConnectionState(state); },
          onNode: (node) => {
            projection.registerNode({ ...node, online: node.online ?? true });
            if (!disposed) setRevision((value) => value + 1);
          },
          onEvent: (event) => {
            projection.apply(event);
            if (!disposed) setRevision((value) => value + 1);
          },
          onControlAction: (action) => {
            projection.applyControlAction(action);
            if (!disposed) setRevision((value) => value + 1);
          },
        });
        await client.ready;
        for (const node of client.listNodes()) projection.registerNode(node);
        if (disposed) {
          client.close();
          return;
        }
        setBoot({ status: "ready", client, projection });
        const saved = readContext();
        const firstNode = client.listNodes().find((node) => node.nodeId === saved.nodeId) ?? client.listNodes()[0];
        if (firstNode) setSelectedNodeId(firstNode.nodeId);
        contextRestored.current = true;
        client.connect();
      } catch (error) {
        if (!disposed) setBoot({ status: "pairing", reason: error instanceof Error ? error.message : "浏览器安全存储不可用" });
      }
    };
    void bootstrap();
    return () => { disposed = true; };
  }, []);

  const state = boot.status === "ready" ? boot.projection.state : undefined;
  const nodes = useMemo(() => state ? Object.values(state.nodes) : [], [state, revision]);
  const selectedNode = nodes.find((node) => node.nodeId === selectedNodeId);
  const workspaces = useMemo(() => state ? Object.values(state.workspaces).filter((workspace) => workspace.nodeId === selectedNodeId) : [], [state, selectedNodeId, revision]);
  const threads = useMemo(() => state ? Object.values(state.threads)
    .filter((thread) => thread.nodeId === selectedNodeId && thread.workspaceId === selectedWorkspaceId)
    .sort((left, right) => (right.updatedAt ?? "").localeCompare(left.updatedAt ?? "")) : [], [state, selectedNodeId, selectedWorkspaceId, revision]);
  const selectedThread = state && selectedThreadId ? state.threads[threadKey(selectedNodeId, selectedWorkspaceId, selectedThreadId)] : undefined;
  const turns = useMemo(() => selectedThread && state ? selectedThread.turnIds
    .map((turnId) => state.turns[turnKey(selectedNodeId, selectedWorkspaceId, selectedThreadId, turnId)])
    .filter((turn): turn is NonNullable<typeof turn> => Boolean(turn)) : [], [state, selectedThread, selectedNodeId, selectedWorkspaceId, selectedThreadId, revision]);
  const activeTurn = [...turns].reverse().find((turn) => ["running", "inProgress", "active"].includes(turn.status ?? ""));
  const approvals = state ? Object.values(state.approvals).filter((approval) => approval.nodeId === selectedNodeId && approval.threadId === selectedThreadId && approval.status === "pending") : [];
  const action = state && selectedThreadId ? Object.values(state.actions).filter((item) => item.nodeId === selectedNodeId && item.threadId === selectedThreadId).sort((left, right) => right.updatedAt.localeCompare(left.updatedAt))[0] : undefined;

  useEffect(() => {
    if (boot.status !== "ready" || !selectedNodeId) return;
    const key = selectedNodeId;
    if (loadedWorkspaces.current.has(key) || connectionState !== "connected") return;
    loadedWorkspaces.current.add(key);
    void request(boot.client, "device.sync", {}, { nodeId: selectedNodeId }).catch(() => undefined);
    void request(boot.client, "workspace.list", { limit: 100 }, { nodeId: selectedNodeId }).catch(() => undefined);
  }, [boot.status, selectedNodeId, connectionState]);

  useEffect(() => {
    if (!selectedNodeId || !workspaces.length) return;
    const saved = readContext();
    const next = workspaces.find((workspace) => workspace.workspaceId === saved.workspaceId)?.workspaceId ?? workspaces[0].workspaceId;
    if (selectedWorkspaceId !== next) setSelectedWorkspaceId(next);
  }, [selectedNodeId, workspaces, selectedWorkspaceId]);

  useEffect(() => {
    if (boot.status !== "ready" || !selectedNodeId || !selectedWorkspaceId || connectionState !== "connected") return;
    const key = `${selectedNodeId}\u001f${selectedWorkspaceId}`;
    if (loadedThreads.current.has(key)) return;
    loadedThreads.current.add(key);
    void request(boot.client, "thread.list", { limit: 100 }, { nodeId: selectedNodeId, workspaceId: selectedWorkspaceId }).catch(() => {
      loadedThreads.current.delete(key);
    });
  }, [boot.status, selectedNodeId, selectedWorkspaceId, connectionState]);

  useEffect(() => {
    if (!threads.length) return;
    const saved = readContext();
    const next = threads.find((thread) => thread.threadId === saved.threadId)?.threadId ?? threads[0].threadId;
    if (selectedThreadId !== next) {
      setSelectedThreadId(next);
      setComposeMode("turn");
    }
  }, [threads, selectedThreadId]);

  useEffect(() => {
    if (boot.status !== "ready" || !selectedNodeId || !selectedWorkspaceId || !selectedThreadId || connectionState !== "connected") return;
    void request(boot.client, "thread.read", { includeTurns: true }, { nodeId: selectedNodeId, workspaceId: selectedWorkspaceId, threadId: selectedThreadId }).catch(() => undefined);
    writeContext({ nodeId: selectedNodeId, workspaceId: selectedWorkspaceId, threadId: selectedThreadId });
  }, [boot.status, selectedNodeId, selectedWorkspaceId, selectedThreadId, connectionState]);

  if (boot.status === "loading") return <LoadingScreen />;
  if (boot.status === "pairing" || boot.status === "config") return <PairingScreen pairingURL={pairingURL} reason={boot.status === "pairing" ? boot.reason : undefined} configured={boot.status === "config"} />;
  if (!state || !contextRestored.current) return <LoadingScreen />;

  const runControl = async (type: "thread.start" | "thread.resume" | "turn.start" | "turn.interrupt", payload: Record<string, unknown>, target: { workspaceId?: string; threadId?: string; turnId?: string }) => {
    if (boot.status !== "ready") return;
    setBusy(true);
    setMessage("");
    try {
      await boot.client.request(type, payload, { nodeId: selectedNodeId, ...target });
      setMessage(type === "turn.interrupt" ? "已发送停止请求，等待源头确认" : "已发送，正在等待 Codex 确认");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "操作失败，结果未知");
    } finally {
      setBusy(false);
    }
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const value = input.trim();
    if (!value || !selectedWorkspaceId) return;
    setInput("");
    if (composeMode === "new" || !selectedThreadId) {
      await runControl("thread.start", { input: value }, { workspaceId: selectedWorkspaceId });
      setComposeMode("turn");
      return;
    }
    await runControl("turn.start", { input: value }, { workspaceId: selectedWorkspaceId, threadId: selectedThreadId });
  };

  const chooseThread = (threadId: string) => {
    setSelectedThreadId(threadId);
    setComposeMode("turn");
    writeContext({ nodeId: selectedNodeId, workspaceId: selectedWorkspaceId, threadId });
  };

  return (
    <main className="workbench-shell">
      <header className="topbar">
        <div className="brand-lockup"><span className="brand-mark">枢</span><div><strong>远枢</strong><span>Yuanshu workspace</span></div></div>
        <div className={`connection-pill ${connectionState}`}><span className="status-dot" />{connectionLabel(connectionState)}</div>
        <a className="pair-link" href={pairingURL}>配对新设备</a>
      </header>

      <section className="node-strip" aria-label="选择设备">
        <div className="section-kicker">你的节点</div>
        <div className="node-list">
          {nodes.map((node) => <button className={`node-chip ${node.nodeId === selectedNodeId ? "selected" : ""}`} key={node.nodeId} onClick={() => { setSelectedNodeId(node.nodeId); setSelectedWorkspaceId(""); setSelectedThreadId(""); }}>
            <span className={`node-avatar ${node.online ? "online" : "offline"}`}>{(node.name ?? node.nodeId).slice(0, 1).toUpperCase()}</span>
            <span><b>{node.name ?? "未命名节点"}</b><small>{node.online ? node.runtimeStatus ?? "在线" : "离线"}</small></span>
          </button>)}
          {!nodes.length && <div className="empty-inline">配对后会在这里显示可控制的电脑</div>}
        </div>
      </section>

      <div className="workspace-layout">
        <aside className="sidebar">
          <div className="sidebar-heading"><div><span className="section-kicker">工作区</span><h2>{selectedNode?.name ?? "选择一个节点"}</h2></div><span className="count-badge">{workspaces.length}</span></div>
          <div className="workspace-list">
            {workspaces.map((workspace) => <button className={`workspace-row ${workspace.workspaceId === selectedWorkspaceId ? "selected" : ""}`} key={workspace.key} onClick={() => { setSelectedWorkspaceId(workspace.workspaceId); setSelectedThreadId(""); }}><span className="workspace-icon">⌂</span><span><b>{workspace.name ?? workspace.workspaceId}</b><small>{workspace.permissionProfile === "workspaceWrite" ? "可写工作区" : "只读工作区"}</small></span></button>)}
            {!workspaces.length && <div className="empty-state small">正在读取工作区…</div>}
          </div>
          <div className="sidebar-foot">内容保留在节点本机<br />远枢只中继结构化事件</div>
        </aside>

        <section className="thread-column">
          <div className="column-heading"><div><span className="section-kicker">任务上下文</span><h2>{selectedWorkspaceId ? "最近 Thread" : "先选择工作区"}</h2></div><button className="icon-button" title="新建 Thread" onClick={() => { setSelectedThreadId(""); setComposeMode("new"); setMessage(""); }}>＋</button></div>
          <div className="thread-list">
            {threads.map((thread) => <button className={`thread-row ${thread.threadId === selectedThreadId ? "selected" : ""}`} key={thread.key} onClick={() => chooseThread(thread.threadId)}>
              <span className={`thread-state ${thread.status ?? "unknown"}`} />
              <span className="thread-copy"><b>{thread.title || thread.preview || `Thread ${thread.threadId.slice(0, 8)}`}</b><small>{thread.preview && thread.title ? thread.preview : formatTime(thread.updatedAt)} </small></span>
              <span className="thread-meta">{thread.pendingApprovals ? "审批" : thread.historyState === "partial" || thread.recovery === "history_gap" ? "缺口" : statusLabel(thread.status)}</span>
            </button>)}
            {!threads.length && <div className="empty-state"><span className="empty-glyph">⌁</span><b>还没有任务</b><p>从下方输入第一条指令，远枢会在当前工作区创建一个 Codex Thread。</p></div>}
          </div>
        </section>

        <section className="detail-column">
          <div className="detail-heading">
            <div><span className="section-kicker">Thread 详情</span><h1>{selectedThread?.title || selectedThread?.preview || (selectedThreadId ? `Thread ${selectedThreadId.slice(0, 12)}` : "开始一个新任务")}</h1>{selectedThread?.preview && selectedThread.title && <p>{selectedThread.preview}</p>}</div>
            <div className="detail-actions">{selectedThread && !activeTurn && <button className="secondary-button" disabled={busy || connectionState !== "connected"} onClick={() => void runControl("thread.resume", {}, { workspaceId: selectedWorkspaceId, threadId: selectedThreadId })}>恢复 Thread</button>}</div>
          </div>
          {selectedThread && (selectedThread.recovery === "history_gap" || selectedThread.historyState === "partial" || selectedThread.historyState === "unavailable") && <div className="notice-banner"><span>!</span><div><b>{selectedThread.recovery === "history_gap" ? "历史存在缺口" : "历史内容不完整"}</b><small>源头事件或当前 Codex 版本无法提供全部历史，下面内容可能是部分视图。</small></div></div>}
          {selectedNode?.runtimeStatus === "unavailable" && <div className="notice-banner warning"><span>×</span><div><b>Codex app-server 暂不可用</b><small>Node 仍在保留本地状态，恢复连接后会继续同步。</small></div></div>}
          <div className="timeline">
            {turns.map((turn) => <TurnCard key={turn.key} turn={turn} />)}
            {!turns.length && <div className="detail-empty"><span className="signal-line" /><b>{selectedThreadId ? "正在读取 Thread 历史" : "选择一个 Thread，或直接开始新任务"}</b><p>源头的消息、命令、工具活动和文件变化会按时间顺序出现在这里。</p></div>}
          </div>
          {approvals.length > 0 && <div className="read-only-panel"><div className="panel-title"><span>审批请求</span><em>只读</em></div>{approvals.map((approval) => <div className="approval-row" key={approval.key}><span className="approval-icon">!</span><div><b>{approval.kind ?? "高风险操作"}</b><p>{approval.summary ?? "Codex 正在等待本机审批"}</p></div><span className="muted-label">待处理</span></div>)}</div>}
          {action && <div className={`action-feedback ${action.state}`}><span className="status-dot" /><span>{actionLabel(action.state)}</span><small>{action.type}</small></div>}
          {message && <div className="inline-message">{message}</div>}
          <form className="composer" onSubmit={(event) => void submit(event)}>
            <div className="composer-mode"><button type="button" className={composeMode === "new" ? "active" : ""} onClick={() => { setComposeMode("new"); setSelectedThreadId(""); }}>新 Thread</button><button type="button" className={composeMode === "turn" ? "active" : ""} disabled={!selectedThreadId} onClick={() => setComposeMode("turn")}>追加 Turn</button></div>
            <textarea value={input} onChange={(event) => setInput(event.target.value)} placeholder={composeMode === "new" ? "告诉 Codex 你想在这个工作区完成什么…" : "继续这个上下文…"} disabled={busy || connectionState !== "connected" || !selectedWorkspaceId} rows={3} />
            <div className="composer-footer"><span>{selectedWorkspaceId ? `${selectedNode?.name ?? "节点"} · ${workspaces.find((workspace) => workspace.workspaceId === selectedWorkspaceId)?.name ?? "工作区"}` : "选择工作区后开始"}</span><div>{activeTurn && <button type="button" className="stop-button" disabled={busy} onClick={() => void runControl("turn.interrupt", {}, { workspaceId: selectedWorkspaceId, threadId: selectedThreadId, turnId: activeTurn.turnId })}>停止 Turn</button>}<button className="send-button" disabled={busy || !input.trim() || connectionState !== "connected" || !selectedWorkspaceId}>{busy ? "发送中…" : composeMode === "new" ? "开始任务" : "发送"}<span>↗</span></button></div></div>
          </form>
        </section>
      </div>
    </main>
  );
}

function TurnCard({ turn }: { turn: ProjectionState["turns"][string] }) {
  return <article className={`turn-card ${turn.status ?? ""}`}><header className="turn-header"><span className="turn-index">TURN</span><b>{statusLabel(turn.status)}</b><time>{formatTime(turn.updatedAt)}</time></header><div className="item-stack">{turn.items.map((item) => <ItemCard item={item} key={item.id} />)}{!turn.items.length && <div className="turn-empty">源头尚未发送可展示的内容</div>}</div></article>;
}

function ItemCard({ item }: { item: ThreadItemProjection }) {
  if (item.kind === "agent_message" || item.kind === "user_message") return <div className={`message-block ${item.kind}`}><span className="item-label">{item.kind === "user_message" ? "你" : "CODEX"}</span><p>{item.text || "（空消息）"}</p></div>;
  if (item.kind === "command" || item.kind === "command_output") return <details className="activity-card command-card" open={item.kind === "command"}><summary><span className="activity-icon">$</span><span><b>{item.command || "命令输出"}</b><small>{item.status ?? "执行中"}{item.exitCode !== undefined ? ` · exit ${item.exitCode}` : ""}</small></span></summary>{item.output && <pre>{item.output}</pre>}</details>;
  if (item.kind === "tool") return <div className="activity-card"><span className="activity-icon">◇</span><span><b>{item.toolName || "工具调用"}</b><small>{item.status ?? "已记录"}</small></span></div>;
  if (item.kind === "file_change" || item.kind === "diff") return <details className="activity-card diff-card"><summary><span className="activity-icon">{item.kind === "diff" ? "±" : "↳"}</span><span><b>{item.path || "文件变更"}</b><small>{item.changeType || "Diff 已更新"}</small></span></summary>{item.diff && <pre>{item.diff}</pre>}</details>;
  return <div className="activity-card error-card"><span className="activity-icon">!</span><span><b>{item.errorCode || "未识别活动"}</b><small>{item.errorMessage || "Codex 返回了未支持的历史项，正文未被转发。"}</small></span></div>;
}

function LoadingScreen() { return <main className="loading-screen"><span className="brand-mark">枢</span><h1>正在恢复你的工作区</h1><p>从浏览器安全存储读取控制端身份和本地上下文…</p><div className="loading-line" /></main>; }

function PairingScreen({ pairingURL, reason, configured }: { pairingURL: string; reason?: string; configured: boolean }) {
  return <main className="pairing-screen"><div className="pairing-card"><span className="brand-mark">枢</span><span className="section-kicker">个人控制端</span><h1>{configured ? "Web 还没有连接地址" : "连接你的 Codex 工作区"}</h1><p>{configured ? "请在独立 Web 构建中配置 VITE_YUANSHU_RELAY_URL，再重新加载页面。" : "先从办公室或家庭电脑生成配对链接。控制端身份只保存在本浏览器的 IndexedDB 中。"}</p>{reason && <small className="error-copy">{reason}</small>}{!configured && <a className="primary-link" href={pairingURL}>打开配对页 <span>↗</span></a>}<div className="trust-note"><span>✓</span> 私钥不进入 URL、日志或 Server</div></div></main>;
}

async function request(client: ControlClient, type: "device.sync" | "workspace.list" | "thread.list" | "thread.read", payload: Record<string, unknown>, target: { nodeId: string; workspaceId?: string; threadId?: string }) { return client.request(type, payload, target); }

function connectionLabel(state: ControlClientState): string { return ({ idle: "未连接", connecting: "连接中", authenticating: "安全认证", connected: "已连接", reconnecting: "正在重连", paused: "已暂停", closed: "已关闭", reauth_required: "需要重新配对" })[state]; }
function statusLabel(status?: string): string { return ({ running: "执行中", active: "执行中", inProgress: "执行中", completed: "已完成", failed: "失败", interrupted: "已停止", idle: "待继续", waiting_approval: "等待审批", uncertain: "待确认", ambiguous: "结果不确定", unavailable: "运行时不可用" }[status ?? ""] ?? "待同步"); }
function actionLabel(state: string): string { return ({ sent: "已发送", confirmed: "源头已确认", rejected: "已拒绝", ambiguous: "结果不确定", unknown: "结果未知", offline: "节点离线" }[state] ?? state); }
function formatTime(value?: string): string { if (!value) return "刚刚"; const date = new Date(value); if (Number.isNaN(date.getTime())) return "刚刚"; return date.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }); }
function readContext(): { nodeId?: string; workspaceId?: string; threadId?: string } { try { const raw = sessionStorage.getItem("yuanshu-workbench-context"); return raw ? JSON.parse(raw) as { nodeId?: string; workspaceId?: string; threadId?: string } : {}; } catch { return {}; } }
function writeContext(value: { nodeId: string; workspaceId: string; threadId: string }): void { try { sessionStorage.setItem("yuanshu-workbench-context", JSON.stringify(value)); } catch { /* browser storage is optional */ } }
