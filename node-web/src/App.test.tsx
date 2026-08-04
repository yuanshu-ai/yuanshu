import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "./App";

describe("Node control center", () => {
  beforeEach(() => {
    history.replaceState(null, "", "/#bootstrap-token");
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ session: "session-token" }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ status: { version: 1, state: "ready", platform: "darwin", config: "ready", identity: "bound", database: "ready", workspaces: 1, codex: "ready", authentication: "authenticated", recovery: "reconciled", remoteControl: "online", autostart: "enabled" }, config: { host: { name: "Office Mac" }, workspaces: [] } }), { status: 200, headers: { "Content-Type": "application/json" } })));
  });

  it("exchanges the fragment without persisting it and renders status", async () => {
    render(<App />);
    await waitFor(() => expect(screen.getAllByText("Node 已就绪")).toHaveLength(2));
    expect(window.location.hash).toBe("");
    expect(localStorage.length).toBe(0);
  });
});

it("uses a native workspace token during first setup without exposing a path field", async () => {
  history.replaceState(null, "", "/#setup-token");
  const setupOverview = { status: { version: 1, state: "needs_attention", platform: "darwin", config: "unavailable", identity: "unchecked", database: "unchecked", workspaces: 0, codex: "unchecked", authentication: "unchecked", recovery: "not_required", remoteControl: "not_available", autostart: "disabled" }, setup: { required: true, pickerAvailable: true, platform: "darwin", defaultName: "Office Mac", defaultCodex: "codex" } };
  vi.stubGlobal("fetch", vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ session: "session-token" }), { status: 200, headers: { "Content-Type": "application/json" } }))
    .mockResolvedValueOnce(new Response(JSON.stringify(setupOverview), { status: 200, headers: { "Content-Type": "application/json" } }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ protocol: "node-local-v1", ok: true, workspaceToken: "opaque-token", workspaceName: "Private project" }), { status: 200, headers: { "Content-Type": "application/json" } }))
    .mockResolvedValueOnce(new Response(JSON.stringify(setupOverview), { status: 200, headers: { "Content-Type": "application/json" } })));
  render(<App />);
  const picker = await screen.findByRole("button", { name: "选择工作区…" });
  expect(screen.queryByLabelText(/路径/)).not.toBeInTheDocument();
  fireEvent.click(picker);
  await screen.findByText("Private project");
  expect(screen.getByText("路径只保存在 Node 本机，不显示在此页面")).toBeInTheDocument();
  const calls = vi.mocked(fetch).mock.calls;
  await waitFor(() => expect(calls.length).toBeGreaterThanOrEqual(4));
  expect(String(calls[2]?.[1]?.body)).toContain('"command":"setup_pick"');
  expect(String(calls[2]?.[1]?.body)).not.toContain("Private project");
});
