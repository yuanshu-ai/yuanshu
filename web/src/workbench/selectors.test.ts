// @vitest-environment node

import { describe, expect, it } from "vitest";

import type { ProjectionState } from "../state/projection";
import { filterTasks, selectHomeGroups, selectTasks } from "./selectors";

describe("workbench task selectors", () => {
  it("groups tasks across Nodes while preserving isolation", () => {
    const tasks = selectTasks(state());
    const groups = selectHomeGroups(tasks);
    expect(tasks.map((task) => task.thread.nodeId)).toEqual(["node-a", "node-b"]);
    expect(groups.continuation?.thread.threadId).toBe("thread-a");
    expect(groups.active).toEqual([]);
    expect(groups.approvals).toEqual([]);
    expect(groups.issues.map((task) => task.thread.threadId)).toEqual(["thread-b"]);
    expect(filterTasks(tasks, "failed", "", "node-b").map((task) => task.thread.threadId)).toEqual(["thread-b"]);
    expect(filterTasks(tasks, "all", "office").map((task) => task.thread.threadId)).toEqual(["thread-a"]);
  });

  it("prioritizes approvals and reports only progress after the local read marker", () => {
    const value = state();
    const tasks = selectTasks(value, { "node-a\u001fworkspace-a\u001fthread-a": 1 });
    expect(tasks[0].unreadCount).toBe(1);
    expect(selectHomeGroups(tasks).continuation?.pendingApprovals).toBe(1);
  });

  it("uses the task-list approval count before detailed approval records are loaded", () => {
    const value = state();
    value.threads["node-a\u001fworkspace-a\u001fthread-a"].pendingApprovals = 2;
    value.approvals = {};
    const task = selectTasks(value).find((candidate) => candidate.thread.threadId === "thread-a");
    expect(task?.pendingApprovals).toBe(2);
    expect(selectHomeGroups(selectTasks(value)).continuation?.thread.threadId).toBe("thread-a");
  });
});

function state(): ProjectionState {
  return {
	agents: {},
    nodes: {
      "node-a": { ownerId: "owner", nodeId: "node-a", name: "Office", online: true, workspaceIds: ["workspace-a"], lastEventSequence: 1 },
      "node-b": { ownerId: "owner", nodeId: "node-b", name: "Home", online: true, workspaceIds: ["workspace-b"], lastEventSequence: 1 },
    },
    workspaces: {
      "node-a\u001fworkspace-a": { key: "node-a\u001fworkspace-a", ownerId: "owner", nodeId: "node-a", workspaceId: "workspace-a", name: "Office repo" },
      "node-b\u001fworkspace-b": { key: "node-b\u001fworkspace-b", ownerId: "owner", nodeId: "node-b", workspaceId: "workspace-b", name: "Home repo" },
    },
    threads: {
      "node-a\u001fworkspace-a\u001fthread-a": { key: "node-a\u001fworkspace-a\u001fthread-a", ownerId: "owner", nodeId: "node-a", workspaceId: "workspace-a", threadId: "thread-a", title: "Office task", status: "running", updatedAt: "2026-08-03T02:00:00Z", turnIds: ["turn-a"], latestSequence: 2, recovery: "none" },
      "node-b\u001fworkspace-b\u001fthread-b": { key: "node-b\u001fworkspace-b\u001fthread-b", ownerId: "owner", nodeId: "node-b", workspaceId: "workspace-b", threadId: "thread-b", title: "Home task", status: "failed", updatedAt: "2026-08-03T01:00:00Z", turnIds: ["turn-b"], latestSequence: 2, recovery: "none" },
    },
    turns: {
      "node-a\u001fworkspace-a\u001fthread-a\u001fturn-a": { key: "a", ownerId: "owner", nodeId: "node-a", workspaceId: "workspace-a", threadId: "thread-a", turnId: "turn-a", status: "running", items: [] },
      "node-b\u001fworkspace-b\u001fthread-b\u001fturn-b": { key: "b", ownerId: "owner", nodeId: "node-b", workspaceId: "workspace-b", threadId: "thread-b", turnId: "turn-b", status: "failed", items: [] },
    },
    approvals: {
      approval: { key: "approval", nodeId: "node-a", workspaceId: "workspace-a", threadId: "thread-a", turnId: "turn-a", approvalId: "approval", status: "pending" },
    },
    files: {}, events: {}, notifications: {}, actions: {},
  };
}
