import { lazy, Suspense, useEffect, useMemo, useRef, useState, useSyncExternalStore, type FormEvent, type KeyboardEvent } from "react";

import type { LeaseScope, LeaseState } from "../relay/control-client";
import type { RuntimeSettings } from "../relay/runtime-config";
import type { ControlStorage } from "../relay/storage";
import { threadKey, turnKey, type ApprovalProjection, type FileChangeProjection, type ThreadItemProjection, type TurnProjection } from "../state/projection";
import { Dialog } from "./Dialog";
import { Icon, type IconName } from "./Icon";
import { SettingsView } from "./Settings";
import { filterTasks, selectHomeGroups, selectNotifications, selectTasks, selectThreadApprovals, type TaskFilter, type TaskSummary } from "./selectors";
import { resourceKey, type ResourceState, type WorkbenchSession } from "./session";

const MarkdownContent = lazy(() => import("./MarkdownContent"));

type Screen = "home" | "tasks" | "notifications" | "settings";
type Selection = { nodeId: string; workspaceId: string; threadId: string };
type Confirmation =
  | { kind: "takeover" }
  | { kind: "approval"; approval: ApprovalProjection; decision: "accept" | "decline"; step: 1 | 2 };

export function Workbench({ session, storage, settings, onSettingsSaved }: { session: WorkbenchSession; storage: ControlStorage; settings: RuntimeSettings; onSettingsSaved: () => void }) {
  const snapshot = useSyncExternalStore(session.subscribe, session.getSnapshot, session.getSnapshot);
  const saved = useRef(readContext());
  const [screen, setScreen] = useState<Screen>("home");
  const [selectedNodeId, setSelectedNodeId] = useState(saved.current.nodeId ?? "");
  const [selectedWorkspaceId, setSelectedWorkspaceId] = useState(saved.current.workspaceId ?? "");
  const [selectedThreadId, setSelectedThreadId] = useState(saved.current.threadId ?? "");
  const [mobileThreadOpen, setMobileThreadOpen] = useState(Boolean(saved.current.threadId));
  const [filter, setFilter] = useState<TaskFilter>("all");
  const [query, setQuery] = useState("");
  const [taskNodeFilter, setTaskNodeFilter] = useState("");
  const [taskWorkspaceFilter, setTaskWorkspaceFilter] = useState("");
  const state = snapshot.projection;

  const nodes = useMemo(() => Object.values(state.nodes).sort((left, right) => (left.name ?? left.nodeId).localeCompare(right.name ?? right.nodeId)), [state, snapshot.revision]);
  const workspaces = useMemo(() => Object.values(state.workspaces).filter((workspace) => !selectedNodeId || workspace.nodeId === selectedNodeId), [state, selectedNodeId, snapshot.revision]);
  const tasks = useMemo(() => selectTasks(state), [state, snapshot.revision]);
  const filteredTasks = useMemo(() => filterTasks(tasks, filter, query, taskNodeFilter, taskWorkspaceFilter), [tasks, filter, query, taskNodeFilter, taskWorkspaceFilter]);
  const groups = useMemo(() => selectHomeGroups(tasks), [tasks]);
  const notifications = useMemo(() => selectNotifications(state), [state, snapshot.revision]);
  const unread = notifications.filter((notification) => !notification.read).length;
  const taskResource = useMemo(() => summarizeTaskResources(snapshot.resources), [snapshot.resources]);

  useEffect(() => {
    if (!nodes.length) return;
    if (!selectedNodeId || !nodes.some((node) => node.nodeId === selectedNodeId)) setSelectedNodeId(nodes[0].nodeId);
  }, [nodes, selectedNodeId]);

  useEffect(() => {
    if (!selectedNodeId) return;
    const available = Object.values(state.workspaces).filter((workspace) => workspace.nodeId === selectedNodeId);
    if (!available.length) return;
    if (!selectedWorkspaceId || !available.some((workspace) => workspace.workspaceId === selectedWorkspaceId)) setSelectedWorkspaceId(available[0].workspaceId);
  }, [state, snapshot.revision, selectedNodeId, selectedWorkspaceId]);

  useEffect(() => {
    if (!selectedNodeId || !selectedWorkspaceId) return;
    writeContext({ nodeId: selectedNodeId, workspaceId: selectedWorkspaceId, threadId: selectedThreadId });
  }, [selectedNodeId, selectedWorkspaceId, selectedThreadId]);

  useEffect(() => {
    const created = snapshot.createdThread;
    if (!created) return;
    setSelectedNodeId(created.nodeId);
    setSelectedWorkspaceId(created.workspaceId);
    setSelectedThreadId(created.threadId);
    setMobileThreadOpen(true);
    void session.loadThread(created.nodeId, created.workspaceId, created.threadId, true).catch(() => undefined);
    session.clearCreatedThread(created.messageId);
  }, [snapshot.createdThread, session]);

  useEffect(() => {
    const onPopState = () => setMobileThreadOpen(false);
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  const openThread = async (task: TaskSummary, pushHistory = true) => {
    const { thread } = task;
    setSelectedNodeId(thread.nodeId);
    setSelectedWorkspaceId(thread.workspaceId);
    setSelectedThreadId(thread.threadId);
    setMobileThreadOpen(true);
    if (pushHistory && !mobileThreadOpen) window.history.pushState({ yuanshuWorkbench: "thread" }, "");
    await session.loadThread(thread.nodeId, thread.workspaceId, thread.threadId).catch(() => undefined);
  };

  const openNewTask = () => {
    setSelectedThreadId("");
    setMobileThreadOpen(true);
    window.history.pushState({ yuanshuWorkbench: "thread" }, "");
  };

  const openNotification = async (notification: (typeof notifications)[number]) => {
    const task = notification.threadId ? tasks.find((candidate) => candidate.thread.nodeId === notification.nodeId && candidate.thread.workspaceId === notification.workspaceId && candidate.thread.threadId === notification.threadId) : undefined;
    if (task) {
      await openThread(task);
      await session.markNotificationRead(notification.id).catch(() => undefined);
      return;
    }
    if (notification.threadId && notification.workspaceId) {
      setSelectedNodeId(notification.nodeId);
      setSelectedWorkspaceId(notification.workspaceId);
      setSelectedThreadId(notification.threadId);
      setMobileThreadOpen(true);
      window.history.pushState({ yuanshuWorkbench: "thread" }, "");
      try {
        await session.loadThread(notification.nodeId, notification.workspaceId, notification.threadId, true);
        await session.markNotificationRead(notification.id);
      } catch { /* keep the notification unread until the target opens */ }
      return;
    }
    if (!notification.threadId) await session.markNotificationRead(notification.id).catch(() => undefined);
  };

  const selectScreen = (next: Screen) => {
    setScreen(next);
    setMobileThreadOpen(false);
  };

  return <main className={`workbench-shell ${mobileThreadOpen ? "thread-open" : ""}`}>
    <header className="workbench-topbar">
      <div className="brand-lockup"><span className="brand-mark">枢</span><div><strong>远枢</strong><span>Personal workspace</span></div></div>
      <nav className="desktop-nav" aria-label="工作台导航"><button type="button" className={screen === "home" ? "active" : ""} onClick={() => selectScreen("home")}>首页</button><button type="button" className={screen === "tasks" ? "active" : ""} onClick={() => selectScreen("tasks")}>任务</button><button type="button" className={screen === "notifications" ? "active" : ""} onClick={() => selectScreen("notifications")}>通知{unread > 0 ? ` ${unread}` : ""}</button><button type="button" className={screen === "settings" ? "active" : ""} onClick={() => selectScreen("settings")}>设置</button></nav>
      <button className={`connection-state ${snapshot.connectionState}`} type="button" onClick={() => void session.refreshAll()} aria-label="刷新连接状态"><span className="semantic-state" />{connectionLabel(snapshot.connectionState)}{unread > 0 && <span className="notification-count">{unread}</span>}</button>
    </header>

    {screen === "notifications" ? <NotificationsView notifications={notifications} resource={snapshot.resources[resourceKey.notifications]} onRefresh={() => void session.refreshNotifications()} onOpen={(notification) => void openNotification(notification)} /> : screen === "settings" ? <SettingsView session={session} storage={storage} settings={settings} selectedNodeId={selectedNodeId} onSettingsSaved={onSettingsSaved} /> : <div className="workbench-grid">
      <ContextRail nodes={nodes} workspaces={workspaces} selectedNodeId={selectedNodeId} selectedWorkspaceId={selectedWorkspaceId} resources={snapshot.resources} onNode={(nodeId) => { setSelectedNodeId(nodeId); setSelectedWorkspaceId(""); setSelectedThreadId(""); }} onWorkspace={(workspaceId) => { setSelectedWorkspaceId(workspaceId); setSelectedThreadId(""); }} />
      <section className="task-pane">
        <TaskDataState resource={taskResource} hasTasks={tasks.length > 0} onRetry={() => void session.refreshAll()} />
        {screen === "home" ? <HomeView groups={groups} nodes={nodes} onOpen={(task) => void openThread(task)} onNew={openNewTask} /> : <TasksView tasks={filteredTasks} allTasks={tasks} nodes={nodes} workspaces={Object.values(state.workspaces)} filter={filter} query={query} selectedNodeId={taskNodeFilter} selectedWorkspaceId={taskWorkspaceFilter} onFilter={setFilter} onQuery={setQuery} onNode={(nodeId) => { setTaskNodeFilter(nodeId); setTaskWorkspaceFilter(""); if (nodeId) setSelectedNodeId(nodeId); }} onWorkspace={(workspaceId) => { setTaskWorkspaceFilter(workspaceId); if (workspaceId) setSelectedWorkspaceId(workspaceId); }} onOpen={(task) => void openThread(task)} onNew={openNewTask} />}
      </section>
      <ThreadDetail session={session} snapshotRevision={snapshot.revision} connectionState={snapshot.connectionState} state={state} resource={selectedThreadId ? snapshot.resources[resourceKey.thread(selectedNodeId, selectedWorkspaceId, selectedThreadId)] : undefined} selectedNodeId={selectedNodeId} selectedWorkspaceId={selectedWorkspaceId} selectedThreadId={selectedThreadId} onBack={() => { if (window.history.state?.yuanshuWorkbench === "thread") window.history.back(); else setMobileThreadOpen(false); }} />
    </div>}

    <nav className="mobile-nav" aria-label="工作台导航">
      <NavButton icon="home" label="首页" active={screen === "home"} onClick={() => selectScreen("home")} />
      <NavButton icon="tasks" label="任务" active={screen === "tasks"} onClick={() => selectScreen("tasks")} />
      <NavButton icon="bell" label="通知" count={unread} active={screen === "notifications"} onClick={() => selectScreen("notifications")} />
      <NavButton icon="settings" label="设置" active={screen === "settings"} onClick={() => selectScreen("settings")} />
    </nav>
  </main>;
}

function ContextRail({ nodes, workspaces, selectedNodeId, selectedWorkspaceId, resources, onNode, onWorkspace }: { nodes: Array<{ nodeId: string; name?: string; online: boolean; runtimeStatus?: string }>; workspaces: Array<{ key: string; workspaceId: string; name?: string; permissionProfile?: string }>; selectedNodeId: string; selectedWorkspaceId: string; resources: Readonly<Record<string, ResourceState>>; onNode: (value: string) => void; onWorkspace: (value: string) => void }) {
  const nodeResource = selectedNodeId ? resources[resourceKey.node(selectedNodeId)] : undefined;
  return <aside className="context-rail" aria-label="Node 和工作区">
    <div className="rail-section"><h2>Node</h2><div className="rail-list">{nodes.map((node) => <button type="button" className={node.nodeId === selectedNodeId ? "selected" : ""} key={node.nodeId} onClick={() => onNode(node.nodeId)}><span className={`node-monogram ${node.online ? "online" : "offline"}`}>{(node.name ?? node.nodeId).slice(0, 1).toUpperCase()}</span><span><b>{node.name ?? "未命名 Node"}</b><small>{node.online ? statusLabel(node.runtimeStatus) : "离线"}</small></span></button>)}</div></div>
    <div className="rail-section workspaces"><div className="rail-heading"><h2>工作区</h2><span>{workspaces.length}</span></div>{nodeResource?.state === "loading" && !workspaces.length ? <SkeletonRows count={3} /> : nodeResource?.state === "error" && !workspaces.length ? <ResourceMessage resource={nodeResource} compact /> : <div className="rail-list">{workspaces.map((workspace) => <button type="button" className={workspace.workspaceId === selectedWorkspaceId ? "selected" : ""} key={workspace.key} onClick={() => onWorkspace(workspace.workspaceId)}><Icon name="folder" /><span><b>{workspace.name ?? workspace.workspaceId}</b><small>{workspace.permissionProfile === "workspace-write" || workspace.permissionProfile === "workspaceWrite" ? "可写" : "只读"}</small></span></button>)}</div>}</div>
    <p className="privacy-note">任务内容保留在 Node。本页面只接收结构化事件。</p>
  </aside>;
}

function HomeView({ groups, nodes, onOpen, onNew }: { groups: ReturnType<typeof selectHomeGroups>; nodes: Array<{ nodeId: string; name?: string; online: boolean; runtimeStatus?: string }>; onOpen: (task: TaskSummary) => void; onNew: () => void }) {
  const warnings = nodes.filter((node) => !node.online || ["unavailable", "not_available"].includes(node.runtimeStatus ?? ""));
  return <div className="task-view"><div className="task-heading"><div><p>现在</p><h1>继续手上的任务</h1></div><button className="button primary" type="button" onClick={onNew}><Icon name="plus" />新任务</button></div>
    {warnings.length > 0 && <div className="health-banner"><Icon name="warning" /><div><b>{warnings.length} 台 Node 需要注意</b><p>离线或 Runtime 不可用的任务仍可查看，恢复后会继续同步。</p></div></div>}
    <TaskGroup title="正在执行" tasks={groups.active} empty="当前没有正在执行的任务" onOpen={onOpen} />
    {groups.approvals.length > 0 && <TaskGroup title="等待审批" tasks={groups.approvals} onOpen={onOpen} tone="warning" />}
    {groups.uncertain.length > 0 && <TaskGroup title="需要确认" tasks={groups.uncertain} onOpen={onOpen} tone="warning" />}
    <TaskGroup title="最近任务" tasks={groups.recent} empty="同步 Thread 摘要后会显示最近任务" onOpen={onOpen} />
  </div>;
}

function TasksView({ tasks, allTasks, nodes, workspaces, filter, query, selectedNodeId, selectedWorkspaceId, onFilter, onQuery, onNode, onWorkspace, onOpen, onNew }: { tasks: TaskSummary[]; allTasks: TaskSummary[]; nodes: Array<{ nodeId: string; name?: string }>; workspaces: Array<{ nodeId: string; workspaceId: string; name?: string }>; filter: TaskFilter; query: string; selectedNodeId: string; selectedWorkspaceId: string; onFilter: (value: TaskFilter) => void; onQuery: (value: string) => void; onNode: (value: string) => void; onWorkspace: (value: string) => void; onOpen: (task: TaskSummary) => void; onNew: () => void }) {
  return <div className="task-view"><div className="task-heading"><div><p>任务</p><h1>所有上下文</h1></div><button className="button primary" type="button" onClick={onNew}><Icon name="plus" />新任务</button></div>
    <div className="task-controls"><label className="search-field"><span className="sr-only">搜索任务</span><Icon name="search" /><input value={query} onChange={(event) => onQuery(event.target.value)} placeholder="搜索已同步的标题和预览" /></label><div className="context-selects"><select aria-label="筛选 Node" value={selectedNodeId} onChange={(event) => { onNode(event.target.value); onWorkspace(""); }}><option value="">全部 Node</option>{nodes.map((node) => <option value={node.nodeId} key={node.nodeId}>{node.name ?? node.nodeId}</option>)}</select><select aria-label="筛选工作区" value={selectedWorkspaceId} onChange={(event) => onWorkspace(event.target.value)}><option value="">全部工作区</option>{workspaces.filter((workspace) => !selectedNodeId || workspace.nodeId === selectedNodeId).map((workspace) => <option value={workspace.workspaceId} key={`${workspace.nodeId}:${workspace.workspaceId}`}>{workspace.name ?? workspace.workspaceId}</option>)}</select></div></div>
    <div className="filter-tabs" role="tablist" aria-label="任务状态">{(["all", "active", "approval", "failed", "completed"] as const).map((value) => <button type="button" role="tab" aria-selected={filter === value} className={filter === value ? "active" : ""} onClick={() => onFilter(value)} key={value}>{filterLabel(value)}</button>)}</div>
    <p className="local-search-note">仅搜索当前浏览器已同步的 {allTasks.length} 个 Thread 摘要。</p>
    <div className="task-list">{tasks.map((task) => <TaskRow task={task} onOpen={() => onOpen(task)} key={task.thread.key} />)}{!tasks.length && <EmptyState icon="tasks" title="没有匹配的任务" detail="调整状态、Node、工作区或搜索条件。" />}</div>
  </div>;
}

function TaskGroup({ title, tasks, empty, tone, onOpen }: { title: string; tasks: TaskSummary[]; empty?: string; tone?: "warning"; onOpen: (task: TaskSummary) => void }) {
  return <section className={`task-group ${tone ?? ""}`}><div className="group-heading"><h2>{title}</h2><span>{tasks.length}</span></div>{tasks.length ? <div className="task-list">{tasks.map((task) => <TaskRow task={task} onOpen={() => onOpen(task)} key={task.thread.key} />)}</div> : empty ? <p className="group-empty">{empty}</p> : null}</section>;
}

function TaskRow({ task, onOpen }: { task: TaskSummary; onOpen: () => void }) {
  const status = task.latestTurn?.status ?? task.thread.status;
  return <button type="button" className="task-row" onClick={onOpen}><span className={`task-status ${status ?? "unknown"}`} aria-hidden="true" /><span className="task-copy"><b>{task.thread.title || task.thread.preview || `Thread ${task.thread.threadId.slice(0, 8)}`}</b><small>{task.thread.preview && task.thread.title ? task.thread.preview : `${task.node?.name ?? "Node"} / ${task.workspace?.name ?? "工作区"}`}</small></span><span className="task-side"><em>{task.pendingApprovals ? "待审批" : statusLabel(status)}</em><time>{formatTime(task.thread.updatedAt)}</time></span></button>;
}

function ThreadDetail({ session, snapshotRevision, connectionState, state, resource, selectedNodeId, selectedWorkspaceId, selectedThreadId, onBack }: { session: WorkbenchSession; snapshotRevision: number; connectionState: string; state: WorkbenchSession["projection"]["state"]; resource?: ResourceState; selectedNodeId: string; selectedWorkspaceId: string; selectedThreadId: string; onBack: () => void }) {
  const thread = selectedThreadId ? state.threads[threadKey(selectedNodeId, selectedWorkspaceId, selectedThreadId)] : undefined;
  const node = state.nodes[selectedNodeId];
  const workspace = state.workspaces[`${selectedNodeId}\u001f${selectedWorkspaceId}`];
  const turns = useMemo(() => thread ? thread.turnIds.map((turnId) => state.turns[turnKey(selectedNodeId, selectedWorkspaceId, selectedThreadId, turnId)]).filter((turn): turn is TurnProjection => Boolean(turn)) : [], [state, thread, selectedNodeId, selectedWorkspaceId, selectedThreadId, snapshotRevision]);
  const activeTurn = [...turns].reverse().find((turn) => ["running", "inProgress", "active"].includes(turn.status ?? ""));
  const approvals = selectThreadApprovals(state, selectedNodeId, selectedWorkspaceId, selectedThreadId);
  const files = Object.values(state.files).filter((file) => file.nodeId === selectedNodeId && file.workspaceId === selectedWorkspaceId && file.threadId === selectedThreadId).sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
  const scope: LeaseScope | undefined = selectedThreadId && selectedWorkspaceId && selectedNodeId ? { nodeId: selectedNodeId, workspaceId: selectedWorkspaceId, threadId: selectedThreadId } : undefined;
  const lease = scope ? session.getLease(scope) : { state: "none", epoch: 0 } as LeaseState;
  const leaseHeld = Boolean(scope && session.canMutate(scope, "turn.start"));
  const actions = Object.values(state.actions).filter((action) => action.nodeId === selectedNodeId && action.threadId === selectedThreadId).sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [confirmation, setConfirmation] = useState<Confirmation>();
  const [visibleItems, setVisibleItems] = useState(100);
  const [atBottom, setAtBottom] = useState(true);
  const [newItems, setNewItems] = useState(0);
  const timeline = useRef<HTMLDivElement>(null);
  const textarea = useRef<HTMLTextAreaElement>(null);
  const itemCount = turns.reduce((count, turn) => count + turn.items.length, 0);
  const previousItemCount = useRef(itemCount);

  useEffect(() => { setVisibleItems(100); setMessage(""); setInput(""); setNewItems(0); }, [selectedThreadId]);
  useEffect(() => {
    if (itemCount <= previousItemCount.current) { previousItemCount.current = itemCount; return; }
    const added = itemCount - previousItemCount.current;
    previousItemCount.current = itemCount;
    if (atBottom) requestAnimationFrame(() => timeline.current?.scrollTo({ top: timeline.current.scrollHeight }));
    else setNewItems((value) => value + added);
  }, [itemCount, atBottom]);
  useEffect(() => {
    if (!textarea.current) return;
    textarea.current.style.height = "0px";
    textarea.current.style.height = `${Math.min(180, Math.max(72, textarea.current.scrollHeight))}px`;
  }, [input]);

  const run = async (type: "thread.resume" | "turn.start" | "turn.steer" | "turn.interrupt", payload: Record<string, unknown>, target: { threadId?: string; turnId?: string }, clearInput = false) => {
    setBusy(true);
    setMessage("");
    try {
      const result = await session.request(type, payload, { nodeId: selectedNodeId, workspaceId: selectedWorkspaceId, ...target });
      const status = typeof result.payload.status === "string" ? result.payload.status : "rejected";
      if (status !== "confirmed") throw new Error(typeof result.payload.errorCode === "string" ? result.payload.errorCode : status);
      if (clearInput) setInput("");
      setMessage(type === "turn.interrupt" ? "停止请求已确认" : "源头已确认请求");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "操作结果未知");
    } finally {
      setBusy(false);
    }
  };

  const submit = async (event?: FormEvent) => {
    event?.preventDefault();
    const value = input.trim();
    if (!value || !selectedNodeId || !selectedWorkspaceId || busy) return;
    if (!selectedThreadId) {
      setBusy(true);
      setMessage("");
      try {
        const handle = await session.startThread(selectedNodeId, selectedWorkspaceId, value);
        const result = await handle.result;
        if (result.payload.status !== "confirmed") throw new Error(typeof result.payload.errorCode === "string" ? result.payload.errorCode : "thread_start_rejected");
        setInput("");
        setMessage("Thread 已创建，正在同步上下文");
      } catch (error) {
        setMessage(error instanceof Error ? error.message : "创建结果未知");
      } finally {
        setBusy(false);
      }
      return;
    }
    if (!leaseHeld) {
      setMessage("当前是只读状态，请先获取 Thread 控制权");
      return;
    }
    if (activeTurn) await run("turn.steer", { input: value }, { threadId: selectedThreadId, turnId: activeTurn.turnId }, true);
    else await run("turn.start", { input: value }, { threadId: selectedThreadId }, true);
  };

  const changeLease = async (force: boolean) => {
    if (!scope) return;
    if (force) { setConfirmation({ kind: "takeover" }); return; }
    try {
      const next = await session.acquireLease(scope, { expectedEpoch: lease.epoch });
      setMessage(next.state === "held" ? "已获得控制权" : "Thread 仍由其他控制端持有");
    } catch (error) { setMessage(error instanceof Error ? error.message : "获取控制权失败"); }
  };

  const takeover = async () => {
    if (!scope) return;
    setConfirmation(undefined);
    try {
      const next = await session.acquireLease(scope, { force: true, expectedEpoch: lease.epoch });
      setMessage(next.state === "held" ? "已接管控制权" : "接管失败");
    } catch (error) { setMessage(error instanceof Error ? error.message : "接管失败"); }
  };

  const resolveApproval = async (approval: ApprovalProjection, decision: "accept" | "decline") => {
    if (!approval.operationDigest || !scope) { setMessage("审批缺少可验证摘要，已保持只读"); return; }
    setBusy(true);
    setConfirmation(undefined);
    try {
      const result = await session.request("approval.resolve", { approvalId: approval.approvalId, decision, operationDigest: approval.operationDigest }, { nodeId: selectedNodeId, workspaceId: selectedWorkspaceId, threadId: selectedThreadId, turnId: approval.turnId, itemId: approval.itemId });
      if (result.payload.status !== "confirmed") throw new Error(typeof result.payload.errorCode === "string" ? result.payload.errorCode : "approval_rejected");
      setMessage(decision === "accept" ? "批准已由源头确认" : "拒绝已由源头确认");
    } catch (error) { setMessage(error instanceof Error ? error.message : "审批结果未知"); }
    finally { setBusy(false); }
  };

  const onComposerKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if ((event.metaKey || event.ctrlKey) && event.key === "Enter") { event.preventDefault(); void submit(); }
  };

  const visible = limitedTurns(turns, visibleItems);
  const hiddenCount = Math.max(0, itemCount - visibleItems);
  const placeholder = !selectedThreadId ? "描述你想在这个工作区完成的任务" : activeTurn ? "向当前 Turn 追加引导" : "继续这个 Thread 的上下文";

  return <section className="thread-detail" aria-label="Thread 详情">
    <div className="thread-detail-heading"><button className="mobile-back" type="button" onClick={onBack} aria-label="返回任务列表"><Icon name="back" /></button><div><p>{node?.name ?? "Node"} / {workspace?.name ?? "工作区"}</p><h1>{thread?.title || thread?.preview || (selectedThreadId ? `Thread ${selectedThreadId.slice(0, 10)}` : "开始新任务")}</h1></div>{thread && <div className="thread-heading-actions"><LeaseControl lease={lease} held={leaseHeld} onAcquire={() => void changeLease(false)} onTakeover={() => void changeLease(true)} onRelease={() => scope && void session.releaseLease(scope).catch(() => undefined)} />{!activeTurn && <button className="button secondary" type="button" disabled={busy || connectionState !== "connected" || !leaseHeld} onClick={() => void run("thread.resume", {}, { threadId: selectedThreadId })}>恢复</button>}</div>}</div>
    {thread && (thread.recovery !== "none" || thread.historyState === "partial" || thread.historyState === "unavailable") && <div className="inline-alert warning"><Icon name="warning" /><div><b>{thread.recovery === "history_gap" ? "历史存在缺口" : "历史内容不完整"}</b><p>当前视图可能只包含 Node 能够恢复的部分内容。</p></div></div>}
    {node && (!node.online || node.runtimeStatus === "unavailable") && <div className="inline-alert danger"><Icon name="warning" /><div><b>Runtime 暂不可用</b><p>本地状态会继续保留，恢复连接后重新同步。</p></div></div>}
    {resource?.state === "error" && <ResourceMessage resource={resource} onRetry={() => void session.loadThread(selectedNodeId, selectedWorkspaceId, selectedThreadId, true)} />}

    <div className="thread-scroll" ref={timeline} onScroll={(event) => { const element = event.currentTarget; const bottom = element.scrollHeight - element.scrollTop - element.clientHeight < 48; setAtBottom(bottom); if (bottom) setNewItems(0); }}>
      {hiddenCount > 0 && <button className="older-button" type="button" onClick={() => setVisibleItems((value) => value + 100)}>显示更早的 {Math.min(100, hiddenCount)} 项</button>}
      {resource?.state === "loading" && !turns.length ? <SkeletonTimeline /> : visible.length ? visible.map((turn) => <TurnCard turn={turn} key={turn.key} />) : <EmptyState icon="tasks" title={selectedThreadId ? "Thread 中还没有可展示内容" : "在当前工作区开始新任务"} detail="消息、命令、工具活动和文件变化会按顺序显示。" />}
      {approvals.length > 0 && <ApprovalPanel approvals={approvals} leaseHeld={leaseHeld} busy={busy} onResolve={(approval, decision) => setConfirmation({ kind: "approval", approval, decision, step: 1 })} />}
      {files.length > 0 && <FileChanges files={files} session={session} />}
      {newItems > 0 && <button className="new-items-button" type="button" onClick={() => { timeline.current?.scrollTo({ top: timeline.current.scrollHeight, behavior: "smooth" }); setNewItems(0); }}>查看 {newItems} 条新内容</button>}
    </div>

    <div className="thread-footer">
      {actions[0] && <div className={`action-status ${actions[0].state}`} aria-live="polite"><span className="semantic-state" /><b>{actionLabel(actions[0].state)}</b><span>{actions[0].type}</span>{actions[0].errorCode && <code>{actions[0].errorCode}</code>}</div>}
      {message && <div className="operation-message" role="status">{message}</div>}
      <form className="composer" onSubmit={(event) => void submit(event)}>
        <label className="sr-only" htmlFor="task-input">任务指令</label>
        <textarea ref={textarea} id="task-input" value={input} onChange={(event) => setInput(event.target.value)} onKeyDown={onComposerKeyDown} placeholder={placeholder} disabled={busy || connectionState !== "connected" || !selectedWorkspaceId} rows={3} />
        <div className="composer-actions"><span>{!selectedThreadId ? "新 Thread" : activeTurn ? "引导当前 Turn" : "追加 Turn"}<small>Cmd/Ctrl + Enter</small></span><div>{activeTurn && <button className="button danger" type="button" disabled={busy || !leaseHeld} onClick={() => void run("turn.interrupt", {}, { threadId: selectedThreadId, turnId: activeTurn.turnId })}><Icon name="stop" />停止</button>}<button className="button primary" type="submit" disabled={busy || !input.trim() || connectionState !== "connected" || !selectedWorkspaceId || (Boolean(selectedThreadId) && !leaseHeld)}>{busy ? "发送中" : selectedThreadId && !leaseHeld ? "需要控制权" : "发送"}<Icon name="send" /></button></div></div>
      </form>
    </div>

    {confirmation?.kind === "takeover" && <Dialog title="接管 Thread 控制权" destructive onClose={() => setConfirmation(undefined)} actions={<><button className="button secondary" type="button" onClick={() => setConfirmation(undefined)}>取消</button><button className="button danger solid" type="button" onClick={() => void takeover()}>确认接管</button></>}><p>接管后，当前持有者会立即变为只读，旧 epoch 的控制消息将被拒绝。</p><dl><dt>持有者</dt><dd>{shortID(lease.holderClientId)}</dd><dt>剩余时间</dt><dd>{lease.expiresAt ? formatLeaseTime(lease.expiresAt) : "未知"}</dd></dl></Dialog>}
    {confirmation?.kind === "approval" && <ApprovalDialog value={confirmation} onClose={() => setConfirmation(undefined)} onNext={() => setConfirmation({ ...confirmation, step: 2 })} onConfirm={() => void resolveApproval(confirmation.approval, confirmation.decision)} />}
  </section>;
}

