import { FormEvent, useEffect, useMemo, useRef, useState } from "react";

import { ControlClient, type ControlClientState, type LeaseScope, type LeaseState } from "./relay/control-client";
import { IndexedDBControlStorage, type ControlStorage, type StoredControlIdentity, type StoredRuntimeSettings } from "./relay/storage";
import { loadRuntimeSettings, normalizeRuntimeSettings } from "./relay/runtime-config";
import { RELAY_SUBPROTOCOL } from "./relay/session";
import { DataProjection, threadKey, turnKey, type ProjectionState, type ThreadItemProjection } from "./state/projection";

type BootState =
  | { status: "loading" }
  | { status: "pairing"; reason?: string; storage?: ControlStorage; settings: StoredRuntimeSettings }
  | { status: "config"; identity: StoredControlIdentity; storage: ControlStorage; settings: StoredRuntimeSettings }
  | { status: "ready"; client: ControlClient; projection: DataProjection; storage: ControlStorage; settings: StoredRuntimeSettings };

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
  const [settingsOpen, setSettingsOpen] = useState(false);
  const contextRestored = useRef(false);
  const loadedWorkspaces = useRef(new Set<string>());
  const loadedThreads = useRef(new Set<string>());
  const loadedNotifications = useRef(new Set<string>());
  const readNotifications = useRef(new Set<string>());

  useEffect(() => {
    let disposed = false;
    const bootstrap = async () => {
      let storage: ControlStorage | undefined;
      let settings: StoredRuntimeSettings = { relayUrl: "", pairingUrl: "/pair" };
      try {
        const currentStorage = new IndexedDBControlStorage();
        storage = currentStorage;
        const identity = await currentStorage.getActiveIdentity();
        settings = await loadRuntimeSettings(currentStorage);
        if (!identity) {
          if (!disposed) setBoot({ status: "pairing", reason: "尚未找到控制端身份", storage, settings });
          return;
        }
        if (!settings.relayUrl) {
          if (!disposed) setBoot({ status: "config", identity, storage, settings });
          return;
        }
        const projection = new DataProjection();
        const client = new ControlClient({
          url: settings.relayUrl,
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
          onControlResult: (event) => {
            projection.applyServerControlResult(event);
            if (!disposed) setRevision((value) => value + 1);
          },
          onLease: () => { if (!disposed) setRevision((value) => value + 1); },
        });
        await client.ready;
        for (const node of client.listNodes()) projection.registerNode(node);
        if (disposed) {
          client.close();
          return;
        }
        setBoot({ status: "ready", client, projection, storage, settings });
        const saved = readContext();
        const firstNode = client.listNodes().find((node) => node.nodeId === saved.nodeId) ?? client.listNodes()[0];
        if (firstNode) setSelectedNodeId(firstNode.nodeId);
        contextRestored.current = true;
        client.connect();
      } catch (error) {
        if (!disposed) setBoot({ status: "pairing", reason: error instanceof Error ? error.message : "浏览器安全存储不可用", storage, settings });
      }
    };
    void bootstrap();
    return () => { disposed = true; };
  }, []);

  const state = boot.status === "ready" ? boot.projection.state : undefined;
  const nodes = useMemo(() => state ? Object.values(state.nodes) : [], [state, revision]);
  const selectedNode = nodes.find((node) => node.nodeId === selectedNodeId);
  const notifications = state ? Object.values(state.notifications).sort((left, right) => right.createdAt.localeCompare(left.createdAt)) : [];
  const unreadNotifications = notifications.filter((item) => !item.read);
  const workspaces = useMemo(() => state ? Object.values(state.workspaces).filter((workspace) => workspace.nodeId === selectedNodeId) : [], [state, selectedNodeId, revision]);
  const threads = useMemo(() => state ? Object.values(state.threads)
    .filter((thread) => thread.nodeId === selectedNodeId && thread.workspaceId === selectedWorkspaceId)
    .sort((left, right) => (right.updatedAt ?? "").localeCompare(left.updatedAt ?? "")) : [], [state, selectedNodeId, selectedWorkspaceId, revision]);
  const selectedThread = state && selectedThreadId ? state.threads[threadKey(selectedNodeId, selectedWorkspaceId, selectedThreadId)] : undefined;
  const leaseScope: LeaseScope | undefined = selectedThreadId && selectedNodeId && selectedWorkspaceId ? { nodeId: selectedNodeId, workspaceId: selectedWorkspaceId, threadId: selectedThreadId } : undefined;
  const lease = boot.status === "ready" && leaseScope ? boot.client.getLease(leaseScope) : { state: "none" as const, epoch: 0 };
  const leaseHeld = lease.state === "held" && !!leaseScope && boot.status === "ready" && boot.client.canMutate(leaseScope, "turn.start");
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
    if (boot.status !== "ready" || !selectedThreadId || connectionState !== "connected") return;
    for (const notification of notifications.filter((item) => item.threadId === selectedThreadId && !item.read && !readNotifications.current.has(item.id))) {
      readNotifications.current.add(notification.id);
      boot.projection.markNotificationRead(notification.id);
      void boot.client.request("notifications.read", { notificationId: notification.id }, { nodeId: notification.nodeId }).catch(() => {
        readNotifications.current.delete(notification.id);
        boot.projection.markNotificationRead(notification.id);
      });
    }
  }, [boot.status, selectedThreadId, connectionState, notifications, revision]);

  useEffect(() => {
    if (boot.status !== "ready" || !selectedNodeId || connectionState !== "connected" || loadedNotifications.current.has(selectedNodeId)) return;
    loadedNotifications.current.add(selectedNodeId);
    void boot.client.request("notifications.list", { limit: 50 }, { nodeId: selectedNodeId }).catch(() => loadedNotifications.current.delete(selectedNodeId));
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
    boot.client.registerLeaseScope({ nodeId: selectedNodeId, workspaceId: selectedWorkspaceId, threadId: selectedThreadId });
    void request(boot.client, "thread.read", { includeTurns: true }, { nodeId: selectedNodeId, workspaceId: selectedWorkspaceId, threadId: selectedThreadId }).catch(() => undefined);
    writeContext({ nodeId: selectedNodeId, workspaceId: selectedWorkspaceId, threadId: selectedThreadId });
  }, [boot.status, selectedNodeId, selectedWorkspaceId, selectedThreadId, connectionState]);

  if (boot.status === "loading") return <LoadingScreen />;
  if (boot.status === "pairing" || boot.status === "config") return <PairingScreen pairingURL={boot.settings.pairingUrl} settings={boot.settings} storage={boot.storage} reason={boot.status === "pairing" ? boot.reason : undefined} configured={boot.status === "config"} />;
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

  const changeLease = async (force: boolean) => {
    if (boot.status !== "ready" || !leaseScope) return;
    if (force && !window.confirm("当前 Thread 已被其他控制端占用。确认接管后，对方会立即变为只读。")) return;
    try {
      const next = await boot.client.acquireLease(leaseScope, { force, expectedEpoch: lease.epoch });
      setMessage(next.state === "held" ? (force ? "已接管 Thread 控制权" : "已获得 Thread 控制权") : "Thread 仍由其他控制端控制");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "获取控制权失败");
    }
  };

  const releaseCurrentLease = async () => {
    if (boot.status !== "ready" || !leaseScope) return;
    try {
      await boot.client.releaseLease(leaseScope);
      setMessage("已释放 Thread 控制权");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "释放控制权失败");
    }
  };

  const resolveApproval = async (approval: (typeof approvals)[number], decision: "accept" | "decline") => {
    if (boot.status !== "ready" || !leaseScope || !approval.operationDigest) {
      setMessage("审批缺少可验证摘要，已保持只读");
      return;
    }
    const highRisk = !approval.kind || /command|file|write|delete|shell/i.test(approval.kind);
    if (!window.confirm(highRisk ? "这是高风险审批。确认查看并准备发送决定？" : "确认发送审批决定？")) return;
    if (highRisk && !window.confirm("第二次确认：这项操作可能修改文件或执行命令，继续发送吗？")) return;
    try {
      await boot.client.request("approval.resolve", { approvalId: approval.approvalId, decision, operationDigest: approval.operationDigest }, { nodeId: selectedNodeId, workspaceId: selectedWorkspaceId, threadId: selectedThreadId, turnId: approval.turnId, itemId: approval.itemId });
      setMessage(decision === "accept" ? "已发送批准，等待 Codex 确认" : "已发送拒绝，等待 Codex 确认");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "审批结果未知");
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
    if (!leaseHeld) {
      setMessage("追加 Turn 需要先获得这个 Thread 的控制权");
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
        <div className={`connection-pill ${connectionState}`}><span className="status-dot" />{connectionLabel(connectionState)}{unreadNotifications.length > 0 && <span className="notification-count">{unreadNotifications.length}</span>}</div>
      <div className="topbar-actions"><a className="pair-link" href={boot.settings.pairingUrl}>配对新设备</a><button className="pair-link settings-link" type="button" onClick={() => setSettingsOpen((value) => !value)}>连接设置</button></div>
      </header>
      {settingsOpen && <>
        <SettingsPanel initial={boot.settings} storage={boot.storage} onSaved={() => window.location.reload()} onClose={() => setSettingsOpen(false)} />
        {selectedNodeId && <NodeSettingsPanel client={boot.client} nodeId={selectedNodeId} connectionState={connectionState} />}
      </>}

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
            <div className="detail-actions">
              {selectedThread && <LeaseControl lease={lease} held={leaseHeld} onAcquire={() => void changeLease(false)} onTakeover={() => void changeLease(true)} onRelease={() => void releaseCurrentLease()} />}
              {selectedThread && !activeTurn && <button className="secondary-button" disabled={busy || connectionState !== "connected"} onClick={() => void runControl("thread.resume", {}, { workspaceId: selectedWorkspaceId, threadId: selectedThreadId })}>恢复 Thread</button>}
            </div>
          </div>
          {selectedThread && (selectedThread.recovery === "history_gap" || selectedThread.historyState === "partial" || selectedThread.historyState === "unavailable") && <div className="notice-banner"><span>!</span><div><b>{selectedThread.recovery === "history_gap" ? "历史存在缺口" : "历史内容不完整"}</b><small>源头事件或当前 Codex 版本无法提供全部历史，下面内容可能是部分视图。</small></div></div>}
          {selectedNode?.runtimeStatus === "unavailable" && <div className="notice-banner warning"><span>×</span><div><b>Codex app-server 暂不可用</b><small>Node 仍在保留本地状态，恢复连接后会继续同步。</small></div></div>}
          <div className="timeline">
            {turns.map((turn) => <TurnCard key={turn.key} turn={turn} />)}
            {!turns.length && <div className="detail-empty"><span className="signal-line" /><b>{selectedThreadId ? "正在读取 Thread 历史" : "选择一个 Thread，或直接开始新任务"}</b><p>源头的消息、命令、工具活动和文件变化会按时间顺序出现在这里。</p></div>}
          </div>
          {approvals.length > 0 && <div className="read-only-panel"><div className="panel-title"><span>审批请求</span><em>{leaseHeld ? "控制权已持有" : "需要控制权"}</em></div>{approvals.map((approval) => <div className="approval-row" key={approval.key}><span className="approval-icon">!</span><div><b>{approval.kind ?? "高风险操作"}</b><p>{approval.summary ?? "Codex 正在等待本机审批"}</p></div><div className="approval-actions"><button disabled={!leaseHeld || !approval.operationDigest} onClick={() => void resolveApproval(approval, "decline")}>拒绝</button><button disabled={!leaseHeld || !approval.operationDigest} onClick={() => void resolveApproval(approval, "accept")}>批准</button></div></div>)}</div>}
          {action && <div className={`action-feedback ${action.state}`}><span className="status-dot" /><span>{actionLabel(action.state)}</span><small>{action.type}</small></div>}
          {unreadNotifications.length > 0 && <div className="notification-strip"><b>最近动态</b><span>{unreadNotifications[0].summary}</span></div>}
          {message && <div className="inline-message">{message}</div>}
          <form className="composer" onSubmit={(event) => void submit(event)}>
            <div className="composer-mode"><button type="button" className={composeMode === "new" ? "active" : ""} onClick={() => { setComposeMode("new"); setSelectedThreadId(""); }}>新 Thread</button><button type="button" className={composeMode === "turn" ? "active" : ""} disabled={!selectedThreadId} onClick={() => setComposeMode("turn")}>追加 Turn</button></div>
            <textarea value={input} onChange={(event) => setInput(event.target.value)} placeholder={composeMode === "new" ? "告诉 Codex 你想在这个工作区完成什么…" : "继续这个上下文…"} disabled={busy || connectionState !== "connected" || !selectedWorkspaceId} rows={3} />
            <div className="composer-footer"><span>{selectedWorkspaceId ? `${selectedNode?.name ?? "节点"} · ${workspaces.find((workspace) => workspace.workspaceId === selectedWorkspaceId)?.name ?? "工作区"}` : "选择工作区后开始"}</span><div>{activeTurn && <button type="button" className="stop-button" disabled={busy || !leaseHeld} onClick={() => void runControl("turn.interrupt", {}, { workspaceId: selectedWorkspaceId, threadId: selectedThreadId, turnId: activeTurn.turnId })}>停止 Turn</button>}<button className="send-button" disabled={busy || !input.trim() || connectionState !== "connected" || !selectedWorkspaceId || (composeMode === "turn" && !leaseHeld)}>{busy ? "发送中…" : composeMode === "new" ? "开始任务" : leaseHeld ? "发送" : "需控制权"}<span>↗</span></button></div></div>
          </form>
        </section>
      </div>
    </main>
  );
}

