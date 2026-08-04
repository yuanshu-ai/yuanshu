import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "./App";

afterEach(() => cleanup());

describe("Node control center", () => {
  beforeEach(() => {
    history.replaceState(null, "", "/#bootstrap-token");
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ session: "session-token" }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ status: { version: 1, state: "ready", platform: "darwin", config: "ready", identity: "bound", database: "ready", workspaces: 1, codex: "ready", authentication: "authenticated", recovery: "reconciled", remoteControl: "online", autostart: "enabled" }, config: { host: { name: "Office Mac" }, workspaces: [] } }), { status: 200, headers: { "Content-Type": "application/json" } })));
  });

  it("exchanges the fragment without persisting it and renders status", async () => {
    render(<App />);
    await waitFor(() => expect(screen.getAllByText("已在线")).toHaveLength(2));
    expect(document.querySelector('img[src="/brand/yuanshu-mark.svg"]')).toBeInTheDocument();
    expect(window.location.hash).toBe("");
    expect(localStorage.length).toBe(0);
  });

  it("reviews protected changes in the local control center", async () => {
    const overview = { status: { version: 1, state: "ready", platform: "darwin", config: "ready", identity: "bound", database: "ready", workspaces: 1, codex: "ready", authentication: "authenticated", recovery: "reconciled", remoteControl: "online", autostart: "enabled" }, config: { revision: "rev-1", host: { name: "Office Mac" }, workspaces: [] }, configChanges: [{ id: "chg-1", baseRevision: "rev-1", state: "pending", createdAt: "2026-08-04T00:00:00Z", expiresAt: "2026-08-05T00:00:00Z", risk: "high", relayReconnect: true, permissionChange: "unchanged", details: [{ category: "relay", before: "wss://old.example", after: "wss://new.example", risk: "high" }] }] };
    vi.mocked(fetch).mockReset()
      .mockResolvedValueOnce(new Response(JSON.stringify({ session: "session-token" }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify(overview), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ protocol: "node-local-v1", ok: true }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ ...overview, configChanges: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "访问权限" }));
    fireEvent.click(await screen.findByRole("button", { name: "批准" }));
    expect(screen.getByRole("heading", { name: "批准安全配置变更" })).toBeInTheDocument();
    expect(screen.getAllByText("wss://old.example → wss://new.example")).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "确认批准" }));
    await waitFor(() => expect(vi.mocked(fetch).mock.calls.some((call) => String(call[1]?.body).includes('"command":"config_approve"') && String(call[1]?.body).includes('"changeId":"chg-1"'))).toBe(true));
  });
});

it("uses a native workspace token during first setup without exposing a path field", async () => {
  history.replaceState(null, "", "/#setup-token");
  const setupOverview = { status: { version: 1, state: "needs_attention", platform: "darwin", config: "unavailable", identity: "unchecked", database: "unchecked", workspaces: 0, codex: "unchecked", authentication: "unchecked", recovery: "not_required", remoteControl: "not_available", autostart: "disabled" }, setup: { required: true, pickerAvailable: true, platform: "darwin", defaultName: "Office Mac", defaultCodex: "codex" } };
  vi.stubGlobal("fetch", vi.fn(async (input, init) => {
    const path = String(input);
    if (path.endsWith("/session")) return new Response(JSON.stringify({ session: "session-token" }), { status: 200, headers: { "Content-Type": "application/json" } });
    if (path.endsWith("/overview")) return new Response(JSON.stringify(setupOverview), { status: 200, headers: { "Content-Type": "application/json" } });
    const command = JSON.parse(String(init?.body || "{}"))?.command;
    if (command === "setup_discover" || command === "setup_test") return new Response(JSON.stringify({ protocol: "node-local-v1", ok: true, config: { relay: "ready", server: { publicUrl: "http://127.0.0.1:9527", nodeRelayUrl: "ws://127.0.0.1:9527/node/connect", deploymentMode: "local" } } }), { status: 200, headers: { "Content-Type": "application/json" } });
    if (command === "setup_pick") return new Response(JSON.stringify({ protocol: "node-local-v1", ok: true, workspaceToken: "opaque-token", workspaceName: "Private project" }), { status: 200, headers: { "Content-Type": "application/json" } });
    return new Response(JSON.stringify({ protocol: "node-local-v1", ok: true }), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  render(<App />);
  const next = await screen.findByRole("button", { name: "下一步" });
  fireEvent.click(next);
  await screen.findByDisplayValue("http://127.0.0.1:9527");
  fireEvent.click(screen.getByRole("button", { name: "下一步" }));
  fireEvent.click(screen.getByRole("button", { name: "测试连接" }));
  await screen.findByText("Server 与安全连接验证通过");
  fireEvent.click(screen.getByRole("button", { name: "下一步" }));
  fireEvent.change(screen.getByLabelText("Bootstrap secret"), { target: { value: "single-use-secret" } });
  fireEvent.click(screen.getByRole("button", { name: "下一步" }));
  fireEvent.click(screen.getByRole("button", { name: "下一步" }));
  const picker = screen.getByRole("button", { name: "选择文件夹" });
  expect(screen.queryByLabelText(/路径/)).not.toBeInTheDocument();
  fireEvent.click(picker);
  await screen.findByText("Private project");
  const calls = vi.mocked(fetch).mock.calls;
  await waitFor(() => expect(calls.some((call) => String(call[1]?.body).includes('"command":"setup_pick"'))).toBe(true));
  const pickCall = calls.find((call) => String(call[1]?.body).includes('"command":"setup_pick"'));
  expect(String(pickCall?.[1]?.body)).not.toContain("Private project");
});