function TurnCard({ turn }: { turn: TurnProjection }) {
  return <article className={`turn-card ${turn.status ?? ""}`}><header><span>Turn</span><b>{statusLabel(turn.status)}</b><time>{formatTime(turn.updatedAt)}</time></header><div className="turn-items">{turn.items.map((item) => <ItemCard item={item} key={item.id} />)}{!turn.items.length && <p className="turn-empty">源头尚未发送可展示内容</p>}</div></article>;
}

function ItemCard({ item }: { item: ThreadItemProjection }) {
  if (item.kind === "agent_message" || item.kind === "user_message") return <div className={`message-item ${item.kind}`}><span>{item.kind === "user_message" ? "你" : "Codex"}</span><Suspense fallback={<p className="plain-message">{item.text || "（空消息）"}</p>}><MarkdownContent value={item.text || "（空消息）"} /></Suspense></div>;
  if (item.kind === "command" || item.kind === "command_output") return <details className="activity-item" open={item.status === "running"}><summary><Icon name="terminal" /><span><b>{item.command || "命令输出"}</b><small>{item.status ?? "执行中"}{item.exitCode !== undefined ? ` / exit ${item.exitCode}` : ""}{item.truncated ? " / 已截断" : ""}</small></span></summary>{item.output && <CodePanel value={item.output} label="复制命令输出" />}</details>;
  if (item.kind === "tool") return <div className="activity-item compact"><Icon name="tool" /><span><b>{item.toolName || "工具调用"}</b><small>{item.status ?? "已记录"}</small></span></div>;
  if (item.kind === "file_change" || item.kind === "diff") return <div className="activity-item compact"><Icon name="file" /><span><b>{item.path || "文件变更"}</b><small>{item.changeType || "Diff 已更新"}{item.truncated ? " / 已截断" : ""}</small></span></div>;
  return <div className="activity-item compact error"><Icon name="warning" /><span><b>{item.errorCode || "未识别活动"}</b><small>{item.errorMessage || "Codex 返回了当前版本无法识别的历史项。"}</small></span></div>;
}

