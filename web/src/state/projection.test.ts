// @vitest-environment node

import { describe, expect, it } from "vitest";
import { DataProjection, eventBucketKey, fileChangeKey, threadKey, turnKey, workspaceKey } from "./projection";
import type { YuanshuMessage } from "../protocol/v1/types.generated";

describe("personal data projection", () => {
  it("keeps Node, workspace, Thread, Turn, and event buckets isolated", () => {
    const projection = new DataProjection({ now: () => "2026-08-03T00:00:00Z" });
    projection.apply(message("node-a", 1, "device.status", { status: "online", name: "Office", workspaces: [{ id: "workspace", name: "Office repo" }] }));
    projection.apply(message("node-a", 2, "thread.started", { status: "running" }, "workspace", "thread", undefined));
    projection.apply(message("node-a", 3, "turn.started", { status: "running" }, "workspace", "thread", "turn"));
    projection.apply(message("node-a", 4, "turn.completed", { status: "completed" }, "workspace", "thread", "turn"));
    projection.apply(message("node-a", 5, "agent.message.delta", { text: "office output" }, "workspace", "thread", "turn"));
    projection.apply(message("node-b", 1, "device.status", { status: "online", name: "Home", workspaces: [{ id: "workspace", name: "Home repo" }] }));
    projection.apply(message("node-b", 2, "thread.started", { status: "running" }, "workspace", "thread", undefined));
    projection.apply(message("node-b", 3, "agent.message.delta", { text: "home output" }, "workspace", "thread", "turn"));

    expect(projection.state.nodes["node-a"].name).toBe("Office");
    expect(projection.state.nodes["node-b"].name).toBe("Home");
    expect(projection.state.workspaces[workspaceKey("node-a", "workspace")].name).toBe("Office repo");
    expect(projection.state.workspaces[workspaceKey("node-b", "workspace")].name).toBe("Home repo");
    expect(projection.state.threads[threadKey("node-a", "workspace", "thread")].status).toBe("completed");
    expect(projection.state.turns[turnKey("node-a", "workspace", "thread", "turn")].status).toBe("completed");
    expect(projection.state.events[eventBucketKey({ nodeId: "node-a", workspaceId: "workspace", threadId: "thread", turnId: "turn" })].find((item) => item.event.payload.text === "office output")).toBeDefined();
    expect(projection.state.events[eventBucketKey({ nodeId: "node-b", workspaceId: "workspace", threadId: "thread", turnId: "turn" })].find((item) => item.event.payload.text === "home output")).toBeDefined();
  });

  it("projects the workspace network policy advertised by the device", () => {
    const projection = new DataProjection();
    projection.apply(message("node-a", 1, "device.status", { status: "online", workspaces: [{ id: "workspace", permissionProfile: "workspace-write", allowNetwork: false }] }));
    expect(projection.state.workspaces[workspaceKey("node-a", "workspace")]).toMatchObject({ permissionProfile: "workspace-write", allowNetwork: false });
  });

  it("accepts the Runtime state field without marking the Node offline", () => {
    const projection = new DataProjection();
    projection.apply(message("node-a", 1, "device.status", { status: "online", runtime: "ready" }));
    projection.apply(message("node-a", 2, "runtime.status", { state: "unavailable" }));
    expect(projection.state.nodes["node-a"]).toMatchObject({ online: true, runtimeStatus: "unavailable" });
    projection.apply(message("node-a", 3, "runtime.status", { status: "ready" }));
    expect(projection.state.nodes["node-a"]).toMatchObject({ online: true, runtimeStatus: "ready" });
  });

  it("maps snapshots, approvals, gaps, and control results without treating sent as confirmed", () => {
    const projection = new DataProjection({ now: () => "2026-08-03T00:00:00Z" });
    projection.applyControlAction({ messageId: "control-1", nodeId: "node-a", type: "turn.start", state: "sent" });
    projection.apply(message("node-a", 1, "thread.snapshot", { status: "running", latestSequence: 1, turns: [{ id: "turn", status: "running" }] }, "workspace", "thread"));
    projection.apply(message("node-a", 2, "approval.requested", { approvalId: "approval", operationDigest: "digest", kind: "command", summary: "run command" }, "workspace", "thread", "turn", "item"));
    projection.apply(message("node-a", 3, "history.gap", { afterSequence: 2, earliestSequence: 5 }, "workspace", "thread"));
    expect(projection.state.actions["control-1"].state).toBe("sent");
    expect(projection.state.approvals["node-a\u001fworkspace\u001fthread\u001fturn\u001fapproval"].status).toBe("pending");
    expect(projection.state.threads[threadKey("node-a", "workspace", "thread")].recovery).toBe("history_gap");

    projection.apply(message("node-a", 4, "control.result", { status: "confirmed" }, "workspace", "thread"));
    expect(projection.state.actions["control-1"].state).toBe("sent");
    projection.apply(message("node-a", 5, "control.result", { status: "confirmed" }, "workspace", "thread"));
    expect(projection.state.actions["control-1"].state).toBe("sent");
    // An envelope is correlated by correlationId, not by a guessed sequence.
    projection.apply({ ...message("node-a", 6, "control.result", { status: "confirmed" }, "workspace", "thread"), correlationId: "control-1" });
    expect(projection.state.actions["control-1"].state).toBe("confirmed");

    projection.apply(message("node-a", 7, "approval.resolved", { approvalId: "approval", decision: "accept" }, "workspace", "thread", "turn", "item"));
    expect(projection.state.approvals["node-a\u001fworkspace\u001fthread\u001fturn\u001fapproval"].decision).toBe("accept");
  });

  it("deduplicates an accepted envelope", () => {
    const projection = new DataProjection();
    const event = message("node-a", 1, "agent.message.delta", { text: "one" }, "workspace", "thread", "turn");
    projection.apply(event);
    projection.apply(event);
    expect(projection.state.events[eventBucketKey(event)]).toHaveLength(1);
  });

  it("merges snapshot history with streaming message, command, and diff items", () => {
    const projection = new DataProjection();
    projection.apply(message("node-a", 1, "thread.snapshot", {
      status: "idle", historyState: "complete", title: "Deploy API", preview: "ship it",
      turns: [{ id: "turn", status: "completed", historyState: "complete", items: [
        { id: "user-item", kind: "user_message", status: "completed", text: "ship it" },
        { id: "agent-item", kind: "agent_message", status: "completed", text: "done" },
      ] }],
    }, "workspace", "thread"));
    projection.apply(message("node-a", 2, "turn.started", { status: "running" }, "workspace", "thread", "turn"));
    projection.apply(message("node-a", 3, "agent.message.delta", { text: " + more" }, "workspace", "thread", "turn", "agent-item"));
    projection.apply(message("node-a", 4, "command.started", { commandId: "command", displayText: "go test" }, "workspace", "thread", "turn"));
    projection.apply(message("node-a", 5, "command.output.delta", { commandId: "command", stream: "stdout", text: "ok\n" }, "workspace", "thread", "turn"));
    projection.apply(message("node-a", 6, "diff.updated", { path: "internal/app.go", diff: "+new" }, "workspace", "thread", "turn"));

    const thread = projection.state.threads[threadKey("node-a", "workspace", "thread")];
    const turn = projection.state.turns[turnKey("node-a", "workspace", "thread", "turn")];
    expect(thread.title).toBe("Deploy API");
    expect(turn.items.find((item) => item.id === "agent-item")?.text).toBe("done + more");
    expect(turn.items.find((item) => item.id === "command")?.output).toBe("ok\n");
    expect(turn.items.find((item) => item.kind === "diff")?.path).toBe("internal/app.go");
    expect(projection.state.files[fileChangeKey("node-a", "workspace", "thread", "turn", "internal/app.go")]?.diff).toBe("+new");
  });

  it("merges a targeted Diff snapshot without replacing existing Turn items", () => {
    const projection = new DataProjection();
    projection.apply(message("node-a", 1, "thread.snapshot", {
      status: "idle",
      turns: [{ id: "turn", status: "completed", items: [{ id: "agent", kind: "agent_message", text: "kept" }, { id: "file", kind: "file_change", path: "app.go", changeType: "modified" }] }],
    }, "workspace", "thread"));

    projection.applyDiffSnapshot(message("node-a", 2, "thread.snapshot", {
      status: "idle",
      turns: [{ id: "turn", status: "completed", items: [{ id: "file", kind: "file_change", path: "app.go", changeType: "modified", diff: "+line", truncated: true, totalBytes: 70000, digest: "digest" }] }],
    }, "workspace", "thread"), "app.go");

    expect(projection.state.turns[turnKey("node-a", "workspace", "thread", "turn")].items.find((item) => item.id === "agent")?.text).toBe("kept");
    expect(projection.state.files[fileChangeKey("node-a", "workspace", "thread", "turn", "app.go")]).toMatchObject({ diff: "+line", truncated: true, totalBytes: 70000, digest: "digest" });
  });

  it("isolates Protocol 1.1 agents and tasks even when native identifiers overlap", () => {
    const projection = new DataProjection();
    projection.apply(messageV11("node-a", 1, "agent.snapshot", { agents: [
      { id: "codex", adapterType: "codex", displayName: "Codex", runtimeMode: "managed", status: "ready", capabilities: [{ id: "task.start", level: "full" }] },
      { id: "claude-detected", adapterType: "claude-code", displayName: "Claude Code", runtimeMode: "detected-only", status: "detected", capabilities: [] },
    ] }));
    projection.apply(messageV11("node-b", 1, "agent.snapshot", { agents: [
      { id: "codex", adapterType: "codex", displayName: "Codex Home", runtimeMode: "managed", status: "ready", capabilities: [{ id: "task.start", level: "full" }] },
    ] }));
    projection.apply(messageV11("node-a", 2, "task.snapshot", { tasks: [{ id: "task", agentInstanceId: "codex", title: "Office task", status: "running" }] }, "workspace", "codex"));
    projection.apply(messageV11("node-b", 2, "task.snapshot", { tasks: [{ id: "task", agentInstanceId: "codex", title: "Home task", status: "running" }] }, "workspace", "codex"));

    expect(projection.state.agents["node-a\u001fcodex"].displayName).toBe("Codex");
    expect(projection.state.agents["node-b\u001fcodex"].displayName).toBe("Codex Home");
    expect(projection.state.agents["node-a\u001fclaude-detected"]).toMatchObject({ runtimeMode: "detected-only", status: "detected" });
    expect(projection.state.threads[threadKey("node-a", "workspace", "task")]).toMatchObject({ title: "Office task", agentInstanceId: "codex" });
    expect(projection.state.threads[threadKey("node-b", "workspace", "task")]).toMatchObject({ title: "Home task", agentInstanceId: "codex" });
  });
});

