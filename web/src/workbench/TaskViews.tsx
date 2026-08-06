import { Icon } from "./Icon";
import { Card } from "@yuanshu/ui/base";
import { useI18n } from "../i18n";
import { canStartTask, type TaskFilter, type TaskSummary } from "./selectors";
import { formatTime, ResourceMessage, SkeletonRows, statusLabel, StatusPill } from "./WorkbenchPrimitives";
import type { ResourceState } from "./session";
import type { AgentProjection } from "../state/projection";

export type DeviceSummary = { nodeId: string; name?: string; online: boolean; runtimeStatus?: string };
export type WorkspaceSummary = { key: string; nodeId: string; workspaceId: string; name?: string; permissionProfile?: string; allowNetwork?: boolean; agentInstanceIds?: string[] };

export function TaskContextBar({ nodes, workspaces, selectedNodeId, selectedWorkspaceId, onNode, onWorkspace, onDevices }: { nodes: DeviceSummary[]; workspaces: WorkspaceSummary[]; selectedNodeId: string; selectedWorkspaceId: string; onNode: (value: string) => void; onWorkspace: (value: string) => void; onDevices?: () => void }) {
  const { t } = useI18n();
  const node = nodes.find((item) => item.nodeId === selectedNodeId);
  const visibleWorkspaces = selectedNodeId ? workspaces.filter((item) => item.nodeId === selectedNodeId) : workspaces;
  return <div className="task-context-bar">
    <label>{node && <span className={`context-presence ${node.online ? "online" : "offline"}`} aria-hidden="true" />}<span className="sr-only">{t("workbench.context.node")}</span><select aria-label={t("workbench.context.node")} value={selectedNodeId} onChange={(event) => onNode(event.target.value)}><option value="">全部设备</option>{nodes.map((item) => <option value={item.nodeId} key={item.nodeId}>{item.name ?? item.nodeId}</option>)}</select></label>
    <label><Icon name="folder" /><span className="sr-only">{t("workbench.context.workspace")}</span><select aria-label={t("workbench.context.workspace")} value={selectedWorkspaceId} onChange={(event) => onWorkspace(event.target.value)}><option value="">全部工作区</option>{visibleWorkspaces.map((item) => <option value={item.workspaceId} key={item.key}>{item.name ?? item.workspaceId}</option>)}</select></label>
    {onDevices && <button className="context-devices" type="button" onClick={onDevices} aria-label={t("workbench.nav.devices")}><Icon name="node" /><span>{t("workbench.nav.devices")}</span></button>}
  </div>;
}

export function ContextRail({ nodes, workspaces, selectedNodeId, selectedWorkspaceId, resources, onNode, onWorkspace }: { nodes: DeviceSummary[]; workspaces: WorkspaceSummary[]; selectedNodeId: string; selectedWorkspaceId: string; resources: Readonly<Record<string, ResourceState>>; onNode: (value: string) => void; onWorkspace: (value: string) => void }) {
  const nodeResource = selectedNodeId ? resources[`node:${selectedNodeId}`] : undefined;
  return <aside className="context-rail" aria-label="设备和工作区">
    <div className="rail-section"><h2>设备</h2><div className="rail-list">{nodes.map((node) => <button type="button" className={node.nodeId === selectedNodeId ? "selected" : ""} key={node.nodeId} onClick={() => onNode(node.nodeId)}><span className={`node-monogram ${node.online ? "online" : "offline"}`}>{(node.name ?? node.nodeId).slice(0, 1).toUpperCase()}</span><span><b>{node.name ?? "未命名设备"}</b><small>{node.online ? statusLabel(node.runtimeStatus) : "离线"}</small></span></button>)}</div></div>
    <div className="rail-section workspaces"><div className="rail-heading"><h2>工作区</h2><span>{workspaces.length}</span></div>{nodeResource?.state === "loading" && !workspaces.length ? <SkeletonRows count={3} /> : nodeResource?.state === "error" && !workspaces.length ? <ResourceMessage resource={nodeResource} compact /> : <div className="rail-list">{workspaces.map((workspace) => <button type="button" className={workspace.workspaceId === selectedWorkspaceId ? "selected" : ""} key={workspace.key} onClick={() => onWorkspace(workspace.workspaceId)}><Icon name="folder" /><span><b>{workspace.name ?? workspace.workspaceId}</b><small>{permissionLabel(workspace.permissionProfile)}</small></span></button>)}</div>}</div>
    <p className="privacy-note">任务内容保留在设备。本页面只接收经过授权的结构化事件。</p>
  </aside>;
}

