import type {
  ApprovalProjection,
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
  latestTurn?: TurnProjection;
  pendingApprovals: number;
}

const ACTIVE = new Set(["running", "active", "inProgress", "waiting_approval", "reconnecting", "uncertain", "ambiguous"]);

export function selectTasks(state: ProjectionState): TaskSummary[] {
  return Object.values(state.threads)
    .map((thread) => {
      const turns = thread.turnIds.map((turnId) => state.turns[`${thread.nodeId}\u001f${thread.workspaceId}\u001f${thread.threadId}\u001f${turnId}`]).filter(Boolean);
      const pendingApprovals = Object.values(state.approvals).filter((approval) => approval.nodeId === thread.nodeId && approval.workspaceId === thread.workspaceId && approval.threadId === thread.threadId && approval.status === "pending").length;
      return {
        thread,
        node: state.nodes[thread.nodeId],
        workspace: state.workspaces[`${thread.nodeId}\u001f${thread.workspaceId}`],
        latestTurn: turns.at(-1),
        pendingApprovals,
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
    return [task.thread.title, task.thread.preview, task.workspace?.name, task.node?.name]
      .filter((value): value is string => Boolean(value))
      .some((value) => value.toLocaleLowerCase().includes(normalized));
  });
}

export function selectHomeGroups(tasks: TaskSummary[]) {
  return {
    active: tasks.filter((task) => ACTIVE.has(task.latestTurn?.status ?? task.thread.status ?? "")),
    approvals: tasks.filter((task) => task.pendingApprovals > 0),
    uncertain: tasks.filter((task) => ["uncertain", "ambiguous", "reconnecting"].includes(task.latestTurn?.status ?? task.thread.status ?? "") || task.thread.recovery !== "none"),
    recent: tasks.slice(0, 8),
  };
}

export function selectThreadApprovals(state: ProjectionState, nodeId: string, workspaceId: string, threadId: string): ApprovalProjection[] {
  return Object.values(state.approvals).filter((approval) => approval.nodeId === nodeId && approval.workspaceId === workspaceId && approval.threadId === threadId && approval.status === "pending");
}

export function selectNotifications(state: ProjectionState): NotificationProjection[] {
  return Object.values(state.notifications).sort((left, right) => right.createdAt.localeCompare(left.createdAt));
}