function FileChanges({ files, session }: { files: FileChangeProjection[]; session: WorkbenchSession }) {
  return <section className="file-changes"><div className="section-heading"><div><Icon name="file" /><h2>文件变化</h2></div><span>{files.length}</span></div><div>{files.map((file) => <details key={file.key} onToggle={(event) => { if (event.currentTarget.open && !file.diff) void session.loadDiff(file.nodeId, file.workspaceId, file.threadId, file.path).catch(() => undefined); }}><summary><span><b>{file.path}</b><small>{changeLabel(file.changeType)} / revision {file.revision}{file.truncated ? " / 已截断" : ""}</small></span><Icon name="chevron" /></summary>{file.diff ? <CodePanel value={file.diff} label="复制 Diff" /> : <p className="diff-loading">展开后从 Node 读取最多 64 KiB Diff</p>}{file.truncated && <p className="truncate-note">仅展示 {Math.min(file.totalBytes ?? 65_536, 65_536)} / {file.totalBytes ?? "未知"} bytes，digest {shortID(file.digest)}</p>}</details>)}</div></section>;
}

function ApprovalPanel({ approvals, leaseHeld, busy, onResolve }: { approvals: ApprovalProjection[]; leaseHeld: boolean; busy: boolean; onResolve: (approval: ApprovalProjection, decision: "accept" | "decline") => void }) {
  return <section className="approval-panel"><div className="section-heading"><div><Icon name="warning" /><h2>等待审批</h2></div><span>{approvals.length}</span></div>{approvals.map((approval) => <article key={approval.key}><div><b>{approval.kind ?? "未知风险操作"}</b><p>{approval.summary ?? "Codex 正在等待审批决定"}</p><small>到期 {formatTime(approval.expiresAt)} / digest {shortID(approval.operationDigest)}</small></div><div><button className="button secondary" type="button" disabled={!leaseHeld || busy || !approval.operationDigest} onClick={() => onResolve(approval, "decline")}>拒绝</button><button className="button warning" type="button" disabled={!leaseHeld || busy || !approval.operationDigest} onClick={() => onResolve(approval, "accept")}>批准</button></div></article>)}</section>;
}

