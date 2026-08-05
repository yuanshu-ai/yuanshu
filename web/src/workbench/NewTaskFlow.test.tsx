import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { AgentProjection, NodeProjection, WorkspaceProjection } from "../state/projection";
import { NewTaskFlow } from "./NewTaskFlow";
import type { WorkbenchSession } from "./session";

afterEach(() => cleanup());

describe("new task flow", () => {
  it("shows unavailable devices without allowing them to be selected", () => {
    renderFlow({
      nodes: [node("offline", false), node("runtime", true, "unavailable")],
      workspaces: [workspace("offline", "work-offline"), workspace("runtime", "work-runtime")],
    });

    expect(screen.getByRole("button", { name: /offline.*设备离线/i })).toBeDisabled();
    expect(screen.getByRole("button", { name: /runtime.*Codex 暂不可用/i })).toBeDisabled();
    expect(screen.getByRole("button", { name: "下一步" })).toBeDisabled();
  });

  it("retains the draft and does not retry an ambiguous start", async () => {
    const startThread = vi.fn(() => Promise.resolve({ messageId: "start", result: Promise.resolve({ payload: { status: "ambiguous", errorCode: "ambiguous" } }) }));
    const onDraftChange = vi.fn();
    renderFlow({ startThread, onDraftChange, initialTarget: { nodeId: "office", workspaceId: "repo" } });

    const input = screen.getByLabelText("你希望 Codex 完成什么？");
    fireEvent.change(input, { target: { value: "Keep this draft" } });
    await waitFor(() => expect(onDraftChange).toHaveBeenCalledWith(true));
    fireEvent.click(screen.getByRole("button", { name: "确认并启动" }));

    await screen.findByText(/创建结果不确定/);
    expect(startThread).toHaveBeenCalledTimes(1);
    expect(screen.getByLabelText("你希望 Codex 完成什么？")).toHaveValue("Keep this draft");
  });

  it("reports an unsent draft and delegates closing to the workbench guard", async () => {
    const onClose = vi.fn();
    const onDraftChange = vi.fn();
    renderFlow({ onClose, onDraftChange, initialTarget: { nodeId: "office", workspaceId: "repo" } });
    fireEvent.change(screen.getByLabelText("你希望 Codex 完成什么？"), { target: { value: "Unsaved" } });
    await waitFor(() => expect(onDraftChange).toHaveBeenCalledWith(true));
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  });

  it("disables direct confirmation immediately when the selected Node becomes unavailable", () => {
    const props = {
      session: { startThread: vi.fn() } as unknown as WorkbenchSession,
      connectionState: "connected",
	  agents: [agent("office")],
      workspaces: [workspace("office", "repo")],
      initialTarget: { nodeId: "office", workspaceId: "repo" },
      onClose: vi.fn(),
      onConfirmed: vi.fn(),
    };
    const view = render(<NewTaskFlow {...props} nodes={[node("office", true, "ready")]} />);
    fireEvent.change(screen.getByLabelText("你希望 Codex 完成什么？"), { target: { value: "Run checks" } });
    expect(screen.getByRole("button", { name: "确认并启动" })).toBeEnabled();
    view.rerender(<NewTaskFlow {...props} nodes={[node("office", false, "ready")]} />);
    expect(screen.getByRole("button", { name: "确认并启动" })).toBeDisabled();
    expect(screen.getByText("设备当前离线")).toBeInTheDocument();
  });

  it("starts directly after a confirmed result", async () => {
    const onConfirmed = vi.fn();
    const startThread = vi.fn(() => Promise.resolve({ messageId: "start-confirmed", result: Promise.resolve({ payload: { status: "confirmed" } }) }));
    renderFlow({ startThread, onConfirmed, initialTarget: { nodeId: "office", workspaceId: "repo" } });
    fireEvent.change(screen.getByLabelText("你希望 Codex 完成什么？"), { target: { value: "Ship the task" } });
    fireEvent.click(screen.getByRole("button", { name: "确认并启动" }));
    await waitFor(() => expect(onConfirmed).toHaveBeenCalledWith("start-confirmed"));
  });

  it("hides detected-only agents from the new task target list", () => {
    renderFlow({
      nodes: [node("office", true, "ready")],
      agents: [{ ...agent("office"), agentInstanceId: "external-codex", key: "office:external-codex", displayName: "External Codex", runtimeMode: "detected-only", status: "detected", capabilities: [] }],
      workspaces: [workspace("office", "repo")],
    });
    fireEvent.click(screen.getByRole("button", { name: /office.*Codex 可用/i }));
    expect(screen.queryByRole("button", { name: /External Codex/ })).not.toBeInTheDocument();
    expect(screen.getByText("该设备还没有可控 Agent。")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "下一步" })).toBeDisabled();
  });

  it("keeps target selection when more than one managed target is available", () => {
    renderFlow({
      nodes: [node("office", true, "ready")],
      workspaces: [workspace("office", "repo"), workspace("office", "docs")],
    });

    expect(screen.queryByLabelText("你希望 Codex 完成什么？")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "下一步" })).toBeDisabled();
    expect(screen.getByRole("button", { name: /office.*Codex 可用/i })).toBeInTheDocument();
  });
});

function renderFlow(options: {
  nodes?: NodeProjection[];
  workspaces?: WorkspaceProjection[];
	agents?: AgentProjection[];
  startThread?: ReturnType<typeof vi.fn>;
  initialTarget?: { nodeId: string; workspaceId: string };
  onClose?: () => void;
  onConfirmed?: (messageId: string) => void;
  onDraftChange?: (dirty: boolean) => void;
} = {}) {
  const nodes = options.nodes ?? [node("office", true, "ready")];
  const workspaces = options.workspaces ?? [workspace("office", "repo")];
	const agents = options.agents ?? nodes.map((item) => agent(item.nodeId));
  const startThread = options.startThread ?? vi.fn();
  const session = { startThread } as unknown as WorkbenchSession;
  return render(<NewTaskFlow session={session} connectionState="connected" nodes={nodes} agents={agents} workspaces={workspaces} initialTarget={options.initialTarget} onClose={options.onClose ?? vi.fn()} onConfirmed={options.onConfirmed ?? vi.fn()} onDraftChange={options.onDraftChange} />);
}

function node(nodeId: string, online: boolean, runtimeStatus = "ready"): NodeProjection {
  return { ownerId: "owner", nodeId, name: nodeId, online, runtimeStatus, workspaceIds: [], lastEventSequence: 1 };
}

function workspace(nodeId: string, workspaceId: string): WorkspaceProjection {
  return { key: `${nodeId}:${workspaceId}`, ownerId: "owner", nodeId, workspaceId, name: workspaceId, permissionProfile: "workspace-write", allowNetwork: false, agentInstanceIds: ["codex-default"], defaultAgentInstanceId: "codex-default" };
}

function agent(nodeId: string): AgentProjection {
  return { key: `${nodeId}:codex-default`, ownerId: "owner", nodeId, agentInstanceId: "codex-default", adapterType: "codex", displayName: "Codex", runtimeMode: "managed", status: "ready", capabilities: [{ id: "task.start", level: "full" }], workspaceIds: ["repo"], updatedAt: "2026-08-05T00:00:00Z" };
}