function LeaseControl({ lease, held, onAcquire, onTakeover, onRelease }: { lease: LeaseState; held: boolean; onAcquire: () => void; onTakeover: () => void; onRelease: () => void }) {
  if (held) return <div className="lease-control"><span className="lease-badge held">你可操作</span><button className="text-button" onClick={onRelease}>释放</button></div>;
  if (lease.state === "occupied") return <div className="lease-control"><span className="lease-badge occupied">他人控制 · {lease.expiresAt ? formatLeaseTime(lease.expiresAt) : ""}</span><button className="text-button" onClick={onTakeover}>接管</button></div>;
  return <div className="lease-control"><span className="lease-badge">只读</span><button className="secondary-button" onClick={onAcquire}>获取控制权</button></div>;
}

function TurnCard({ turn }: { turn: ProjectionState["turns"][string] }) {
  return <article className={`turn-card ${turn.status ?? ""}`}><header className="turn-header"><span className="turn-index">TURN</span><b>{statusLabel(turn.status)}</b><time>{formatTime(turn.updatedAt)}</time></header><div className="item-stack">{turn.items.map((item) => <ItemCard item={item} key={item.id} />)}{!turn.items.length && <div className="turn-empty">源头尚未发送可展示的内容</div>}</div></article>;
}

function ItemCard({ item }: { item: ThreadItemProjection }) {
  if (item.kind === "agent_message" || item.kind === "user_message") return <div className={`message-block ${item.kind}`}><span className="item-label">{item.kind === "user_message" ? "你" : "CODEX"}</span><p>{item.text || "（空消息）"}</p></div>;
  if (item.kind === "command" || item.kind === "command_output") return <details className="activity-card command-card" open={item.kind === "command"}><summary><span className="activity-icon">$</span><span><b>{item.command || "命令输出"}</b><small>{item.status ?? "执行中"}{item.exitCode !== undefined ? ` · exit ${item.exitCode}` : ""}</small></span></summary>{item.output && <pre>{item.output}</pre>}</details>;
  if (item.kind === "tool") return <div className="activity-card"><span className="activity-icon">◇</span><span><b>{item.toolName || "工具调用"}</b><small>{item.status ?? "已记录"}</small></span></div>;
  if (item.kind === "file_change" || item.kind === "diff") return <details className="activity-card diff-card"><summary><span className="activity-icon">{item.kind === "diff" ? "±" : "↳"}</span><span><b>{item.path || "文件变更"}</b><small>{item.changeType || "Diff 已更新"}{item.truncated ? ` · 仅展示 ${Math.min(item.totalBytes ?? 0, 64 * 1024)} / ${item.totalBytes ?? "?"} bytes` : ""}</small></span></summary>{item.diff && <pre>{item.diff}</pre>}</details>;
  return <div className="activity-card error-card"><span className="activity-icon">!</span><span><b>{item.errorCode || "未识别活动"}</b><small>{item.errorMessage || "Codex 返回了未支持的历史项，正文未被转发。"}</small></span></div>;
}