export function HomeView({ groups, nodes, unreadNotifications, onOpen, onNew, onNotifications, onDevices }: { groups: { continuation?: TaskSummary; approvals: TaskSummary[]; issues: TaskSummary[]; active: TaskSummary[]; recent: TaskSummary[] }; nodes: DeviceSummary[]; unreadNotifications: number; onOpen: (task: TaskSummary) => void; onNew: () => void; onNotifications: () => void; onDevices: () => void }) {
  const { t } = useI18n();
  const warnings = nodes.filter((node) => !canStartTask(node));
  return <div className="task-view home-view">
    <div className="task-heading"><div><p>{t("workbench.task.kicker")}</p><h1>{t("workbench.task.continue")}</h1></div><div className="heading-actions"><button className="context-devices home-devices" type="button" onClick={onDevices}><Icon name="node" />{t("workbench.nav.devices")}</button><button className="attention-button" type="button" onClick={onNotifications} aria-label={`${t("workbench.nav.notifications")} ${unreadNotifications}`}><Icon name="bell" />{unreadNotifications > 0 && <span>{unreadNotifications}</span>}</button><button className="button primary" type="button" onClick={onNew}><Icon name="plus" />{t("workbench.nav.newTask")}</button></div></div>
    {groups.continuation ? <ContinueTaskCard task={groups.continuation} onOpen={() => onOpen(groups.continuation!)} /> : <div className="continue-empty"><Icon name="check" /><div><b>当前没有需要接续的任务</b><p>设备上的任务开始运行或等待审批时，会优先出现在这里。</p></div></div>}
    {warnings.length > 0 && <div className="health-banner"><Icon name="warning" /><div><b>{warnings.length} 台设备需要注意</b><p>离线或 Agent 不可用的任务仍可查看，恢复后会继续同步。</p></div></div>}
    {groups.approvals.length > 0 && <TaskGroup title="等待我审批" tasks={groups.approvals} onOpen={onOpen} tone="warning" />}
    {groups.issues.length > 0 && <TaskGroup title="需要确认" tasks={groups.issues} onOpen={onOpen} tone="warning" />}
    {groups.active.length > 0 && <TaskGroup title="其它运行中任务" tasks={groups.active} onOpen={onOpen} />}
    <TaskGroup title={t("workbench.task.recent")} tasks={groups.recent} empty="完成的任务会保留在这里，方便查看结果和文件变化。" onOpen={onOpen} />
  </div>;
}

function ContinueTaskCard({ task, onOpen }: { task: TaskSummary; onOpen: () => void }) {
  const status = task.latestTurn?.status ?? task.thread.status;
  const tone = task.pendingApprovals > 0 ? "warning" : ["failed", "uncertain", "ambiguous"].includes(status ?? "") ? "danger" : "accent";
  return <button type="button" className={`continue-card ${tone}`} onClick={onOpen} aria-label={`继续任务 ${taskTitle(task)}`}>
    <div className="continue-card-top"><StatusPill tone={tone}>{task.pendingApprovals > 0 ? "等待审批" : statusLabel(status)}</StatusPill><time>{formatTime(task.thread.updatedAt)}</time></div>
    <strong>{taskTitle(task)}</strong>
    {task.thread.preview && task.thread.title && <p>{task.thread.preview}</p>}
    <div className="continue-card-meta"><span><Icon name="tool" />{task.agent?.displayName ?? "Agent"}</span><span><Icon name="node" />{task.node?.name ?? "未命名设备"}</span><span><Icon name="folder" />{task.workspace?.name ?? "工作区"}</span>{task.unreadCount > 0 && <em>{task.unreadCount} 条新进展</em>}</div>
    <span className="continue-action">继续查看 <Icon name="chevron" /></span>
  </button>;
}