function ApprovalDialog({ value, onClose, onNext, onConfirm }: { value: Extract<Confirmation, { kind: "approval" }>; onClose: () => void; onNext: () => void; onConfirm: () => void }) {
  const highRisk = isHighRisk(value.approval);
  const final = !highRisk || value.step === 2;
  return <Dialog title={final ? (value.decision === "accept" ? "确认批准操作" : "确认拒绝操作") : "检查高风险操作"} destructive={value.decision === "accept" && highRisk} onClose={onClose} actions={<><button className="button secondary" type="button" onClick={onClose}>取消</button>{final ? <button className={`button ${value.decision === "accept" ? "warning" : "primary"}`} type="button" onClick={onConfirm}>{value.decision === "accept" ? "发送批准" : "发送拒绝"}</button> : <button className="button warning" type="button" onClick={onNext}>继续确认</button>}</>}><p>{value.approval.summary ?? "Codex 正在等待审批决定"}</p><dl><dt>风险</dt><dd>{value.approval.risk ?? value.approval.kind ?? "未知"}</dd><dt>Turn</dt><dd>{shortID(value.approval.turnId)}</dd><dt>Item</dt><dd>{shortID(value.approval.itemId)}</dd><dt>Digest</dt><dd>{shortID(value.approval.operationDigest)}</dd><dt>到期</dt><dd>{formatTime(value.approval.expiresAt)}</dd></dl>{highRisk && <div className="dialog-warning">这项操作可能执行命令或修改文件。Node 会再次校验目标、digest、租约和有效期。</div>}</Dialog>;
}