function LoadingScreen() { return <main className="loading-screen"><span className="brand-mark">枢</span><h1>正在恢复你的工作区</h1><p>从浏览器安全存储读取控制端身份和本地上下文…</p><div className="loading-line" /></main>; }

function PairingScreen({ pairingURL, settings, storage, reason, configured }: { pairingURL: string; settings: StoredRuntimeSettings; storage?: ControlStorage; reason?: string; configured: boolean }) {
  return <main className="pairing-screen"><div className="pairing-card"><span className="brand-mark">枢</span><span className="section-kicker">个人控制端</span><h1>连接你的 Codex 工作区</h1><p>{configured ? "先填写办公室或家庭电脑的 HTTPS/WSS 地址，保存后即可从手机连接。" : "先从办公室或家庭电脑生成配对链接。控制端身份只保存在本浏览器的 IndexedDB 中。"}</p>{reason && <small className="error-copy">{reason}</small>}{!configured && pairingURL && <a className="primary-link" href={pairingURL}>打开配对页 <span>↗</span></a>}{storage ? <SettingsPanel initial={settings} storage={storage} compact onSaved={() => window.location.reload()} /> : <small className="error-copy">浏览器 IndexedDB 不可用，无法保存连接设置。</small>}<div className="trust-note"><span>✓</span> 私钥不进入 URL、日志或 Server</div></div></main>;
}

