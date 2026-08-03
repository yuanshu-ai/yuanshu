import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { YuanshuMessage } from "../protocol/v1/types.generated";
import { MemoryControlStorage } from "../relay/storage";
import { DataProjection } from "../state/projection";
import { Workbench } from "./Workbench";
import type { WorkbenchSession, WorkbenchSnapshot } from "./session";

describe("personal workbench", () => {
  beforeEach(() => sessionStorage.clear());
  afterEach(() => cleanup());

  it("starts with task-first groups and opens a Thread without a device drill-down", async () => {
    const fake = new FakeSession();
    render(<Workbench session={fake as unknown as WorkbenchSession} storage={new MemoryControlStorage()} settings={{ relayUrl: "wss://relay.test/web/connect", pairingUrl: "https://relay.test/pair" }} onSettingsSaved={() => undefined} />);

    expect(screen.getByRole("heading", { name: "继续你的 Codex 工作" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "继续任务 Office task" })).toHaveTextContent("Office");
    expect(screen.queryByRole("heading", { name: "其它运行中任务" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "继续任务 Office task" }));
    await waitFor(() => expect(fake.loadThread).toHaveBeenCalledWith("node-a", "workspace-a", "thread-a"));
    expect(screen.getByRole("heading", { name: "Office task" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /需要控制权/ })).toBeDisabled();
  });

  it("uses the dedicated task and notification navigation views", () => {
    const fake = new FakeSession();
    render(<Workbench session={fake as unknown as WorkbenchSession} storage={new MemoryControlStorage()} settings={{ relayUrl: "wss://relay.test/web/connect", pairingUrl: "https://relay.test/pair" }} onSettingsSaved={() => undefined} />);

    fireEvent.click(screen.getAllByRole("button", { name: "任务" })[0]);
    expect(screen.getByRole("heading", { name: "全部任务" })).toBeInTheDocument();
    fireEvent.change(screen.getByPlaceholderText("搜索已同步的标题和摘要"), { target: { value: "missing" } });
    expect(screen.getByText("没有匹配的任务")).toBeInTheDocument();

    fireEvent.click(screen.getAllByRole("button", { name: "待办通知 1" })[0]);
    expect(screen.getByRole("heading", { name: "最近通知" })).toBeInTheDocument();
    expect(screen.getByText("任务已完成")).toBeInTheDocument();
  });

  it("exposes devices as a mobile-level destination", () => {
    const fake = new FakeSession();
    render(<Workbench session={fake as unknown as WorkbenchSession} storage={new MemoryControlStorage()} settings={{ relayUrl: "wss://relay.test/web/connect", pairingUrl: "https://relay.test/pair" }} onSettingsSaved={() => undefined} />);

    fireEvent.click(screen.getAllByRole("button", { name: "设备" })[0]);
    expect(screen.getByRole("heading", { name: "设备与工作区" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Office repo/ })).toBeInTheDocument();
  });

  it("pauses live following while reading older content and exposes new progress", async () => {
    const fake = new FakeSession();
    const { container } = render(<Workbench session={fake as unknown as WorkbenchSession} storage={new MemoryControlStorage()} settings={{ relayUrl: "wss://relay.test/web/connect", pairingUrl: "https://relay.test/pair" }} onSettingsSaved={() => undefined} />);
    fireEvent.click(screen.getByRole("button", { name: "继续任务 Office task" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Office task" })).toBeInTheDocument());
    const timeline = container.querySelector(".thread-scroll") as HTMLDivElement;
    Object.defineProperties(timeline, { scrollHeight: { configurable: true, value: 1000 }, clientHeight: { configurable: true, value: 300 }, scrollTop: { configurable: true, writable: true, value: 100 } });
    fireEvent.scroll(timeline);
    act(() => fake.push(event(5, "agent.message.completed", { text: "New remote progress" }, "workspace-a", "thread-a", "turn-a")));
    expect(await screen.findByRole("button", { name: "查看 1 条新内容" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "查看 1 条新内容" }));
    expect(screen.queryByRole("button", { name: "查看 1 条新内容" })).not.toBeInTheDocument();
  });
});

class FakeSession {
  readonly projection = new DataProjection();
  readonly loadThread = vi.fn(() => Promise.resolve());
  readonly client = { state: "connected" };
  private snapshot: WorkbenchSnapshot;
  private listener?: () => void;

  constructor() {
    this.projection.apply(event(1, "device.status", { status: "online", name: "Office", workspaces: [{ id: "workspace-a", name: "Office repo", permissionProfile: "workspace-write" }] }));
    this.projection.apply(event(2, "thread.snapshot", { threads: [{ id: "thread-a", title: "Office task", preview: "Continue release", status: "running", updatedAt: "2026-08-03T02:00:00Z" }] }, "workspace-a"));
    this.projection.apply(event(3, "turn.started", { status: "running" }, "workspace-a", "thread-a", "turn-a"));
    this.projection.applyServerControlResult({ ...event(4, "control.result", { status: "confirmed", notifications: [{ id: "notice", nodeId: "node-a", workspaceId: "workspace-a", threadId: "thread-a", type: "task.completed", summary: "任务已完成", sourceSequence: 3, createdAt: "2026-08-03T02:10:00Z", read: false }] }), streamId: "server-control-v1-client" });
    this.snapshot = { revision: 1, connectionState: "connected", projection: this.projection.state, resources: {} };
  }

  subscribe = (listener: () => void) => { this.listener = listener; return () => { this.listener = undefined; }; };
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

  push(next: YuanshuMessage) {
    this.projection.apply(next);
    this.snapshot = { ...this.snapshot, revision: this.snapshot.revision + 1, projection: this.projection.state };
    this.listener?.();
  }
}

function event(sequence: number, type: string, payload: Record<string, unknown>, workspaceId?: string, threadId?: string, turnId?: string): YuanshuMessage {
  return { protocolVersion: "1.0", messageId: `event-${sequence}`, type, ownerId: "owner", nodeId: "node-a", streamId: "node-events-v1", sequence, correlationId: `correlation-${sequence}`, sentAt: "2026-08-03T00:00:00Z", payload, ...(workspaceId ? { workspaceId } : {}), ...(threadId ? { threadId } : {}), ...(turnId ? { turnId } : {}) };
}
