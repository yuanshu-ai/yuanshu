import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";

import type { RuntimeSettings } from "../relay/runtime-config";
import type { ControlStorage } from "../relay/storage";
import { Icon, type IconName } from "./Icon";
import { selectHomeGroups, selectNotifications, selectTasks, filterTasks, type TaskFilter, type TaskSummary } from "./selectors";
import { SettingsView } from "./Settings";
import { resourceKey, type ResourceState, type WorkbenchSession } from "./session";
import { ContextRail, DevicesView, HomeView, TasksView } from "./TaskViews";
import { ThreadDetail } from "./ThreadDetail";
import { connectionLabel, EmptyState, formatTime, ResourceMessage, SkeletonRows } from "./WorkbenchPrimitives";

type Screen = "home" | "tasks" | "devices" | "notifications" | "settings";
type Selection = { nodeId: string; workspaceId: string; threadId: string };

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
  const [readSequences, setReadSequences] = useState<Readonly<Record<string, number>>>(() => readTaskProgress());
  const state = snapshot.projection;

  const nodes = useMemo(() => Object.values(state.nodes).sort((left, right) => (left.name ?? left.nodeId).localeCompare(right.name ?? right.nodeId)), [state, snapshot.revision]);
  const allWorkspaces = useMemo(() => Object.values(state.workspaces), [state, snapshot.revision]);
  const workspaces = useMemo(() => allWorkspaces.filter((workspace) => !selectedNodeId || workspace.nodeId === selectedNodeId), [allWorkspaces, selectedNodeId]);
  const tasks = useMemo(() => selectTasks(state, readSequences), [state, readSequences, snapshot.revision]);
  const filteredTasks = useMemo(() => filterTasks(tasks, filter, query, taskNodeFilter, taskWorkspaceFilter), [tasks, filter, query, taskNodeFilter, taskWorkspaceFilter]);
  const groups = useMemo(() => selectHomeGroups(tasks), [tasks]);
  const notifications = useMemo(() => selectNotifications(state), [state, snapshot.revision]);
  const unread = notifications.filter((notification) => !notification.read).length;
  const taskResource = useMemo(() => summarizeTaskResources(snapshot.resources), [snapshot.resources]);

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
    const available = allWorkspaces.filter((workspace) => workspace.nodeId === selectedNodeId);
    if (!available.length) return;
    if (!selectedWorkspaceId || !available.some((workspace) => workspace.workspaceId === selectedWorkspaceId)) setSelectedWorkspaceId(available[0].workspaceId);
  }, [allWorkspaces, selectedNodeId, selectedWorkspaceId]);

  useEffect(() => {
    const missing = Object.values(state.threads).filter((thread) => readSequences[thread.key] === undefined);
    if (!missing.length) return;
    setReadSequences((current) => {
      const next = { ...current };
      for (const thread of missing) if (next[thread.key] === undefined) next[thread.key] = thread.latestSequence;
      writeTaskProgress(next);
      return next;
    });
  }, [readSequences, snapshot.revision, state.threads]);

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
    markTaskRead(thread.key, thread.latestSequence);
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

  const selectDeviceWorkspace = (nodeId: string, workspaceId: string) => {
    setSelectedNodeId(nodeId);
    setSelectedWorkspaceId(workspaceId);
    setSelectedThreadId("");
    setTaskNodeFilter(nodeId);
    setTaskWorkspaceFilter(workspaceId);
  };

  return <main className={`workbench-shell ${mobileThreadOpen ? "thread-open" : ""}`}>
    <header className="workbench-topbar">
      <div className="brand-lockup"><span className="brand-mark">枢</span><div><strong>远枢</strong><span>Codex task relay</span></div></div>
      <nav className="desktop-nav" aria-label="工作台导航"><button type="button" className={screen === "home" ? "active" : ""} onClick={() => selectScreen("home")}>首页</button><button type="button" className={screen === "tasks" ? "active" : ""} onClick={() => selectScreen("tasks")}>任务</button><button type="button" className={screen === "devices" ? "active" : ""} onClick={() => selectScreen("devices")}>设备</button><button type="button" className={screen === "settings" ? "active" : ""} onClick={() => selectScreen("settings")}>设置</button></nav>
      <button className={`topbar-attention ${screen === "notifications" ? "active" : ""}`} type="button" onClick={() => selectScreen("notifications")} aria-label={`待办通知 ${unread}`}><Icon name="bell" />{unread > 0 && <span>{unread}</span>}</button>
      <button className={`connection-state ${snapshot.connectionState}`} type="button" onClick={() => void session.refreshAll()} aria-label="刷新连接状态"><span className="semantic-state" />{connectionLabel(snapshot.connectionState)}</button>
    </header>

    {screen === "notifications" ? <NotificationsView notifications={notifications} resource={snapshot.resources[resourceKey.notifications]} onRefresh={() => void session.refreshNotifications()} onOpen={(notification) => void openNotification(notification)} />
      : screen === "settings" ? <SettingsView session={session} storage={storage} settings={settings} selectedNodeId={selectedNodeId} onSettingsSaved={onSettingsSaved} />
      : screen === "devices" ? <DevicesView nodes={nodes} workspaces={allWorkspaces} selectedNodeId={selectedNodeId} selectedWorkspaceId={selectedWorkspaceId} onNode={(nodeId) => { setSelectedNodeId(nodeId); setSelectedWorkspaceId(""); }} onWorkspace={selectDeviceWorkspace} onShowTasks={() => selectScreen("tasks")} />
      : <div className="workbench-grid">
        <ContextRail nodes={nodes} workspaces={workspaces} selectedNodeId={selectedNodeId} selectedWorkspaceId={selectedWorkspaceId} resources={snapshot.resources} onNode={(nodeId) => { setSelectedNodeId(nodeId); setSelectedWorkspaceId(""); setSelectedThreadId(""); }} onWorkspace={(workspaceId) => { setSelectedWorkspaceId(workspaceId); setSelectedThreadId(""); }} />
        <section className="task-pane">
          <TaskDataState resource={taskResource} hasTasks={tasks.length > 0} onRetry={() => void session.refreshAll()} />
          {screen === "home" ? <HomeView groups={groups} nodes={nodes} unreadNotifications={unread} onOpen={(task) => void openThread(task)} onNew={openNewTask} onNotifications={() => selectScreen("notifications")} /> : <TasksView tasks={filteredTasks} allTasks={tasks} nodes={nodes} workspaces={allWorkspaces} filter={filter} query={query} selectedNodeId={taskNodeFilter} selectedWorkspaceId={taskWorkspaceFilter} onFilter={setFilter} onQuery={setQuery} onNode={(nodeId) => { setTaskNodeFilter(nodeId); setTaskWorkspaceFilter(""); if (nodeId) setSelectedNodeId(nodeId); }} onWorkspace={(workspaceId) => { setTaskWorkspaceFilter(workspaceId); if (workspaceId) setSelectedWorkspaceId(workspaceId); }} onOpen={(task) => void openThread(task)} onNew={openNewTask} />}
        </section>
        <ThreadDetail session={session} snapshotRevision={snapshot.revision} connectionState={snapshot.connectionState} state={state} resource={selectedThreadId ? snapshot.resources[resourceKey.thread(selectedNodeId, selectedWorkspaceId, selectedThreadId)] : undefined} selectedNodeId={selectedNodeId} selectedWorkspaceId={selectedWorkspaceId} selectedThreadId={selectedThreadId} onRead={markTaskRead} onBack={() => { if (window.history.state?.yuanshuWorkbench === "thread") window.history.back(); else setMobileThreadOpen(false); }} />
      </div>}

    <nav className="mobile-nav" aria-label="工作台导航">
      <NavButton icon="home" label="首页" active={screen === "home"} onClick={() => selectScreen("home")} />
      <NavButton icon="tasks" label="任务" active={screen === "tasks"} onClick={() => selectScreen("tasks")} />
      <NavButton icon="node" label="设备" active={screen === "devices"} onClick={() => selectScreen("devices")} />
      <NavButton icon="settings" label="设置" active={screen === "settings"} onClick={() => selectScreen("settings")} />
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