export function TasksView({ tasks, allTasks, filter, query, onFilter, onQuery, onOpen, onNew }: { tasks: TaskSummary[]; allTasks: TaskSummary[]; filter: TaskFilter; query: string; onFilter: (value: TaskFilter) => void; onQuery: (value: string) => void; onOpen: (task: TaskSummary) => void; onNew: () => void }) {
  const { t } = useI18n();
  return <div className="task-view"><div className="task-heading"><div><p>{t("workbench.task.kicker")}</p><h1>{t("workbench.task.all")}</h1></div><button className="button primary" type="button" onClick={onNew}><Icon name="plus" />{t("workbench.nav.newTask")}</button></div>
    <div className="task-controls"><label className="search-field"><span className="sr-only">{t("workbench.nav.search")}</span><Icon name="search" /><input value={query} onChange={(event) => onQuery(event.target.value)} placeholder={t("workbench.task.searchHint")} /></label></div>
    <div className="filter-tabs" role="tablist" aria-label="任务状态">{(["all", "active", "approval", "failed", "completed"] as const).map((value) => <button type="button" role="tab" aria-selected={filter === value} className={filter === value ? "active" : ""} onClick={() => onFilter(value)} key={value}>{filterLabel(value)}</button>)}</div>
    <p className="local-search-note">{t("workbench.task.localSearch", { count: allTasks.length })}</p>
    <div className="task-list">{tasks.map((task) => <TaskRow task={task} onOpen={() => onOpen(task)} key={task.thread.key} />)}{!tasks.length && <EmptyTaskList />}</div>
  </div>;
}

export function DevicesView({ nodes, agents, workspaces, selectedNodeId, selectedAgentInstanceId, selectedWorkspaceId, onNode, onAgent, onWorkspace, onNewTask, onShowTasks }: { nodes: DeviceSummary[]; agents: AgentProjection[]; workspaces: WorkspaceSummary[]; selectedNodeId: string; selectedAgentInstanceId: string; selectedWorkspaceId: string; onNode: (value: string) => void; onAgent: (value: string) => void; onWorkspace: (nodeId: string, workspaceId: string) => void; onNewTask: (nodeId: string, agentInstanceId: string, workspaceId: string) => void; onShowTasks: () => void }) {
  return <section className="utility-view devices-view"><div className="utility-heading"><div><p>资源中心</p><h1>设备与 Agent</h1></div></div><p className="utility-intro">先进入设备，再选择可控 Agent 和工作区。检测到进程不代表它的现有任务可以安全接入。</p><div className="device-card-list">{nodes.map((node) => {
    const nodeWorkspaces = workspaces.filter((workspace) => workspace.nodeId === node.nodeId);
    const nodeAgents = agents.filter((agent) => agent.nodeId === node.nodeId);
    const primaryAgents = nodeAgents.filter((agent) => agent.runtimeMode !== "detected-only");
    const detectedAgents = nodeAgents.filter((agent) => agent.runtimeMode === "detected-only");
    const controllable = canStartTask(node) ? primaryAgents.filter(agentCanCreate).length : 0;
    return <article className={`device-card ${node.nodeId === selectedNodeId ? "selected" : ""}`} key={node.nodeId}><button className="device-card-heading" type="button" onClick={() => onNode(node.nodeId)}><span className={`node-monogram ${node.online ? "online" : "offline"}`}>{(node.name ?? node.nodeId).slice(0, 1).toUpperCase()}</span><span><b>{node.name ?? "未命名设备"}</b><small>{node.online ? `${primaryAgents.length} 个 Agent · ${controllable} 个可控` : "离线"}{detectedAgents.length ? ` · ${detectedAgents.length} 个待接入` : ""}</small></span><StatusPill tone={node.online ? "accent" : "quiet"}>{node.online ? statusLabel(node.runtimeStatus) : "离线"}</StatusPill></button>{node.nodeId === selectedNodeId && <div className="device-agent-list">{primaryAgents.map((agent) => {
      const available = agentCanCreate(agent) && canStartTask(node);
      const agentWorkspaces = nodeWorkspaces.filter((workspace) => !workspace.agentInstanceIds?.length || workspace.agentInstanceIds.includes(agent.agentInstanceId));
      return <section className={`agent-card ${selectedAgentInstanceId === agent.agentInstanceId ? "selected" : ""}`} key={agent.key}><button type="button" className="agent-card-heading" onClick={() => onAgent(agent.agentInstanceId)}><span className="node-monogram agent">{agent.displayName.slice(0, 1).toUpperCase()}</span><span><b>{agent.displayName}</b><small>{agentStatusLabel(agent, available)}</small></span><StatusPill tone={available ? "accent" : "quiet"}>{available ? "可控制" : agent.runtimeMode === "history-only" ? "历史可读" : "只读"}</StatusPill></button>{selectedAgentInstanceId === agent.agentInstanceId && <><details className="agent-advanced"><summary>查看技术详情</summary><div className="agent-capabilities"><span>类型<small>{agent.adapterType}</small></span>{agent.version && <span>版本<small>{agent.version}</small></span>}<span>运行模式<small>{agent.runtimeMode}</small></span>{agent.capabilities.map((capability) => <span className={capability.level} key={capability.id}>{capability.id}<small>{capability.level}</small></span>)}</div></details><div className="device-workspaces">{agentWorkspaces.map((workspace) => <div className={`device-workspace-row ${workspace.workspaceId === selectedWorkspaceId ? "selected" : ""}`} key={workspace.key}><button type="button" className="workspace-select" onClick={() => onWorkspace(node.nodeId, workspace.workspaceId)}><Icon name="folder" /><span><b>{workspace.name ?? "未命名工作区"}</b><small>{permissionLabel(workspace.permissionProfile)} · {networkLabel(workspace.allowNetwork)}</small></span><Icon name="chevron" /></button><button type="button" className="workspace-new-task" disabled={!available} onClick={() => onNewTask(node.nodeId, agent.agentInstanceId, workspace.workspaceId)} aria-label={`使用 ${agent.displayName} 在 ${workspace.name ?? "未命名工作区"} 新建任务`}><Icon name="plus" />新建</button></div>)}{!agentWorkspaces.length && <p>该 Agent 尚未授权工作区。</p>}</div></>}</section>;
    })}{detectedAgents.length > 0 && <details className="detected-agents"><summary>检测到但尚不可接入（{detectedAgents.length}）</summary><div className="detected-agent-list">{detectedAgents.map((agent) => <article className="detected-agent" key={agent.key}><span className="node-monogram agent">{agent.displayName.slice(0, 1).toUpperCase()}</span><div><b>{agent.displayName}</b><small>检测到进程，但现有任务不能安全接入</small></div></article>)}</div><p className="agent-boundary-note">可以创建新的远枢托管任务；不会接管这个已有进程。</p></details>}{!nodeAgents.length && <p className="agent-boundary-note">该设备尚未上报 Agent 资源。</p>}</div>}</article>;
  })}</div><button className="button primary devices-task-action" type="button" disabled={!selectedNodeId || !selectedAgentInstanceId || !selectedWorkspaceId} onClick={onShowTasks}>查看所选 Agent 的任务</button></section>;
}

