import { Icon } from "./Icon";
import type { TaskFilter, TaskSummary } from "./selectors";
import { formatTime, ResourceMessage, SkeletonRows, statusLabel, StatusPill } from "./WorkbenchPrimitives";
import type { ResourceState } from "./session";

export type DeviceSummary = { nodeId: string; name?: string; online: boolean; runtimeStatus?: string };
export type WorkspaceSummary = { key: string; nodeId: string; workspaceId: string; name?: string; permissionProfile?: string };

export function ContextRail({ nodes, workspaces, selectedNodeId, selectedWorkspaceId, resources, onNode, onWorkspace }: { nodes: DeviceSummary[]; workspaces: WorkspaceSummary[]; selectedNodeId: string; selectedWorkspaceId: string; resources: Readonly<Record<string, ResourceState>>; onNode: (value: string) => void; onWorkspace: (value: string) => void }) {
  const nodeResource = selectedNodeId ? resources[`node:${selectedNodeId}`] : undefined;
  return <aside className="context-rail" aria-label="设备和工作区">
    <div className="rail-section"><h2>设备</h2><div className="rail-list">{nodes.map((node) => <button type="button" className={node.nodeId === selectedNodeId ? "selected" : ""} key={node.nodeId} onClick={() => onNode(node.nodeId)}><span className={`node-monogram ${node.online ? "online" : "offline"}`}>{(node.name ?? node.nodeId).slice(0, 1).toUpperCase()}</span><span><b>{node.name ?? "未命名设备"}</b><small>{node.online ? statusLabel(node.runtimeStatus) : "离线"}</small></span></button>)}</div></div>
    <div className="rail-section workspaces"><div className="rail-heading"><h2>工作区</h2><span>{workspaces.length}</span></div>{nodeResource?.state === "loading" && !workspaces.length ? <SkeletonRows count={3} /> : nodeResource?.state === "error" && !workspaces.length ? <ResourceMessage resource={nodeResource} compact /> : <div className="rail-list">{workspaces.map((workspace) => <button type="button" className={workspace.workspaceId === selectedWorkspaceId ? "selected" : ""} key={workspace.key} onClick={() => onWorkspace(workspace.workspaceId)}><Icon name="folder" /><span><b>{workspace.name ?? workspace.workspaceId}</b><small>{permissionLabel(workspace.permissionProfile)}</small></span></button>)}</div>}</div>
    <p className="privacy-note">任务内容保留在设备。本页面只接收经过授权的结构化事件。</p>
  </aside>;
}

export function HomeView({ groups, nodes, unreadNotifications, onOpen, onNew, onNotifications }: { groups: { continuation?: TaskSummary; approvals: TaskSummary[]; issues: TaskSummary[]; active: TaskSummary[]; recent: TaskSummary[] }; nodes: DeviceSummary[]; unreadNotifications: number; onOpen: (task: TaskSummary) => void; onNew: () => void; onNotifications: () => void }) {
  const warnings = nodes.filter((node) => !node.online || ["unavailable", "not_available"].includes(node.runtimeStatus ?? ""));
  return <div className="task-view home-view">
    <div className="task-heading"><div><p>任务接力</p><h1>继续你的 Codex 工作</h1></div><div className="heading-actions"><button className="attention-button" type="button" onClick={onNotifications} aria-label={`待办通知 ${unreadNotifications}`}><Icon name="bell" />{unreadNotifications > 0 && <span>{unreadNotifications}</span>}</button><button className="button primary" type="button" onClick={onNew}><Icon name="plus" />新任务</button></div></div>
    {groups.continuation ? <ContinueTaskCard task={groups.continuation} onOpen={() => onOpen(groups.continuation!)} /> : <div className="continue-empty"><Icon name="check" /><div><b>当前没有需要接续的任务</b><p>设备上的任务开始运行或等待审批时，会优先出现在这里。</p></div></div>}
    {warnings.length > 0 && <div className="health-banner"><Icon name="warning" /><div><b>{warnings.length} 台设备需要注意</b><p>离线或 Codex 不可用的任务仍可查看，恢复后会继续同步。</p></div></div>}
    {groups.approvals.length > 0 && <TaskGroup title="等待我审批" tasks={groups.approvals} onOpen={onOpen} tone="warning" />}
    {groups.issues.length > 0 && <TaskGroup title="需要确认" tasks={groups.issues} onOpen={onOpen} tone="warning" />}
    {groups.active.length > 0 && <TaskGroup title="其它运行中任务" tasks={groups.active} onOpen={onOpen} />}
    <TaskGroup title="最近完成" tasks={groups.recent} empty="完成的任务会保留在这里，方便查看结果和文件变化。" onOpen={onOpen} />
  </div>;
}