function SettingsPanel({ initial, storage, compact = false, onSaved, onClose }: { initial: StoredRuntimeSettings; storage: ControlStorage; compact?: boolean; onSaved: () => void; onClose?: () => void }) {
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
      const settings = normalizeRuntimeSettings({ relayUrl, pairingUrl, displayName });
      setSaving(true);
      await storage.putRuntimeSettings(settings);
      onSaved();
    } catch (value) {
      setError(value instanceof Error ? value.message : "连接设置无效");
      setSaving(false);
    }
  };
  const testConnection = () => {
    setError("");
    setTestStatus("");
    let settings: StoredRuntimeSettings;
    try {
      settings = normalizeRuntimeSettings({ relayUrl, pairingUrl, displayName });
    } catch (value) {
      setError(value instanceof Error ? value.message : "连接设置无效");
      return;
    }
    if (typeof WebSocket === "undefined") {
      setTestStatus("当前浏览器不支持 WebSocket");
      return;
    }
    setTestStatus("正在测试 WSS/TLS…");
    let finished = false;
    const socket = new WebSocket(settings.relayUrl, RELAY_SUBPROTOCOL);
    const timer = window.setTimeout(() => {
      if (finished) return;
      finished = true;
      socket.close();
      setTestStatus("连接超时，请检查 IP、端口和证书信任");
    }, 5000);
    socket.onopen = () => {
      if (finished) return;
      finished = true;
      window.clearTimeout(timer);
      socket.close();
      setTestStatus("WSS/TLS 可达；保存后会完成身份认证");
    };
    socket.onerror = () => {
      if (finished) return;
      finished = true;
      window.clearTimeout(timer);
      setTestStatus("WSS/TLS 测试失败，请检查证书 SAN、Origin 和网络");
    };
  };
  const reset = async () => {
    setError("");
    await storage.removeRuntimeSettings();
    onSaved();
  };
  return <section className={`settings-panel ${compact ? "compact" : ""}`} aria-label="连接设置"><div className="settings-heading"><div><span className="section-kicker">连接设置</span><h2>通过 IP 或域名连接</h2></div>{onClose && <button className="text-button" type="button" onClick={onClose}>关闭</button>}</div><p className="settings-help">Relay 必须使用 <code>wss://</code>，配对页必须使用 <code>https://</code>。局域网 IP 也需要设备信任对应的 TLS 证书。</p><form onSubmit={(event) => void save(event)}><label>Relay URL<input value={relayUrl} onChange={(event) => setRelayUrl(event.target.value)} placeholder="wss://192.168.1.20:7444/web/connect" inputMode="url" /></label><label>Pairing URL<input value={pairingUrl} onChange={(event) => setPairingUrl(event.target.value)} placeholder="https://192.168.1.20:7444/pair" inputMode="url" /></label><label>设备显示名称（可选）<input value={displayName} onChange={(event) => setDisplayName(event.target.value)} maxLength={128} /></label>{error && <small className="error-copy">{error}</small>}{testStatus && <small className="settings-status">{testStatus}</small>}<div className="settings-actions"><button className="secondary-button" type="button" onClick={testConnection}>测试连接</button><button className="text-button" type="button" onClick={() => void reset()}>恢复部署默认</button><button className="primary-link" type="submit" disabled={saving}>{saving ? "保存中…" : "保存并重新连接"}</button></div></form></section>;
}