function message(nodeId: string, sequence: number, type: string, payload: Record<string, unknown>, workspaceId?: string, threadId?: string, turnId?: string, itemId?: string): YuanshuMessage {
  return {
    protocolVersion: "1.0",
    messageId: `${nodeId}-${sequence}`,
    type,
    ownerId: "owner",
    nodeId,
    streamId: "node-events-v1",
    sequence,
    correlationId: `${nodeId}-correlation-${sequence}`,
    sentAt: "2026-08-03T00:00:00Z",
    payload,
    ...(workspaceId ? { workspaceId } : {}),
    ...(threadId ? { threadId } : {}),
    ...(turnId ? { turnId } : {}),
    ...(itemId ? { itemId } : {}),
  };
}

function messageV11(nodeId: string, sequence: number, type: string, payload: Record<string, unknown>, workspaceId?: string, agentInstanceId?: string, taskId?: string, runId?: string): YuanshuMessage {
  return {
    protocolVersion: "1.1", messageId: `${nodeId}-v11-${sequence}`, type, ownerId: "owner", nodeId, streamId: "node-events-v1.1",
    sequence, correlationId: `${nodeId}-v11-correlation-${sequence}`, sentAt: "2026-08-03T00:00:00Z", payload,
    ...(workspaceId ? { workspaceId } : {}), ...(agentInstanceId ? { agentInstanceId } : {}), ...(taskId ? { taskId } : {}), ...(runId ? { runId } : {}),
  } as unknown as YuanshuMessage;
}