function ContinueTaskCard({ task, onOpen }: { task: TaskSummary; onOpen: () => void }) {
  const status = task.latestTurn?.status ?? task.thread.status;
  const tone = task.pendingApprovals > 0 ? "warning" : ["failed", "uncertain", "ambiguous"].includes(status ?? "") ? "danger" : "accent";
  return <button type="button" className={`continue-card ${tone}`} onClick={onOpen} aria-label={`继续任务 ${taskTitle(task)}`}>
    <div className="continue-card-top"><StatusPill tone={tone}>{task.pendingApprovals > 0 ? "等待审批" : statusLabel(status)}</StatusPill><time>{formatTime(task.thread.updatedAt)}</time></div>
    <strong>{taskTitle(task)}</strong>
    {task.thread.preview && task.thread.title && <p>{task.thread.preview}</p>}
    <div className="continue-card-meta"><span><Icon name="node" />{task.node?.name ?? "未命名设备"}</span><span><Icon name="folder" />{task.workspace?.name ?? "工作区"}</span>{task.unreadCount > 0 && <em>{task.unreadCount} 条新进展</em>}</div>
    <span className="continue-action">继续查看 <Icon name="chevron" /></span>
  </button>;
}

export function TasksView({ tasks, allTasks, nodes, workspaces, filter, query, selectedNodeId, selectedWorkspaceId, onFilter, onQuery, onNode, onWorkspace, onOpen, onNew }: { tasks: TaskSummary[]; allTasks: TaskSummary[]; nodes: Array<{ nodeId: string; name?: string }>; workspaces: Array<{ nodeId: string; workspaceId: string; name?: string }>; filter: TaskFilter; query: string; selectedNodeId: string; selectedWorkspaceId: string; onFilter: (value: TaskFilter) => void; onQuery: (value: string) => void; onNode: (value: string) => void; onWorkspace: (value: string) => void; onOpen: (task: TaskSummary) => void; onNew: () => void }) {
  return <div className="task-view"><div className="task-heading"><div><p>任务</p><h1>全部任务</h1></div><button className="button primary" type="button" onClick={onNew}><Icon name="plus" />新任务</button></div>
    <div className="task-controls"><label className="search-field"><span className="sr-only">搜索任务</span><Icon name="search" /><input value={query} onChange={(event) => onQuery(event.target.value)} placeholder="搜索已同步的标题和摘要" /></label><div className="context-selects"><select aria-label="筛选设备" value={selectedNodeId} onChange={(event) => { onNode(event.target.value); onWorkspace(""); }}><option value="">全部设备</option>{nodes.map((node) => <option value={node.nodeId} key={node.nodeId}>{node.name ?? node.nodeId}</option>)}</select><select aria-label="筛选工作区" value={selectedWorkspaceId} onChange={(event) => onWorkspace(event.target.value)}><option value="">全部工作区</option>{workspaces.filter((workspace) => !selectedNodeId || workspace.nodeId === selectedNodeId).map((workspace) => <option value={workspace.workspaceId} key={`${workspace.nodeId}:${workspace.workspaceId}`}>{workspace.name ?? workspace.workspaceId}</option>)}</select></div></div>
    <div className="filter-tabs" role="tablist" aria-label="任务状态">{(["all", "active", "approval", "failed", "completed"] as const).map((value) => <button type="button" role="tab" aria-selected={filter === value} className={filter === value ? "active" : ""} onClick={() => onFilter(value)} key={value}>{filterLabel(value)}</button>)}</div>
    <p className="local-search-note">仅搜索当前浏览器已同步的 {allTasks.length} 个任务摘要。</p>
    <div className="task-list">{tasks.map((task) => <TaskRow task={task} onOpen={() => onOpen(task)} key={task.thread.key} />)}{!tasks.length && <EmptyTaskList />}</div>
  </div>;
}