type NodeConfigView = {
  revision: string;
  host?: { name?: string };
  relay?: { url?: string; proxyUrl?: string; connectTimeoutSeconds?: number; credentialConfigured?: boolean };
  events?: { maxAgeHours?: number; maxSizeMiB?: number };
  adapter?: { codexEnabled?: boolean; binaryConfigured?: boolean; homeConfigured?: boolean; runtimeMode?: string };
  workspaces?: Array<{ id: string; name?: string; permissionProfile?: string; allowNetwork?: boolean }>;
  pendingChanges?: number;
};

function NodeSettingsPanel({ client, nodeId, connectionState }: { client: ControlClient; nodeId: string; connectionState: ControlClientState }) {
  const [view, setView] = useState<NodeConfigView>();
  const [hostName, setHostName] = useState("");
  const [relayUrl, setRelayUrl] = useState("");
  const [proxyUrl, setProxyUrl] = useState("");
  const [timeout, setTimeoutValue] = useState(30);
  const [maxAge, setMaxAge] = useState(168);
  const [maxSize, setMaxSize] = useState(256);
  const [status, setStatus] = useState("正在读取节点配置…");
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
    if (connectionState !== "connected") {
      setStatus("节点当前离线，连接恢复后可读取配置");
      return;
    }
    setStatus("正在读取节点配置…");
    try {
      const result = await client.request("config.read", {}, { nodeId });
      const payload = result.payload as { config?: NodeConfigView };
      if (!payload.config) throw new Error("节点没有返回脱敏配置");
      applyView(payload.config);
      setStatus("已读取脱敏配置");
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "读取节点配置失败");
    }
  };

  useEffect(() => { void read(); }, [nodeId, connectionState]);

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
      const result = await client.request("config.update", { baseRevision: view.revision, changes }, { nodeId });
      const payload = result.payload as { config?: NodeConfigView; requiresLocalConfirmation?: boolean; applied?: boolean };
      if (payload.config) applyView(payload.config);
      setStatus(payload.requiresLocalConfirmation ? "已提交；Relay/代理变更需要 Node 本机确认" : payload.applied ? "已应用，Node 正在安全重载" : "更新已提交");
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "更新节点配置失败，请重新读取 revision");
    } finally {
      setSaving(false);
    }
  };

  return <section className="settings-panel node-settings-panel" aria-label="Node 设置"><div className="settings-heading"><div><span className="section-kicker">Node 设置 · {nodeId.slice(0, 12)}</span><h2>脱敏配置与本机确认</h2></div><button className="text-button" type="button" onClick={() => void read()}>重新读取</button></div><p className="settings-help">配置 revision：<code>{view?.revision ?? "—"}</code>。Relay 地址、代理和工作区安全边界变更会保存为待确认项；凭据、路径、Server 监听与 TLS 私钥不可远程修改。</p><form onSubmit={(event) => void save(event)}><label>Node 显示名称<input value={hostName} onChange={(event) => setHostName(event.target.value)} maxLength={128} /></label><div className="settings-grid"><label>Relay URL（需本机确认）<input value={relayUrl} onChange={(event) => setRelayUrl(event.target.value)} inputMode="url" /></label><label>Relay Proxy URL（需本机确认）<input value={proxyUrl} onChange={(event) => setProxyUrl(event.target.value)} inputMode="url" /></label></div><div className="settings-grid"><label>连接超时（秒）<input type="number" min={1} max={300} value={timeout} onChange={(event) => setTimeoutValue(Number(event.target.value))} /></label><label>事件保留时间（小时）<input type="number" min={1} max={8760} value={maxAge} onChange={(event) => setMaxAge(Number(event.target.value))} /></label><label>事件上限（MiB）<input type="number" min={1} max={16384} value={maxSize} onChange={(event) => setMaxSize(Number(event.target.value))} /></label></div>{view?.adapter && <div className="settings-status-row"><span>Codex：{view.adapter.codexEnabled ? "已启用" : "未启用"} · {view.adapter.runtimeMode || "默认运行时"}</span><span>凭据：{view.relay?.credentialConfigured ? "已配置（不展示）" : "未配置"}</span><span>待确认：{view.pendingChanges ?? 0}</span></div>}{view?.workspaces?.map((workspace) => <div className="settings-status-row" key={workspace.id}><span>{workspace.name || workspace.id}</span><span>{workspace.permissionProfile === "workspace-write" ? "可写" : "只读"}</span><span>{workspace.allowNetwork ? "网络开启" : "网络关闭"}</span></div>)}<small className={status.includes("失败") || status.includes("离线") ? "error-copy" : "settings-status"}>{status}</small><div className="settings-actions"><button className="primary-link" type="submit" disabled={saving || !view || connectionState !== "connected"}>{saving ? "保存中…" : "保存 Node 配置"}</button></div></form></section>;
}

