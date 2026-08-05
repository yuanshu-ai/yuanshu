import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";

import { BrandMark } from "../BrandMark";
import { LanguageSwitch, useI18n } from "../i18n";
import type { RuntimeSettings } from "../relay/runtime-config";
import type { ControlStorage } from "../relay/storage";
import { Dialog } from "./Dialog";
import { Icon, type IconName } from "./Icon";
import { NewTaskFlow, type NewTaskTarget } from "./NewTaskFlow";
import { selectHomeGroups, selectNotifications, selectTasks, filterTasks, type TaskFilter, type TaskSummary } from "./selectors";
import { SettingsView } from "./Settings";
import { resourceKey, type ResourceState, type WorkbenchSession } from "./session";
import { DevicesView, HomeView, TaskContextBar, TasksView } from "./TaskViews";
import { ThreadDetail } from "./ThreadDetail";
import { connectionLabel, EmptyState, formatTime, ResourceMessage, SkeletonRows } from "./WorkbenchPrimitives";

type Screen = "home" | "tasks" | "devices" | "notifications" | "settings";
type Selection = { nodeId: string; workspaceId: string; threadId: string };
type HistorySurface = "thread" | "new-task";

export function Workbench({ session, storage, settings, onSettingsSaved }: { session: WorkbenchSession; storage: ControlStorage; settings: RuntimeSettings; onSettingsSaved: () => void }) {
  const { t } = useI18n();
  const snapshot = useSyncExternalStore(session.subscribe, session.getSnapshot, session.getSnapshot);
  const saved = useRef(readContext());
  const restoredThreadPending = useRef(Boolean(saved.current.nodeId && saved.current.workspaceId && saved.current.threadId));
  const [screen, setScreen] = useState<Screen>("home");
  const [selectedNodeId, setSelectedNodeId] = useState(saved.current.nodeId ?? "");
  const [selectedAgentInstanceId, setSelectedAgentInstanceId] = useState("");
  const [selectedWorkspaceId, setSelectedWorkspaceId] = useState(saved.current.workspaceId ?? "");
  const [selectedThreadId, setSelectedThreadId] = useState(saved.current.threadId ?? "");
  const [mobileThreadOpen, setMobileThreadOpen] = useState(Boolean(saved.current.threadId));
  const [filter, setFilter] = useState<TaskFilter>("all");
  const [query, setQuery] = useState("");
  const [taskNodeFilter, setTaskNodeFilter] = useState("");
  const [taskWorkspaceFilter, setTaskWorkspaceFilter] = useState("");
  const [readSequences, setReadSequences] = useState<Readonly<Record<string, number>>>(() => readTaskProgress());
  const [newTask, setNewTask] = useState<{ initialTarget?: NewTaskTarget }>();
  const [confirmedThreadStart, setConfirmedThreadStart] = useState("");
  const [detailDraftDirty, setDetailDraftDirty] = useState(false);
  const [newTaskDraftDirty, setNewTaskDraftDirty] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const pendingNavigation = useRef<(() => void) | undefined>(undefined);
  const draftDirtyRef = useRef(false);
  const detailDraftDirtyRef = useRef(false);
  const newTaskDraftDirtyRef = useRef(false);
  const activeSurfaceRef = useRef<HistorySurface | undefined>(undefined);
  const suppressNextPop = useRef(false);
  const hydratedWorkspaces = useRef(new Set<string>());
  const state = snapshot.projection;
  const activeSurface: HistorySurface | undefined = newTask ? "new-task" : mobileThreadOpen ? "thread" : undefined;
  activeSurfaceRef.current = activeSurface;
  detailDraftDirtyRef.current = detailDraftDirty;
  newTaskDraftDirtyRef.current = newTaskDraftDirty;
  draftDirtyRef.current = detailDraftDirty || newTaskDraftDirty;

  const nodes = useMemo(() => Object.values(state.nodes).sort((left, right) => (left.name ?? left.nodeId).localeCompare(right.name ?? right.nodeId)), [state, snapshot.revision]);
  const agents = useMemo(() => Object.values(state.agents).sort((left, right) => left.displayName.localeCompare(right.displayName)), [state, snapshot.revision]);
  const allWorkspaces = useMemo(() => Object.values(state.workspaces), [state, snapshot.revision]);
  const workspaces = useMemo(() => allWorkspaces.filter((workspace) => !selectedNodeId || workspace.nodeId === selectedNodeId), [allWorkspaces, selectedNodeId]);
  const tasks = useMemo(() => selectTasks(state, readSequences), [state, readSequences, snapshot.revision]);
  const filteredTasks = useMemo(() => filterTasks(tasks, filter, query, taskNodeFilter, taskWorkspaceFilter), [tasks, filter, query, taskNodeFilter, taskWorkspaceFilter]);
  const groups = useMemo(() => selectHomeGroups(tasks), [tasks]);
  const notifications = useMemo(() => selectNotifications(state), [state, snapshot.revision]);
  const unread = notifications.filter((notification) => !notification.read).length;
  const taskResource = useMemo(() => summarizeTaskResources(snapshot.resources), [snapshot.resources]);

  const navigate = useCallback((action: () => void) => {
    if (!draftDirtyRef.current) {
      action();
      return;
    }
    pendingNavigation.current = action;
    setConfirmDiscard(true);
  }, []);

  const handleDetailDraftChange = useCallback((dirty: boolean) => {
    detailDraftDirtyRef.current = dirty;
    draftDirtyRef.current = dirty || newTaskDraftDirtyRef.current;
    setDetailDraftDirty(dirty);
  }, []);

  const handleNewTaskDraftChange = useCallback((dirty: boolean) => {
    newTaskDraftDirtyRef.current = dirty;
    draftDirtyRef.current = dirty || detailDraftDirtyRef.current;
    setNewTaskDraftDirty(dirty);
  }, []);

  const closeSurface = useCallback((surface: HistorySurface) => {
    if (surface === "new-task") {
      setNewTask(undefined);
      setNewTaskDraftDirty(false);
      return;
    }
    setMobileThreadOpen(false);
    setDetailDraftDirty(false);
  }, []);

  const leaveSurface = useCallback((surface: HistorySurface, next?: () => void) => {
    const finish = () => {
      closeSurface(surface);
      next?.();
    };
    if (window.history.state?.yuanshuWorkbench === surface) {
      suppressNextPop.current = true;
      window.history.back();
      finish();
      return;
    }
    finish();
  }, [closeSurface]);

  useEffect(() => {
    if (!detailDraftDirty && !newTaskDraftDirty) return;
    const protect = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", protect);
    return () => window.removeEventListener("beforeunload", protect);
  }, [detailDraftDirty, newTaskDraftDirty]);

  const markTaskRead = useCallback((key: string, sequence: number) => {
    setReadSequences((current) => {
      if ((current[key] ?? -1) >= sequence) return current;
      const next = { ...current, [key]: sequence };
      writeTaskProgress(next);
      return next;
    });
  }, []);

  useEffect(() => {
    if (!nodes.length) return;
    if (!selectedNodeId || !nodes.some((node) => node.nodeId === selectedNodeId)) setSelectedNodeId(nodes[0].nodeId);
  }, [nodes, selectedNodeId]);

  useEffect(() => {
    if (!selectedNodeId) return;
	const availableAgents = agents.filter((agent) => agent.nodeId === selectedNodeId);
	if (availableAgents.length && (!selectedAgentInstanceId || !availableAgents.some((agent) => agent.agentInstanceId === selectedAgentInstanceId))) setSelectedAgentInstanceId(availableAgents[0].agentInstanceId);
  }, [agents, selectedAgentInstanceId, selectedNodeId]);

  useEffect(() => {
    if (!selectedNodeId) return;
    const available = allWorkspaces.filter((workspace) => workspace.nodeId === selectedNodeId);
    if (!available.length) return;
    if (!selectedWorkspaceId || !available.some((workspace) => workspace.workspaceId === selectedWorkspaceId)) setSelectedWorkspaceId(available[0].workspaceId);
  }, [allWorkspaces, selectedNodeId, selectedWorkspaceId]);

  useEffect(() => {
    const additions: Record<string, number> = {};
    for (const workspace of allWorkspaces) {
      const workspaceKey = `${workspace.nodeId}\u001f${workspace.workspaceId}`;
	  const agentIDs = workspace.agentInstanceIds ?? [];
	  const resource = agentIDs.map((agentID) => snapshot.resources[resourceKey.tasks(workspace.nodeId, agentID, workspace.workspaceId)]).find((value) => value?.state === "ready" || value?.state === "empty");
      if (resource?.state !== "ready" && resource?.state !== "empty") continue;
      const baseline = !hydratedWorkspaces.current.has(workspaceKey);
      for (const thread of Object.values(state.threads)) {
        if (thread.nodeId !== workspace.nodeId || thread.workspaceId !== workspace.workspaceId || readSequences[thread.key] !== undefined) continue;
        additions[thread.key] = baseline ? thread.latestSequence : Math.max(0, (thread.firstObservedSequence ?? thread.latestSequence) - 1);
      }
      hydratedWorkspaces.current.add(workspaceKey);
    }
    if (!Object.keys(additions).length) return;
    setReadSequences((current) => {
      const next = { ...current };
      for (const [key, sequence] of Object.entries(additions)) if (next[key] === undefined) next[key] = sequence;
      writeTaskProgress(next);
      return next;
    });
  }, [allWorkspaces, readSequences, snapshot.resources, snapshot.revision, state.threads]);

  useEffect(() => {
    if (!selectedNodeId || !selectedWorkspaceId) return;
    writeContext({ nodeId: selectedNodeId, workspaceId: selectedWorkspaceId, threadId: selectedThreadId });
  }, [selectedNodeId, selectedWorkspaceId, selectedThreadId]);

  useEffect(() => {
    if (!restoredThreadPending.current || snapshot.connectionState !== "connected" || !selectedNodeId || !selectedWorkspaceId || !selectedThreadId) return;
    const key = `${selectedNodeId}\u001f${selectedWorkspaceId}\u001f${selectedThreadId}`;
    if (!state.threads[key]) return;
    restoredThreadPending.current = false;
    void session.loadThread(selectedNodeId, selectedWorkspaceId, selectedThreadId, true).catch(() => undefined);
  }, [selectedNodeId, selectedWorkspaceId, selectedThreadId, session, snapshot.connectionState, snapshot.revision, state.threads]);

  useEffect(() => {
    const created = snapshot.createdThread;
    if (!created || created.messageId !== confirmedThreadStart) return;
    setSelectedNodeId(created.nodeId);
    setSelectedWorkspaceId(created.workspaceId);
    setSelectedThreadId(created.threadId);
    setMobileThreadOpen(true);
    if (window.history.state?.yuanshuWorkbench !== "thread") window.history.pushState({ yuanshuWorkbench: "thread" }, "");
    const createdKey = `${created.nodeId}\u001f${created.workspaceId}\u001f${created.threadId}`;
    markTaskRead(createdKey, state.threads[createdKey]?.latestSequence ?? 0);
    setConfirmedThreadStart("");
    void session.loadThread(created.nodeId, created.workspaceId, created.threadId, true).catch(() => undefined);
    session.clearCreatedThread(created.messageId);
  }, [confirmedThreadStart, markTaskRead, snapshot.createdThread, session, state.threads]);

  useEffect(() => {
    const onPopState = () => {
      const surface = activeSurfaceRef.current;
      if (!surface) return;
      if (suppressNextPop.current) {
        suppressNextPop.current = false;
        return;
      }
      if (draftDirtyRef.current) {
        window.history.pushState({ yuanshuWorkbench: surface }, "");
        pendingNavigation.current = () => leaveSurface(surface);
        setConfirmDiscard(true);
        return;
      }
      closeSurface(surface);
    };
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, [closeSurface, leaveSurface]);

  const openThread = (task: TaskSummary, pushHistory = true, afterOpen?: () => void) => {
    navigate(() => {
      const { thread } = task;
      setSelectedNodeId(thread.nodeId);
      setSelectedWorkspaceId(thread.workspaceId);
      setSelectedThreadId(thread.threadId);
      setMobileThreadOpen(true);
      markTaskRead(thread.key, thread.latestSequence);
      if (pushHistory && activeSurfaceRef.current !== "thread") window.history.pushState({ yuanshuWorkbench: "thread" }, "");
      void session.loadThread(thread.nodeId, thread.workspaceId, thread.threadId).then(afterOpen).catch(() => undefined);
    });
  };

  const openNewTask = (initialTarget?: NewTaskTarget) => navigate(() => {
    setNewTask({ initialTarget });
    setNewTaskDraftDirty(false);
    if (window.history.state?.yuanshuWorkbench !== "new-task") window.history.pushState({ yuanshuWorkbench: "new-task" }, "");
  });

  const openNotification = async (notification: (typeof notifications)[number]) => {
    const task = notification.threadId ? tasks.find((candidate) => candidate.thread.nodeId === notification.nodeId && candidate.thread.workspaceId === notification.workspaceId && candidate.thread.threadId === notification.threadId) : undefined;
    if (task) {
      openThread(task, true, () => void session.markNotificationRead(notification.id).catch(() => undefined));
      return;
    }
    if (notification.threadId && notification.workspaceId) {
      navigate(() => {
        setSelectedNodeId(notification.nodeId);
        setSelectedWorkspaceId(notification.workspaceId!);
        setSelectedThreadId(notification.threadId!);
        setMobileThreadOpen(true);
        window.history.pushState({ yuanshuWorkbench: "thread" }, "");
        void session.loadThread(notification.nodeId, notification.workspaceId!, notification.threadId!, true).then(() => session.markNotificationRead(notification.id)).catch(() => undefined);
      });
      return;
    }
    if (!notification.threadId) await session.markNotificationRead(notification.id).catch(() => undefined);
  };

  const selectScreen = (next: Screen) => navigate(() => {
    const apply = () => {
      setScreen(next);
      setMobileThreadOpen(false);
    };
    const surface = activeSurfaceRef.current;
    if (surface) leaveSurface(surface, apply); else apply();
  });

  const selectDeviceWorkspace = (nodeId: string, workspaceId: string) => navigate(() => {
    const apply = () => {
      setSelectedNodeId(nodeId);
      setSelectedWorkspaceId(workspaceId);
      setSelectedThreadId("");
      setTaskNodeFilter(nodeId);
      setTaskWorkspaceFilter(workspaceId);
    };
    const surface = activeSurfaceRef.current;
    if (surface) leaveSurface(surface, apply); else apply();
  });

  return <main className={`workbench-shell ${mobileThreadOpen ? "thread-open" : ""}`}>
    <header className="workbench-topbar">
      <div className="brand-lockup"><BrandMark /><div><strong>远枢</strong><span>Codex task relay</span></div></div>
      <nav className="desktop-nav" aria-label="工作台导航"><button type="button" className={screen === "home" ? "active" : ""} onClick={() => selectScreen("home")}>{t("workbench.nav.home")}</button><button type="button" className={screen === "tasks" ? "active" : ""} onClick={() => selectScreen("tasks")}>{t("workbench.nav.tasks")}</button><button type="button" className={screen === "devices" ? "active" : ""} onClick={() => selectScreen("devices")}>{t("workbench.nav.devices")}</button><button type="button" className={screen === "settings" ? "active" : ""} onClick={() => selectScreen("settings")}>{t("workbench.nav.settings")}</button></nav>
      <LanguageSwitch compact />
      <button className={`topbar-attention ${screen === "notifications" ? "active" : ""}`} type="button" onClick={() => selectScreen("notifications")} aria-label={`待办通知 ${unread}`}><Icon name="bell" />{unread > 0 && <span>{unread}</span>}</button>
      <button className={`connection-state ${snapshot.connectionState}`} type="button" onClick={() => void session.refreshAll()} aria-label="刷新连接状态"><span className="semantic-state" />{connectionLabel(snapshot.connectionState)}</button>
    </header>

    {snapshot.connectionState === "reauth_required" && <div className="reauth-banner" role="alert"><Icon name="lock" /><div><b>当前浏览器需要重新配对</b><span>现有身份已失效；重新授权前，任务仍可在设备上继续运行。</span></div><a className="button warning" href={settings.pairingUrl}>重新配对</a></div>}

    {screen === "notifications" ? <NotificationsView notifications={notifications} resource={snapshot.resources[resourceKey.notifications]} onRefresh={() => void session.refreshNotifications()} onOpen={(notification) => void openNotification(notification)} />
      : screen === "settings" ? <SettingsView session={session} storage={storage} settings={settings} connectionState={snapshot.connectionState} selectedNodeId={selectedNodeId} onSettingsSaved={onSettingsSaved} />
      : screen === "devices" ? <DevicesView nodes={nodes} agents={agents} workspaces={allWorkspaces} selectedNodeId={selectedNodeId} selectedAgentInstanceId={selectedAgentInstanceId} selectedWorkspaceId={selectedWorkspaceId} onNode={(nodeId) => navigate(() => { setSelectedNodeId(nodeId); setSelectedAgentInstanceId(""); setSelectedWorkspaceId(""); })} onAgent={(agentInstanceId) => navigate(() => { setSelectedAgentInstanceId(agentInstanceId); setSelectedWorkspaceId(""); })} onWorkspace={selectDeviceWorkspace} onNewTask={(nodeId, agentInstanceId, workspaceId) => openNewTask({ nodeId, agentInstanceId, workspaceId })} onShowTasks={() => { setTaskNodeFilter(selectedNodeId); setTaskWorkspaceFilter(selectedWorkspaceId); selectScreen("tasks"); }} />
      : <div className="workbench-grid">
        <aside className="task-sidebar" aria-label="任务列表与上下文">
          <TaskContextBar nodes={nodes} workspaces={workspaces} selectedNodeId={selectedNodeId} selectedWorkspaceId={selectedWorkspaceId} onNode={(nodeId) => navigate(() => { setSelectedNodeId(nodeId); setSelectedWorkspaceId(""); setSelectedThreadId(""); })} onWorkspace={(workspaceId) => navigate(() => { setSelectedWorkspaceId(workspaceId); setSelectedThreadId(""); })} />
          <section className="task-pane">
          <TaskDataState resource={taskResource} hasTasks={tasks.length > 0} onRetry={() => void session.refreshAll()} />
          {screen === "home" ? <HomeView groups={groups} nodes={nodes} unreadNotifications={unread} onOpen={openThread} onNew={() => openNewTask()} onNotifications={() => selectScreen("notifications")} /> : <TasksView tasks={filteredTasks} allTasks={tasks} nodes={nodes} workspaces={allWorkspaces} filter={filter} query={query} selectedNodeId={taskNodeFilter} selectedWorkspaceId={taskWorkspaceFilter} onFilter={setFilter} onQuery={setQuery} onNode={(nodeId) => { setTaskNodeFilter(nodeId); setTaskWorkspaceFilter(""); }} onWorkspace={setTaskWorkspaceFilter} onOpen={openThread} onNew={() => openNewTask()} />}
          </section>
        </aside>
        <ThreadDetail session={session} snapshotRevision={snapshot.revision} connectionState={snapshot.connectionState} state={state} resource={selectedThreadId ? snapshot.resources[resourceKey.thread(selectedNodeId, selectedWorkspaceId, selectedThreadId)] : undefined} selectedNodeId={selectedNodeId} selectedWorkspaceId={selectedWorkspaceId} selectedThreadId={selectedThreadId} onRead={markTaskRead} onDraftChange={handleDetailDraftChange} onBack={() => navigate(() => leaveSurface("thread"))} />
      </div>}

    {newTask && <NewTaskFlow session={session} connectionState={snapshot.connectionState} nodes={nodes} agents={agents} workspaces={allWorkspaces} initialTarget={newTask.initialTarget} onClose={() => navigate(() => leaveSurface("new-task"))} onConfirmed={(messageId) => { draftDirtyRef.current = false; setNewTaskDraftDirty(false); leaveSurface("new-task", () => setConfirmedThreadStart(messageId)); }} onDraftChange={handleNewTaskDraftChange} />}
    {confirmDiscard && <Dialog title="放弃未发送的内容？" destructive onClose={() => setConfirmDiscard(false)} actions={<><button className="button secondary" type="button" onClick={() => setConfirmDiscard(false)}>继续编辑</button><button className="button danger solid" type="button" onClick={() => { setConfirmDiscard(false); draftDirtyRef.current = false; detailDraftDirtyRef.current = false; newTaskDraftDirtyRef.current = false; setDetailDraftDirty(false); setNewTaskDraftDirty(false); const action = pendingNavigation.current; pendingNavigation.current = undefined; action?.(); }}>放弃草稿</button></>}><p>当前内容尚未发送。离开后这份草稿不会保存在浏览器中。</p></Dialog>}

    <nav className="mobile-nav" aria-label="工作台导航">
      <NavButton icon="home" label={t("workbench.nav.home")} active={screen === "home"} onClick={() => selectScreen("home")} />
      <NavButton icon="tasks" label={t("workbench.nav.tasks")} active={screen === "tasks"} onClick={() => selectScreen("tasks")} />
      <NavButton icon="node" label={t("workbench.nav.devices")} active={screen === "devices"} onClick={() => selectScreen("devices")} />
      <NavButton icon="settings" label={t("workbench.nav.settings")} active={screen === "settings"} onClick={() => selectScreen("settings")} />
    </nav>
  </main>;
}

function NotificationsView({ notifications, resource, onRefresh, onOpen }: { notifications: ReturnType<typeof selectNotifications>; resource?: ResourceState; onRefresh: () => void; onOpen: (notification: ReturnType<typeof selectNotifications>[number]) => void }) {
  return <section className="utility-view notifications-view"><div className="utility-heading"><div><p>待办与动态</p><h1>最近通知</h1></div><button className="icon-action" type="button" onClick={onRefresh} aria-label="刷新通知"><Icon name="refresh" /></button></div>{resource?.state === "loading" && !notifications.length ? <SkeletonRows count={5} /> : resource?.state === "error" && !notifications.length ? <ResourceMessage resource={resource} onRetry={onRefresh} /> : notifications.length ? <div className="notification-list">{notifications.map((notification) => <button type="button" className={notification.read ? "read" : "unread"} onClick={() => onOpen(notification)} key={notification.id}><Icon name={notificationIcon(notification.type)} /><span><b>{notificationTitle(notification.type)}</b><small>{notification.summary}</small></span><time>{formatTime(notification.createdAt)}</time></button>)}</div> : <EmptyState icon="bell" title="没有待办" detail="任务完成、失败、等待审批和设备状态变化会显示在这里。" />}</section>;
}

function TaskDataState({ resource, hasTasks, onRetry }: { resource?: ResourceState; hasTasks: boolean; onRetry: () => void }) {
  if (!resource || resource.state === "idle" || resource.state === "ready" || resource.state === "empty") return null;
  if (resource.state === "loading") return <div className="task-sync-state" role="status"><span className="semantic-state" />正在同步各工作区的任务摘要</div>;
  if (hasTasks && resource.state === "error") return <div className="task-sync-state warning" role="status"><Icon name="warning" />部分工作区同步失败，当前结果可能不完整<button type="button" onClick={onRetry}>重试</button></div>;
  return <ResourceMessage resource={resource} compact onRetry={onRetry} />;
}

function NavButton({ icon, label, active, onClick }: { icon: IconName; label: string; active: boolean; onClick: () => void }) {
  return <button type="button" className={active ? "active" : ""} aria-current={active ? "page" : undefined} onClick={onClick}><span><Icon name={icon} /></span><small>{label}</small></button>;
}

function notificationTitle(value: string) {
  return ({ "task.completed": "任务完成", "task.failed": "任务失败", "approval.required": "等待审批", "node.offline": "设备离线", "node.online": "设备上线" } as Record<string, string>)[value] ?? "状态更新";
}

function notificationIcon(value: string): IconName {
  return value === "approval.required" || value === "task.failed" ? "warning" : value.startsWith("node.") ? "node" : "check";
}

function readContext(): Partial<Selection> {
  try { const value = sessionStorage.getItem("yuanshu-workbench-context"); return value ? JSON.parse(value) as Partial<Selection> : {}; }
  catch { return {}; }
}

function writeContext(value: Selection) {
  try { sessionStorage.setItem("yuanshu-workbench-context", JSON.stringify(value)); }
  catch { /* optional browser context only */ }
}

function readTaskProgress(): Readonly<Record<string, number>> {
  try { const value = sessionStorage.getItem("yuanshu-workbench-read-progress"); return value ? JSON.parse(value) as Record<string, number> : {}; }
  catch { return {}; }
}

function writeTaskProgress(value: Readonly<Record<string, number>>) {
  try { sessionStorage.setItem("yuanshu-workbench-read-progress", JSON.stringify(value)); }
  catch { /* optional browser progress only */ }
}

function summarizeTaskResources(resources: Readonly<Record<string, ResourceState>>): ResourceState | undefined {
  const values = Object.entries(resources).filter(([key]) => key.startsWith("tasks:")).map(([, value]) => value);
  if (!values.length) return undefined;
  const error = values.find((value) => value.state === "error");
  if (error) return error;
  const stale = values.find((value) => value.state === "stale");
  if (stale) return stale;
  if (values.some((value) => value.state === "loading")) return { state: "loading" };
  const updatedAt = values.flatMap((value) => "updatedAt" in value ? [value.updatedAt] : []).sort().at(-1) ?? new Date(0).toISOString();
  return values.every((value) => value.state === "empty") ? { state: "empty", updatedAt } : { state: "ready", updatedAt };
}
