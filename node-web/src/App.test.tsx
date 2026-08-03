import { render, screen, waitFor } from "@testing-library/react";
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
