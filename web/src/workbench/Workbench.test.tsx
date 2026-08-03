import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { YuanshuMessage } from "../protocol/v1/types.generated";
import { MemoryControlStorage } from "../relay/storage";
import { DataProjection } from "../state/projection";
import { Workbench } from "./Workbench";
import type { WorkbenchSession, WorkbenchSnapshot } from "./session";

describe("personal workbench", () => {
  beforeEach(() => sessionStorage.clear());

  it("starts with task-first groups and opens a Thread without a device drill-down", async () => {
    const fake = new FakeSession();
    render(<Workbench session={fake as unknown as WorkbenchSession} storage={new MemoryControlStorage()} settings={{ relayUrl: "wss://relay.test/web/connect", pairingUrl: "https://relay.test/pair" }} onSettingsSaved={() => undefined} />);

    expect(screen.getByRole("heading", { name: "继续手上的任务" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "正在执行" })).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("button", { name: /Office task/ })[0]);
    await waitFor(() => expect(fake.loadThread).toHaveBeenCalledWith("node-a", "workspace-a", "thread-a"));
    expect(screen.getByRole("heading", { name: "Office task" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /需要控制权/ })).toBeDisabled();
  });

  it("uses the dedicated task and notification navigation views", () => {
    const fake = new FakeSession();
    render(<Workbench session={fake as unknown as WorkbenchSession} storage={new MemoryControlStorage()} settings={{ relayUrl: "wss://relay.test/web/connect", pairingUrl: "https://relay.test/pair" }} onSettingsSaved={() => undefined} />);

    fireEvent.click(screen.getAllByRole("button", { name: "任务" })[0]);
    expect(screen.getByRole("heading", { name: "所有上下文" })).toBeInTheDocument();
    fireEvent.change(screen.getByPlaceholderText("搜索已同步的标题和预览"), { target: { value: "missing" } });
    expect(screen.getByText("没有匹配的任务")).toBeInTheDocument();

    fireEvent.click(screen.getAllByRole("button", { name: "通知 1" })[0]);
    expect(screen.getByRole("heading", { name: "最近动态" })).toBeInTheDocument();
    expect(screen.getByText("任务已完成")).toBeInTheDocument();
  });
});

class FakeSession {
  readonly projection = new DataProjection();
  readonly loadThread = vi.fn(() => Promise.resolve());
  readonly client = { state: "connected" };
  private readonly snapshot: WorkbenchSnapshot;

  constructor() {
    this.projection.apply(event(1, "device.status", { status: "online", name: "Office", workspaces: [{ id: "workspace-a", name: "Office repo", permissionProfile: "workspace-write" }] }));
    this.projection.apply(event(2, "thread.snapshot", { threads: [{ id: "thread-a", title: "Office task", preview: "Continue release", status: "running", updatedAt: "2026-08-03T02:00:00Z" }] }, "workspace-a"));
    this.projection.apply(event(3, "turn.started", { status: "running" }, "workspace-a", "thread-a", "turn-a"));
    this.projection.applyServerControlResult({ ...event(4, "control.result", { status: "confirmed", notifications: [{ id: "notice", nodeId: "node-a", workspaceId: "workspace-a", threadId: "thread-a", type: "task.completed", summary: "任务已完成", sourceSequence: 3, createdAt: "2026-08-03T02:10:00Z", read: false }] }), streamId: "server-control-v1-client" });
    this.snapshot = { revision: 1, connectionState: "connected", projection: this.projection.state, resources: {} };
  }

  subscribe = () => () => undefined;
  getSnapshot = () => this.snapshot;
  refreshAll = vi.fn(() => Promise.resolve());
  refreshNotifications = vi.fn(() => Promise.resolve());
  markNotificationRead = vi.fn(() => Promise.resolve());
  clearCreatedThread = vi.fn();
  getLease = () => ({ state: "none" as const, epoch: 0 });
  canMutate = () => false;
  acquireLease = vi.fn(() => Promise.resolve({ state: "held" as const, leaseId: "lease", epoch: 1, expiresAt: "2099-01-01T00:00:00Z" }));
  releaseLease = vi.fn(() => Promise.resolve({ state: "none" as const, epoch: 2 }));
  request = vi.fn(() => Promise.resolve(event(10, "control.result", { status: "confirmed" })));
  startThread = vi.fn();
  loadDiff = vi.fn(() => Promise.resolve());
}

function event(sequence: number, type: string, payload: Record<string, unknown>, workspaceId?: string, threadId?: string, turnId?: string): YuanshuMessage {
  return { protocolVersion: "1.0", messageId: `event-${sequence}`, type, ownerId: "owner", nodeId: "node-a", streamId: "node-events-v1", sequence, correlationId: `correlation-${sequence}`, sentAt: "2026-08-03T00:00:00Z", payload, ...(workspaceId ? { workspaceId } : {}), ...(threadId ? { threadId } : {}), ...(turnId ? { turnId } : {}) };
}