function agentCanCreate(agent: AgentProjection): boolean {
  return agent.runtimeMode === "managed" && agent.capabilities.some((capability) => capability.id === "task.start" && capability.level === "full");
}

function agentStatusLabel(agent: AgentProjection, available: boolean): string {
  if (available) return `${agent.displayName} 可用，可创建任务`;
  if (agent.status === "ready") return agent.runtimeMode === "attached" ? `${agent.displayName} 可查看，需本机托管` : `${agent.displayName} 当前只读`;
  return `${agent.displayName} 当前不可用`;
}

function TaskGroup({ title, tasks, empty, tone, onOpen }: { title: string; tasks: TaskSummary[]; empty?: string; tone?: "warning"; onOpen: (task: TaskSummary) => void }) {
  return <Card className={`task-group ${tone ?? ""}`}><div className="group-heading"><h2>{title}</h2><span>{tasks.length}</span></div>{tasks.length ? <div className="task-list">{tasks.map((task) => <TaskRow task={task} onOpen={() => onOpen(task)} key={task.thread.key} />)}</div> : empty ? <p className="group-empty">{empty}</p> : null}</Card>;
}

function TaskRow({ task, onOpen }: { task: TaskSummary; onOpen: () => void }) {
  const status = task.latestTurn?.status ?? task.thread.status;
  return <button type="button" className="task-row" onClick={onOpen} aria-label={`${taskTitle(task)}，${task.agent?.displayName ?? "Agent"}，${task.node?.name ?? "未命名设备"}，${task.workspace?.name ?? "工作区"}`}><span className={`task-status ${status ?? "unknown"}`} aria-hidden="true" /><span className="task-copy"><b>{taskTitle(task)}</b><small>{task.agent?.displayName ?? "Agent"} · {task.node?.name ?? "未命名设备"} · {task.workspace?.name ?? "工作区"}</small></span><span className="task-side">{task.unreadCount > 0 && <strong>{task.unreadCount} 条新进展</strong>}<em>{task.pendingApprovals ? "待审批" : statusLabel(status)}</em><time>{formatTime(task.thread.updatedAt)}</time></span></button>;
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

function networkLabel(value?: boolean) {
  return value === true ? "允许网络" : value === false ? "网络关闭" : "网络策略由本机控制";
}
