import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { NodeProjection, WorkspaceProjection } from "../state/projection";
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
    renderFlow({ startThread, initialTarget: { nodeId: "office", workspaceId: "repo" } });

    const input = screen.getByLabelText("你希望 Codex 完成什么？");
    fireEvent.change(input, { target: { value: "Keep this draft" } });
    const leaving = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(leaving);
    expect(leaving.defaultPrevented).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "下一步" }));
    fireEvent.click(screen.getByRole("button", { name: "确认并启动" }));

    await screen.findByText(/创建结果不确定/);
    expect(startThread).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "上一步" }));
    expect(screen.getByLabelText("你希望 Codex 完成什么？")).toHaveValue("Keep this draft");
  });

  it("asks before discarding an unsent new task", async () => {
    const onClose = vi.fn();
    renderFlow({ onClose, initialTarget: { nodeId: "office", workspaceId: "repo" } });
    fireEvent.change(screen.getByLabelText("你希望 Codex 完成什么？"), { target: { value: "Unsaved" } });
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    expect(screen.getByRole("dialog", { name: "放弃未发送的任务？" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "继续编辑" }));
    expect(screen.getByLabelText("你希望 Codex 完成什么？")).toHaveValue("Unsaved");
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    fireEvent.click(screen.getByRole("button", { name: "放弃草稿" }));
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  });

  it("advances only after a confirmed result", async () => {
    const onConfirmed = vi.fn();
    const startThread = vi.fn(() => Promise.resolve({ messageId: "start-confirmed", result: Promise.resolve({ payload: { status: "confirmed" } }) }));
    renderFlow({ startThread, onConfirmed, initialTarget: { nodeId: "office", workspaceId: "repo" } });
    fireEvent.change(screen.getByLabelText("你希望 Codex 完成什么？"), { target: { value: "Ship the task" } });
    fireEvent.click(screen.getByRole("button", { name: "下一步" }));
    fireEvent.click(screen.getByRole("button", { name: "确认并启动" }));
    await waitFor(() => expect(onConfirmed).toHaveBeenCalledWith("start-confirmed"));
  });
});

function renderFlow(options: {
  nodes?: NodeProjection[];
  workspaces?: WorkspaceProjection[];
  startThread?: ReturnType<typeof vi.fn>;
  initialTarget?: { nodeId: string; workspaceId: string };
  onClose?: () => void;
  onConfirmed?: (messageId: string) => void;
} = {}) {
  const nodes = options.nodes ?? [node("office", true, "ready")];
  const workspaces = options.workspaces ?? [workspace("office", "repo")];
  const startThread = options.startThread ?? vi.fn();
  const session = { startThread } as unknown as WorkbenchSession;
  return render(<NewTaskFlow session={session} connectionState="connected" nodes={nodes} workspaces={workspaces} initialTarget={options.initialTarget} onClose={options.onClose ?? vi.fn()} onConfirmed={options.onConfirmed ?? vi.fn()} />);
}

function node(nodeId: string, online: boolean, runtimeStatus = "ready"): NodeProjection {
  return { ownerId: "owner", nodeId, name: nodeId, online, runtimeStatus, workspaceIds: [], lastEventSequence: 1 };
}

function workspace(nodeId: string, workspaceId: string): WorkspaceProjection {
  return { key: `${nodeId}:${workspaceId}`, ownerId: "owner", nodeId, workspaceId, name: workspaceId, permissionProfile: "workspace-write", allowNetwork: false };
}