function LeaseControl({ lease, held, onAcquire, onTakeover, onRelease }: { lease: LeaseState; held: boolean; onAcquire: () => void; onTakeover: () => void; onRelease: () => void }) {
  if (held) return <div className="lease-control"><span className="lease-badge held"><Icon name="check" />可操作</span><button type="button" onClick={onRelease}>释放</button></div>;
  if (lease.state === "occupied") return <div className="lease-control"><span className="lease-badge occupied"><Icon name="lock" />占用中 {lease.expiresAt ? formatLeaseTime(lease.expiresAt) : ""}</span><button type="button" onClick={onTakeover}>接管</button></div>;
  return <div className="lease-control"><span className="lease-badge"><Icon name="lock" />只读</span><button type="button" onClick={onAcquire}>获取控制权</button></div>;
}

function NotificationsView({ notifications, resource, onRefresh, onOpen }: { notifications: ReturnType<typeof selectNotifications>; resource?: ResourceState; onRefresh: () => void; onOpen: (notification: ReturnType<typeof selectNotifications>[number]) => void }) {
  return <section className="utility-view notifications-view"><div className="utility-heading"><div><p>通知</p><h1>最近动态</h1></div><button className="icon-action" type="button" onClick={onRefresh} aria-label="刷新通知"><Icon name="refresh" /></button></div>{resource?.state === "loading" && !notifications.length ? <SkeletonRows count={5} /> : resource?.state === "error" && !notifications.length ? <ResourceMessage resource={resource} onRetry={onRefresh} /> : notifications.length ? <div className="notification-list">{notifications.map((notification) => <button type="button" className={notification.read ? "read" : "unread"} onClick={() => onOpen(notification)} key={notification.id}><Icon name={notificationIcon(notification.type)} /><span><b>{notificationTitle(notification.type)}</b><small>{notification.summary}</small></span><time>{formatTime(notification.createdAt)}</time></button>)}</div> : <EmptyState icon="bell" title="没有通知" detail="任务完成、失败、等待审批和 Node 状态变化会显示在这里。" />}</section>;
}

