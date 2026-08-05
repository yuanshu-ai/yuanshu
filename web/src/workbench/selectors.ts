import type {
  ApprovalProjection,
  AgentProjection,
  NodeProjection,
  NotificationProjection,
  ProjectionState,
  ThreadProjection,
  TurnProjection,
  WorkspaceProjection,
} from "../state/projection";

export type TaskFilter = "all" | "active" | "approval" | "failed" | "completed";

export interface TaskSummary {
  thread: ThreadProjection;
  node?: NodeProjection;
  workspace?: WorkspaceProjection;
  agent?: AgentProjection;
  latestTurn?: TurnProjection;
  pendingApprovals: number;
  unreadCount: number;
}

const ACTIVE = new Set(["running", "active", "inProgress", "waiting_approval", "reconnecting", "uncertain", "ambiguous"]);
const RUNNING = new Set(["running", "active", "inProgress"]);
const WAITING = new Set(["idle", "waiting"]);

export function canStartTask(node?: Pick<NodeProjection, "online" | "runtimeStatus">): boolean {
  return node?.online === true && node.runtimeStatus === "ready";
}

export function taskStartUnavailableReason(node?: Pick<NodeProjection, "online" | "runtimeStatus">): string {
  if (!node) return "请选择设备";
  if (!node.online) return "设备当前离线";
  if (!node.runtimeStatus) return "正在确认设备上的 Codex 状态";
  if (node.runtimeStatus !== "ready") return "设备在线，但 Codex 当前不可用";
  return "";
}

export function selectTasks(state: ProjectionState, readSequences: Readonly<Record<string, number>> = {}): TaskSummary[] {
  return Object.values(state.threads)
    .map((thread) => {
      const turns = thread.turnIds.map((turnId) => state.turns[`${thread.nodeId}\u001f${thread.workspaceId}\u001f${thread.threadId}\u001f${turnId}`]).filter(Boolean);
      const approvals = Object.values(state.approvals).filter((approval) => approval.nodeId === thread.nodeId && approval.workspaceId === thread.workspaceId && approval.threadId === thread.threadId);
      const pendingApprovals = approvals.length > 0
        ? approvals.filter((approval) => approval.status === "pending").length
        : thread.pendingApprovals ?? 0;
      return {
        thread,
        node: state.nodes[thread.nodeId],
        workspace: state.workspaces[`${thread.nodeId}\u001f${thread.workspaceId}`],
        agent: thread.agentInstanceId ? state.agents[`${thread.nodeId}\u001f${thread.agentInstanceId}`] : undefined,
        latestTurn: turns.at(-1),
        pendingApprovals,
        unreadCount: Math.max(0, thread.latestSequence - (readSequences[thread.key] ?? thread.latestSequence)),
      };
    })
    .sort((left, right) => (right.thread.updatedAt ?? "").localeCompare(left.thread.updatedAt ?? ""));
}

export function filterTasks(tasks: TaskSummary[], filter: TaskFilter, query: string, nodeId = "", workspaceId = ""): TaskSummary[] {
  const normalized = query.trim().toLocaleLowerCase();
  return tasks.filter((task) => {
    if (nodeId && task.thread.nodeId !== nodeId) return false;
    if (workspaceId && task.thread.workspaceId !== workspaceId) return false;
    const status = task.latestTurn?.status ?? task.thread.status ?? "";
    if (filter === "active" && !ACTIVE.has(status)) return false;
    if (filter === "approval" && task.pendingApprovals === 0) return false;
    if (filter === "failed" && status !== "failed") return false;
    if (filter === "completed" && status !== "completed") return false;
    if (!normalized) return true;
    return [task.thread.title, task.thread.preview, task.agent?.displayName, task.workspace?.name, task.node?.name]
      .filter((value): value is string => Boolean(value))
      .some((value) => value.toLocaleLowerCase().includes(normalized));
  });
}

export function selectHomeGroups(tasks: TaskSummary[]) {
  const ranked = [...tasks].filter((task) => continuationPriority(task) < Number.POSITIVE_INFINITY).sort((left, right) => {
    const priority = continuationPriority(left) - continuationPriority(right);
    return priority || (right.thread.updatedAt ?? "").localeCompare(left.thread.updatedAt ?? "");
  });
  const continuation = ranked[0];
  const remaining = tasks.filter((task) => task.thread.key !== continuation?.thread.key);
  const issues = remaining.filter((task) => isIssue(task));
  const issueKeys = new Set(issues.map((task) => task.thread.key));
  const approvals = remaining.filter((task) => task.pendingApprovals > 0 && !issueKeys.has(task.thread.key));
  const approvalKeys = new Set(approvals.map((task) => task.thread.key));
  const active = remaining.filter((task) => ACTIVE.has(task.latestTurn?.status ?? task.thread.status ?? "") && !issueKeys.has(task.thread.key) && !approvalKeys.has(task.thread.key));
  const grouped = new Set([continuation?.thread.key, ...issues.map((task) => task.thread.key), ...approvals.map((task) => task.thread.key), ...active.map((task) => task.thread.key)].filter(Boolean));
  return {
    continuation,
    approvals,
    issues,
    active,
    recent: tasks.filter((task) => !grouped.has(task.thread.key)).slice(0, 8),
  };
}

function continuationPriority(task: TaskSummary): number {
  const status = task.latestTurn?.status ?? task.thread.status ?? "";
  if (task.pendingApprovals > 0 || status === "waiting_approval") return 0;
  if (RUNNING.has(status)) return 1;
  if (WAITING.has(status)) return 2;
  if (["reconnecting", "uncertain", "ambiguous"].includes(status) || task.thread.recovery !== "none") return 3;
  return Number.POSITIVE_INFINITY;
}

function isIssue(task: TaskSummary): boolean {
  const status = task.latestTurn?.status ?? task.thread.status ?? "";
  return status === "failed" || ["uncertain", "ambiguous", "reconnecting"].includes(status) || task.thread.recovery !== "none";
}

export function selectThreadApprovals(state: ProjectionState, nodeId: string, workspaceId: string, threadId: string): ApprovalProjection[] {
  return Object.values(state.approvals).filter((approval) => approval.nodeId === nodeId && approval.workspaceId === workspaceId && approval.threadId === threadId && approval.status === "pending");
}

export function selectNotifications(state: ProjectionState): NotificationProjection[] {
  return Object.values(state.notifications).sort((left, right) => right.createdAt.localeCompare(left.createdAt));
}