export function DevicesView({ nodes, workspaces, selectedNodeId, selectedWorkspaceId, onNode, onWorkspace, onShowTasks }: { nodes: DeviceSummary[]; workspaces: WorkspaceSummary[]; selectedNodeId: string; selectedWorkspaceId: string; onNode: (value: string) => void; onWorkspace: (nodeId: string, workspaceId: string) => void; onShowTasks: () => void }) {
  return <section className="utility-view devices-view"><div className="utility-heading"><div><p>执行位置</p><h1>设备与工作区</h1></div></div><p className="utility-intro">选择设备和工作区后查看对应任务。工作区权限仍由设备本地配置决定。</p><div className="device-card-list">{nodes.map((node) => {
    const nodeWorkspaces = workspaces.filter((workspace) => workspace.nodeId === node.nodeId);
    return <article className={`device-card ${node.nodeId === selectedNodeId ? "selected" : ""}`} key={node.nodeId}><button className="device-card-heading" type="button" onClick={() => onNode(node.nodeId)}><span className={`node-monogram ${node.online ? "online" : "offline"}`}>{(node.name ?? node.nodeId).slice(0, 1).toUpperCase()}</span><span><b>{node.name ?? "未命名设备"}</b><small>{node.online ? statusLabel(node.runtimeStatus) : "离线"}</small></span><StatusPill tone={node.online ? "accent" : "quiet"}>{node.online ? "在线" : "离线"}</StatusPill></button><div className="device-workspaces">{nodeWorkspaces.map((workspace) => <button type="button" className={workspace.nodeId === selectedNodeId && workspace.workspaceId === selectedWorkspaceId ? "selected" : ""} onClick={() => onWorkspace(node.nodeId, workspace.workspaceId)} key={workspace.key}><Icon name="folder" /><span><b>{workspace.name ?? workspace.workspaceId}</b><small>{permissionLabel(workspace.permissionProfile)}</small></span><Icon name="chevron" /></button>)}{!nodeWorkspaces.length && <p>该设备尚未同步工作区。</p>}</div></article>;
  })}</div><button className="button primary devices-task-action" type="button" disabled={!selectedNodeId || !selectedWorkspaceId} onClick={onShowTasks}>查看所选工作区任务</button></section>;
}

function TaskGroup({ title, tasks, empty, tone, onOpen }: { title: string; tasks: TaskSummary[]; empty?: string; tone?: "warning"; onOpen: (task: TaskSummary) => void }) {
  return <section className={`task-group ${tone ?? ""}`}><div className="group-heading"><h2>{title}</h2><span>{tasks.length}</span></div>{tasks.length ? <div className="task-list">{tasks.map((task) => <TaskRow task={task} onOpen={() => onOpen(task)} key={task.thread.key} />)}</div> : empty ? <p className="group-empty">{empty}</p> : null}</section>;
}

function TaskRow({ task, onOpen }: { task: TaskSummary; onOpen: () => void }) {
  const status = task.latestTurn?.status ?? task.thread.status;
  return <button type="button" className="task-row" onClick={onOpen} aria-label={`${taskTitle(task)}，${task.node?.name ?? "未命名设备"}，${task.workspace?.name ?? "工作区"}`}><span className={`task-status ${status ?? "unknown"}`} aria-hidden="true" /><span className="task-copy"><b>{taskTitle(task)}</b><small>{task.node?.name ?? "未命名设备"} · {task.workspace?.name ?? "工作区"}</small></span><span className="task-side">{task.unreadCount > 0 && <strong>{task.unreadCount} 条新进展</strong>}<em>{task.pendingApprovals ? "待审批" : statusLabel(status)}</em><time>{formatTime(task.thread.updatedAt)}</time></span></button>;
}

function EmptyTaskList() {
  return <div className="empty-state"><Icon name="tasks" /><b>没有匹配的任务</b><p>调整状态、设备、工作区或搜索条件。</p></div>;
}

function taskTitle(task: TaskSummary) {
  return task.thread.title || task.thread.preview || `任务 ${task.thread.threadId.slice(0, 8)}`;
}

function filterLabel(value: TaskFilter) {
  return ({ all: "全部", active: "运行中", approval: "待审批", failed: "失败", completed: "已完成" })[value];
}

function permissionLabel(value?: string) {
  return value === "workspace-write" || value === "workspaceWrite" ? "可修改工作区" : "只读";
}