function ResourceMessage({ resource, compact = false, onRetry }: { resource: ResourceState; compact?: boolean; onRetry?: () => void }) {
  if (resource.state !== "error" && resource.state !== "stale") return null;
  return <div className={`resource-message ${compact ? "compact" : ""}`}><Icon name="warning" /><div><b>{errorTitle(resource.errorCode)}</b><p>{errorDetail(resource.errorCode)}</p></div>{onRetry && <button className="button secondary" type="button" onClick={onRetry}>重试</button>}</div>;
}

function TaskDataState({ resource, hasTasks, onRetry }: { resource?: ResourceState; hasTasks: boolean; onRetry: () => void }) {
  if (!resource || resource.state === "idle" || resource.state === "ready" || resource.state === "empty") return null;
  if (resource.state === "loading") return <div className="task-sync-state" role="status"><span className="semantic-state" />正在同步各工作区的任务摘要</div>;
  if (hasTasks && resource.state === "error") return <div className="task-sync-state warning" role="status"><Icon name="warning" />部分工作区同步失败，当前结果可能不完整<button type="button" onClick={onRetry}>重试</button></div>;
  return <ResourceMessage resource={resource} compact onRetry={onRetry} />;
}

function EmptyState({ icon, title, detail }: { icon: IconName; title: string; detail: string }) { return <div className="empty-state"><Icon name={icon} /><b>{title}</b><p>{detail}</p></div>; }
function SkeletonRows({ count }: { count: number }) { return <div className="skeleton-rows" aria-label="正在加载">{Array.from({ length: count }, (_, index) => <span key={index} />)}</div>; }
function SkeletonTimeline() { return <div className="skeleton-timeline" aria-label="正在读取 Thread 历史"><span /><span /><span /></div>; }
function NavButton({ icon, label, active, count = 0, onClick }: { icon: IconName; label: string; active: boolean; count?: number; onClick: () => void }) { return <button type="button" className={active ? "active" : ""} aria-current={active ? "page" : undefined} onClick={onClick}><span><Icon name={icon} />{count > 0 && <em>{count}</em>}</span><small>{label}</small></button>; }