async function request(client: ControlClient, type: "device.sync" | "workspace.list" | "thread.list" | "thread.read", payload: Record<string, unknown>, target: { nodeId: string; workspaceId?: string; threadId?: string }) { return client.request(type, payload, target); }


function connectionLabel(state: ControlClientState): string { return ({ idle: "未连接", connecting: "连接中", authenticating: "安全认证", connected: "已连接", reconnecting: "正在重连", paused: "已暂停", closed: "已关闭", reauth_required: "需要重新配对" })[state]; }
function statusLabel(status?: string): string { return ({ running: "执行中", active: "执行中", inProgress: "执行中", completed: "已完成", failed: "失败", interrupted: "已停止", idle: "待继续", waiting_approval: "等待审批", uncertain: "待确认", ambiguous: "结果不确定", unavailable: "运行时不可用" }[status ?? ""] ?? "待同步"); }
function actionLabel(state: string): string { return ({ sent: "已发送", confirmed: "源头已确认", rejected: "已拒绝", ambiguous: "结果不确定", unknown: "结果未知", offline: "节点离线" }[state] ?? state); }
function formatTime(value?: string): string { if (!value) return "刚刚"; const date = new Date(value); if (Number.isNaN(date.getTime())) return "刚刚"; return date.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }); }
function formatLeaseTime(value: string): string { const remaining = Math.max(0, Math.round((Date.parse(value) - Date.now()) / 1000)); return remaining > 0 ? `${remaining}s` : "已过期"; }
function readContext(): { nodeId?: string; workspaceId?: string; threadId?: string } { try { const raw = sessionStorage.getItem("yuanshu-workbench-context"); return raw ? JSON.parse(raw) as { nodeId?: string; workspaceId?: string; threadId?: string } : {}; } catch { return {}; } }
function writeContext(value: { nodeId: string; workspaceId: string; threadId: string }): void { try { sessionStorage.setItem("yuanshu-workbench-context", JSON.stringify(value)); } catch { /* browser storage is optional */ } }
