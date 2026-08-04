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
    expect(screen.getByRole("button", { name: /^Office repo/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "在 Office repo 新建任务" })).toBeEnabled();
  });

  it("requires an explicit target before starting a new task", async () => {
    const fake = new FakeSession();
    render(<Workbench session={fake as unknown as WorkbenchSession} storage={new MemoryControlStorage()} settings={{ relayUrl: "wss://relay.test/web/connect", pairingUrl: "https://relay.test/pair" }} onSettingsSaved={() => undefined} />);

    fireEvent.click(screen.getByRole("button", { name: "新任务" }));
    expect(screen.getByRole("dialog", { name: "开始新任务" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "下一步" })).toBeDisabled();
    expect(screen.queryByLabelText("你希望 Codex 完成什么？")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Office.*Codex 可用/ }));
    fireEvent.click(screen.getByRole("button", { name: /Office repo.*可修改工作区文件/ }));
    fireEvent.click(screen.getByRole("button", { name: "下一步" }));
    fireEvent.change(screen.getByLabelText("你希望 Codex 完成什么？"), { target: { value: "Run the release checks" } });
    fireEvent.click(screen.getByRole("button", { name: "下一步" }));
    expect(screen.getByRole("region", { name: "执行目标" })).toHaveTextContent("Office");
    expect(screen.getByRole("region", { name: "执行目标" })).toHaveTextContent("Office repo");
    fireEvent.click(screen.getByRole("button", { name: "确认并启动" }));
    await waitFor(() => expect(fake.startThread).toHaveBeenCalledWith("node-a", "workspace-a", "Run the release checks"));
  });

  it("prefills only the workspace explicitly chosen from the device view", () => {
    const fake = new FakeSession();
    render(<Workbench session={fake as unknown as WorkbenchSession} storage={new MemoryControlStorage()} settings={{ relayUrl: "wss://relay.test/web/connect", pairingUrl: "https://relay.test/pair" }} onSettingsSaved={() => undefined} />);

    fireEvent.click(screen.getAllByRole("button", { name: "设备" })[0]);
    fireEvent.click(screen.getByRole("button", { name: "在 Office repo 新建任务" }));
    expect(screen.getByLabelText("你希望 Codex 完成什么？")).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "执行目标" })).toHaveTextContent("Office repo");
  });

  it("protects an unsent task draft during application and browser navigation", async () => {
    const fake = new FakeSession();
    render(<Workbench session={fake as unknown as WorkbenchSession} storage={new MemoryControlStorage()} settings={{ relayUrl: "wss://relay.test/web/connect", pairingUrl: "https://relay.test/pair" }} onSettingsSaved={() => undefined} />);
    fireEvent.click(screen.getByRole("button", { name: "继续任务 Office task" }));
    await screen.findByRole("heading", { name: "Office task" });
    fireEvent.change(screen.getByLabelText("任务指令"), { target: { value: "unfinished guidance" } });

    const leaving = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(leaving);
    expect(leaving.defaultPrevented).toBe(true);
    fireEvent.click(screen.getAllByRole("button", { name: "设置" })[0]);
    expect(screen.getByRole("dialog", { name: "放弃未发送的内容？" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "继续编辑" }));
    expect(screen.getByLabelText("任务指令")).toHaveValue("unfinished guidance");
    fireEvent.click(screen.getAllByRole("button", { name: "设置" })[0]);
    fireEvent.click(screen.getByRole("button", { name: "放弃草稿" }));
    expect(screen.getByRole("heading", { name: "浏览器与安全" })).toBeInTheDocument();
  });

  it("layers settings and keeps re-pairing visible after authorization expires", () => {
    const fake = new FakeSession("reauth_required");
    render(<Workbench session={fake as unknown as WorkbenchSession} storage={new MemoryControlStorage()} settings={{ relayUrl: "wss://relay.test/web/connect", pairingUrl: "https://relay.test/pair", adminEnabled: true, adminUrl: "/admin" }} onSettingsSaved={() => undefined} />);

    expect(screen.getByRole("alert")).toHaveTextContent("当前浏览器需要重新配对");
    expect(screen.getByRole("link", { name: "重新配对" })).toHaveAttribute("href", "https://relay.test/pair");
    fireEvent.click(screen.getAllByRole("button", { name: "设置" })[0]);
    expect(screen.getByRole("button", { name: "基础" })).toHaveAttribute("aria-current", "page");
    fireEvent.click(screen.getByRole("button", { name: "安全" }));
    expect(screen.getByRole("heading", { name: "控制端授权" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "管理已授权控制端" })).toHaveAttribute("href", "/admin");
    fireEvent.click(screen.getByRole("button", { name: "高级" }));
    expect(screen.getByRole("heading", { name: "Relay 与配对地址" })).toBeInTheDocument();
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

  it("seeds initial history as read and reports a newly observed Thread", async () => {
    const fake = new FakeSession();
    render(<Workbench session={fake as unknown as WorkbenchSession} storage={new MemoryControlStorage()} settings={{ relayUrl: "wss://relay.test/web/connect", pairingUrl: "https://relay.test/pair" }} onSettingsSaved={() => undefined} />);
    await waitFor(() => expect(screen.queryByText("1 条新进展")).not.toBeInTheDocument());
    act(() => fake.push(event(5, "thread.started", { status: "running", title: "External task" }, "workspace-a", "thread-external")));
    expect(await screen.findByText("1 条新进展")).toBeInTheDocument();
  });
});

class FakeSession {
  readonly projection = new DataProjection();
  readonly loadThread = vi.fn(() => Promise.resolve());
  readonly client = { state: "connected" };
  private snapshot: WorkbenchSnapshot;
  private listener?: () => void;

  constructor(connectionState: WorkbenchSnapshot["connectionState"] = "connected") {
    this.projection.apply(event(1, "device.status", { status: "online", runtime: "ready", name: "Office", workspaces: [{ id: "workspace-a", name: "Office repo", permissionProfile: "workspace-write", allowNetwork: false }] }));
    this.projection.apply(event(2, "thread.snapshot", { threads: [{ id: "thread-a", title: "Office task", preview: "Continue release", status: "running", updatedAt: "2026-08-03T02:00:00Z" }] }, "workspace-a"));
    this.projection.apply(event(3, "turn.started", { status: "running" }, "workspace-a", "thread-a", "turn-a"));
    this.projection.applyServerControlResult({ ...event(4, "control.result", { status: "confirmed", notifications: [{ id: "notice", nodeId: "node-a", workspaceId: "workspace-a", threadId: "thread-a", type: "task.completed", summary: "任务已完成", sourceSequence: 3, createdAt: "2026-08-03T02:10:00Z", read: false }] }), streamId: "server-control-v1-client" });
    this.snapshot = { revision: 1, connectionState, projection: this.projection.state, resources: { "threads:node-a:workspace-a": { state: "ready", updatedAt: "2026-08-03T02:00:00Z" } } };
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
  startThread = vi.fn(() => Promise.resolve({ messageId: "start-thread", result: Promise.resolve(event(11, "control.result", { status: "confirmed" })) }));
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