function CodePanel({ value, label }: { value: string; label: string }) { return <div className="code-panel"><button type="button" onClick={() => void navigator.clipboard?.writeText(value)} aria-label={label}><Icon name="copy" /></button><pre>{value}</pre></div>; }

function limitedTurns(turns: TurnProjection[], limit: number): TurnProjection[] {
  let remaining = limit;
  const result: TurnProjection[] = [];
  for (let index = turns.length - 1; index >= 0 && remaining > 0; index -= 1) {
    const turn = turns[index];
    const items = turn.items.slice(Math.max(0, turn.items.length - remaining));
    remaining -= items.length;
    result.unshift({ ...turn, items });
  }
  return result;
}

function filterLabel(value: TaskFilter) { return ({ all: "全部", active: "运行中", approval: "待审批", failed: "失败", completed: "已完成" })[value]; }
function connectionLabel(value: string) { return ({ idle: "未连接", connecting: "连接中", authenticating: "安全认证", connected: "已连接", reconnecting: "正在重连", paused: "已暂停", closed: "已关闭", reauth_required: "需要重新配对" } as Record<string, string>)[value] ?? "未知状态"; }
function statusLabel(value?: string) { return ({ running: "执行中", active: "执行中", inProgress: "执行中", completed: "已完成", failed: "失败", interrupted: "已停止", idle: "待继续", waiting_approval: "等待审批", uncertain: "待确认", ambiguous: "结果不确定", unavailable: "运行时不可用", reconnecting: "恢复中" } as Record<string, string>)[value ?? ""] ?? "待同步"; }
function actionLabel(value: string) { return ({ sent: "已发送", executing: "正在执行", confirmed: "源头已确认", rejected: "已拒绝", failed: "执行失败", ambiguous: "结果不确定", unknown: "结果未知", offline: "Node 离线" } as Record<string, string>)[value] ?? value; }
function changeLabel(value?: string) { return ({ created: "新增", modified: "修改", deleted: "删除", renamed: "重命名" } as Record<string, string>)[value ?? ""] ?? "变更"; }
function notificationTitle(value: string) { return ({ "task.completed": "任务完成", "task.failed": "任务失败", "approval.required": "等待审批", "node.offline": "Node 离线", "node.online": "Node 上线" } as Record<string, string>)[value] ?? "状态更新"; }
function notificationIcon(value: string): IconName { return value === "approval.required" || value === "task.failed" ? "warning" : value.startsWith("node.") ? "node" : "check"; }
function formatTime(value?: string) { if (!value) return "刚刚"; const date = new Date(value); return Number.isNaN(date.getTime()) ? "刚刚" : date.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }); }
function formatLeaseTime(value: string) { const seconds = Math.max(0, Math.round((Date.parse(value) - Date.now()) / 1_000)); return seconds > 0 ? `${seconds}s` : "已过期"; }
function shortID(value?: string) { if (!value) return "未提供"; return value.length > 14 ? `${value.slice(0, 8)}...${value.slice(-4)}` : value; }
function isHighRisk(approval: ApprovalProjection) { return !approval.risk || !approval.kind || /high|unknown|command|file|write|delete|shell/i.test(`${approval.risk} ${approval.kind}`); }
function errorTitle(code?: string) { if (!code) return "读取失败"; if (code.includes("offline")) return "Node 离线"; if (code.includes("reauth")) return "需要重新配对"; if (code.includes("history")) return "历史存在缺口"; if (code.includes("unsupported")) return "当前能力不受支持"; return "读取失败"; }
function errorDetail(code?: string) { if (!code) return "请检查连接后重试。"; if (code.includes("offline")) return "Node 恢复在线后可以重新读取。"; if (code.includes("reauth")) return "当前控制端身份已失效，请重新配对。"; return `错误代码：${code}`; }
function readContext(): Partial<Selection> { try { const value = sessionStorage.getItem("yuanshu-workbench-context"); return value ? JSON.parse(value) as Partial<Selection> : {}; } catch { return {}; } }
function writeContext(value: Selection) { try { sessionStorage.setItem("yuanshu-workbench-context", JSON.stringify(value)); } catch { /* optional browser context only */ } }

function summarizeTaskResources(resources: Readonly<Record<string, ResourceState>>): ResourceState | undefined {
  const values = Object.entries(resources).filter(([key]) => key.startsWith("threads:")).map(([, value]) => value);
  if (!values.length) return undefined;
  const error = values.find((value) => value.state === "error");
  if (error) return error;
  const stale = values.find((value) => value.state === "stale");
  if (stale) return stale;
  if (values.some((value) => value.state === "loading")) return { state: "loading" };
  const updatedAt = values.flatMap((value) => "updatedAt" in value ? [value.updatedAt] : []).sort().at(-1) ?? new Date(0).toISOString();
  return values.every((value) => value.state === "empty") ? { state: "empty", updatedAt } : { state: "ready", updatedAt };
}
