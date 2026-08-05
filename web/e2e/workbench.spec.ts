import { expect, test, type Page } from "@playwright/test";
import { mkdir } from "node:fs/promises";
import path from "node:path";

const OWNER_ID = "owner-e2e";
const CLIENT_ID = "client-e2e";

test.beforeEach(async ({ page }) => {
  await installFakeRelay(page);
  await page.goto("/yuanshu.config.example.json");
  await seedBrowserIdentity(page);
  await page.goto("/");
  await expect(page.getByRole("button", { name: /Office release/ }).first()).toBeVisible();
});

test("opens a running task directly and safely controls its Turn", async ({ page }) => {
  await page.getByRole("button", { name: /Office release/ }).first().click();
  await expect(page.getByRole("heading", { name: "Office release" })).toBeVisible();
  await expect(page.getByText("Remote workbench ready")).toBeVisible();

  await page.getByRole("button", { name: "获取控制权" }).click();
  await expect(page.locator(".lease-badge.held:visible").filter({ hasText: "当前浏览器可操作" }).first()).toBeVisible();

  const composer = page.getByLabel("任务指令");
  await composer.fill("Continue the release checks");
  await composer.press(process.platform === "darwin" ? "Meta+Enter" : "Control+Enter");
  await expect(page.getByText("设备已确认请求")).toBeVisible();
  await expect(page.getByText("Streaming follow-up received")).toBeVisible();
});

test("uses application dialogs for high-risk approval and loads Diff on demand", async ({ page }) => {
  await page.getByRole("button", { name: /Office release/ }).first().click();
  await page.getByRole("button", { name: "获取控制权" }).click();

  await openInspector(page, "审批");
  await page.getByRole("button", { name: "批准" }).click();
  await expect(page.getByRole("dialog", { name: "检查高风险操作" })).toBeVisible();
  await page.getByRole("button", { name: "继续确认" }).click();
  await expect(page.getByRole("dialog", { name: "确认批准操作" })).toBeVisible();
  await page.getByRole("button", { name: "发送批准" }).click();
  await expect(page.getByText("批准已由设备确认")).toBeVisible();

  await page.getByRole("tab", { name: /文件/ }).click();
  await page.getByText("src/app.ts").last().click();
  await expect(page.getByText("+const ready = true;")).toBeVisible();
});

test("keeps two Nodes isolated and exposes task-first mobile navigation", async ({ page }) => {
  const usesMobileNavigation = (page.viewportSize()?.width ?? 1280) < 768;
  const navigation = usesMobileNavigation ? page.locator(".mobile-nav") : page.locator(".desktop-nav");
  await navigation.getByRole("button", { name: "任务" }).click();
  await expect(page.getByRole("heading", { name: "全部任务" })).toBeVisible();
  await expect(page.getByRole("button", { name: /Office release/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /Home maintenance/ })).toBeVisible();

  await page.getByLabel("筛选设备").selectOption("node-home");
  await expect(page.getByRole("button", { name: /Office release/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Home maintenance/ })).toBeVisible();

  if (usesMobileNavigation) {
    await expect(page.locator(".mobile-nav")).toBeVisible();
  }
});

test("switches the workbench language without losing task state", async ({ page }) => {
  await page.getByRole("button", { name: "English" }).click();
  await expect(page.locator("html")).toHaveAttribute("lang", "en-US");
  await expect(page.locator(".desktop-nav:visible, .mobile-nav:visible").getByRole("button", { name: "Tasks" })).toBeVisible();
  await expect(page.getByRole("button", { name: /Office release/ }).first()).toBeVisible();

  await page.getByRole("button", { name: "中文" }).click();
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-CN");
  await expect(page.getByRole("button", { name: /Office release/ }).first()).toBeVisible();
});

test("uses phone, tablet and desktop layout contracts", async ({ page }) => {
  const width = page.viewportSize()?.width ?? 1280;
  const taskSidebar = page.getByRole("complementary", { name: "任务列表与上下文" });
  const mobileNavigation = page.locator(".mobile-nav");
  if (width < 768) {
    await expect(taskSidebar).toBeVisible();
    await expect(mobileNavigation).toBeVisible();
  } else {
    await expect(taskSidebar).toBeVisible();
    await expect(mobileNavigation).toBeHidden();
  }

  await page.getByRole("button", { name: /Office release/ }).first().click();
  await expect(page.getByRole("region", { name: "任务详情" })).toBeVisible();
  if (width < 768) {
    await expect(taskSidebar).toBeHidden();
    await expect(mobileNavigation).toBeHidden();
    await expect(page.getByRole("button", { name: "打开任务 Inspector" })).toBeVisible();
  } else {
    await expect(taskSidebar).toBeVisible();
    await expect(page.getByRole("complementary", { name: "任务 Inspector" })).toBeVisible();
  }
  if (width < 1200) {
    const selector = width < 768
      ? ".mobile-nav button, .topbar-attention"
      : ".desktop-nav button, .topbar-attention, .connection-state";
    const touchTargets = await page.locator(selector).evaluateAll((elements) =>
      elements.filter((element) => {
        const bounds = element.getBoundingClientRect();
        return bounds.width > 0 && bounds.height > 0;
      }).map((element) => {
        const bounds = element.getBoundingClientRect();
        return { width: bounds.width, height: bounds.height };
      }),
    );
    expect(touchTargets.length).toBeGreaterThan(0);
    for (const target of touchTargets) {
      expect(target.width).toBeGreaterThanOrEqual(44);
      expect(target.height).toBeGreaterThanOrEqual(44);
    }
  }
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
});

test("starts a task only after explicitly confirming its device and workspace", async ({ page }) => {
  await page.getByRole("button", { name: "新任务" }).click();
  const dialog = page.getByRole("dialog", { name: "开始新任务" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole("button", { name: "下一步" })).toBeDisabled();
  await expect(dialog.getByLabel("你希望 Codex 完成什么？")).toHaveCount(0);

  await dialog.getByRole("button", { name: /Office Mac.*Codex 可用/ }).click();
  await dialog.getByRole("button", { name: /Codex.*可创建任务/ }).click();
  await dialog.getByRole("button", { name: /Release repo.*可修改工作区文件/ }).click();
  await dialog.getByRole("button", { name: "下一步" }).click();
  await dialog.getByLabel("你希望 Codex 完成什么？").fill("Create an explicit-target task");
  await dialog.getByRole("button", { name: "下一步" }).click();
  await expect(dialog.getByRole("region", { name: "执行目标" })).toContainText("Office Mac");
  await expect(dialog.getByRole("region", { name: "执行目标" })).toContainText("Codex");
  await expect(dialog.getByRole("region", { name: "执行目标" })).toContainText("Release repo");
  await dialog.getByRole("button", { name: "确认并启动" }).click();

  await expect.poll(() => page.evaluate(() => (window as unknown as { __yuanshuStartedTarget?: unknown }).__yuanshuStartedTarget)).toEqual({ nodeId: "node-office", agentInstanceId: "codex-default", workspaceId: "workspace-office", input: "Create an explicit-target task" });
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("heading", { name: "Explicit target task" })).toBeVisible();
});

test("protects an unsent task draft before leaving the detail", async ({ page }) => {
  await page.getByRole("button", { name: /Office release/ }).first().click();
  await page.getByLabel("任务指令").fill("Keep this unsent draft");
  if ((page.viewportSize()?.width ?? 1280) < 768) {
    await page.getByRole("button", { name: "返回任务列表" }).click();
  } else {
    await page.locator(".desktop-nav").getByRole("button", { name: "设置" }).click();
  }
  await expect(page.getByRole("dialog", { name: "放弃未发送的内容？" })).toBeVisible();
  await page.getByRole("button", { name: "继续编辑" }).click();
  await expect(page.getByLabel("任务指令")).toHaveValue("Keep this unsent draft");
});

test("keeps browser Back aligned with an unsent Thread draft", async ({ page }) => {
  await page.getByRole("button", { name: /Office release/ }).first().click();
  await page.getByLabel("任务指令").fill("Keep this browser-back draft");
  await page.goBack();
  await expect(page.getByRole("dialog", { name: "放弃未发送的内容？" })).toBeVisible();
  await page.getByRole("button", { name: "继续编辑" }).click();
  await expect(page.getByLabel("任务指令")).toHaveValue("Keep this browser-back draft");
  await page.goBack();
  await page.getByRole("button", { name: "放弃草稿" }).click();
  await expect.poll(() => page.evaluate(() => history.state?.yuanshuWorkbench ?? null)).not.toBe("thread");
});

test("protects a new-task draft from browser Back", async ({ page }) => {
  await page.getByRole("button", { name: "新任务" }).click();
  const flow = page.getByRole("dialog", { name: "开始新任务" });
  await flow.getByRole("button", { name: /Office Mac.*Codex 可用/ }).click();
  await flow.getByRole("button", { name: /Codex.*可创建任务/ }).click();
  await flow.getByRole("button", { name: /Release repo.*可修改工作区文件/ }).click();
  await flow.getByRole("button", { name: "下一步" }).click();
  await flow.getByLabel("你希望 Codex 完成什么？").fill("Keep this new-task draft");
  await page.goBack();
  await expect(page.getByRole("dialog", { name: "放弃未发送的内容？" })).toBeVisible();
  await page.getByRole("button", { name: "继续编辑" }).click();
  await expect(flow.getByLabel("你希望 Codex 完成什么？")).toHaveValue("Keep this new-task draft");
});

test("disables new work when presence or Runtime becomes unavailable", async ({ page }) => {
  await page.evaluate(() => (window as unknown as { __yuanshuEmitRuntime: (nodeId: string, state: string) => void }).__yuanshuEmitRuntime("node-office", "unavailable"));
  await page.locator(".desktop-nav:visible, .mobile-nav:visible").getByRole("button", { name: "设备" }).click();
  await page.getByRole("button", { name: /Office Mac.*Codex 不可用/ }).click();
  await page.getByRole("button", { name: /Codex.*只读/ }).click();
  await expect(page.getByRole("button", { name: "使用 Codex 在 Release repo 新建任务" })).toBeDisabled();

  await page.evaluate(() => {
    (window as unknown as { __yuanshuSetNodeOnline: (nodeId: string, online: boolean) => void }).__yuanshuSetNodeOnline("node-office", false);
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await expect(page.getByText("离线", { exact: true }).first()).toBeVisible();
  await expect(page.getByRole("button", { name: "使用 Codex 在 Release repo 新建任务" })).toBeDisabled();
});

test("marks a Thread first observed after hydration as new progress", async ({ page }) => {
  await page.evaluate(() => (window as unknown as { __yuanshuEmitThread: (nodeId: string, workspaceId: string, threadId: string) => void }).__yuanshuEmitThread("node-office", "workspace-office", "thread-external"));
  await expect(page.getByText("1 条新进展")).toBeVisible();
});

test("guides a revoked browser back to pairing", async ({ page }) => {
  await page.evaluate(() => localStorage.setItem("yuanshu-e2e-reauth", "1"));
  await page.reload();
  await expect(page.getByRole("alert")).toContainText("当前浏览器需要重新配对", { timeout: 15_000 });
  await expect(page.getByRole("link", { name: "重新配对" })).toHaveAttribute("href", "https://relay.test/pair");
});

test("captures the three product-design baselines", async ({ page }, testInfo) => {
  test.skip(process.env.YUANSHU_CAPTURE_WORKBENCH !== "1", "visual artifacts are updated explicitly");
  const output = path.join(process.cwd(), "..", "docs", "design", "web-workbench");
  await mkdir(output, { recursive: true });
  if (testInfo.project.name === "mobile-390-chromium") {
    await page.screenshot({ path: path.join(output, "mobile-home.png") });
    await page.getByRole("button", { name: /继续任务 Office release/ }).click();
    await expect(page.getByRole("heading", { name: "Office release" })).toBeVisible();
    await expect(page.getByText("Remote workbench ready")).toBeVisible();
    await page.screenshot({ path: path.join(output, "mobile-task-detail.png") });
    return;
  }
  if (testInfo.project.name === "ipad-landscape-webkit") {
    await page.getByRole("button", { name: /继续任务 Office release/ }).click();
    await expect(page.getByRole("heading", { name: "Office release" })).toBeVisible();
    await expect(page.getByText("Remote workbench ready")).toBeVisible();
    await page.screenshot({ path: path.join(output, "ipad-workbench.png") });
    return;
  }
  test.skip(true, "this project does not own a baseline artifact");
});

async function seedBrowserIdentity(page: Page): Promise<void> {
  await page.evaluate(async ({ ownerId, clientId }) => {
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.deleteDatabase("yuanshu-control-client");
      request.onsuccess = () => resolve();
      request.onerror = () => reject(request.error);
      request.onblocked = () => reject(new Error("database deletion blocked"));
    });
    const database = await new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open("yuanshu-control-client", 5);
      request.onupgradeneeded = () => {
        for (const store of ["keys", "event-cursors", "control-sequences", "node-bindings", "runtime-settings", "preferences"]) {
          if (!request.result.objectStoreNames.contains(store)) request.result.createObjectStore(store);
        }
      };
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    const transaction = database.transaction(["keys", "node-bindings", "runtime-settings", "preferences"], "readwrite");
    transaction.objectStore("keys").put({ ownerId, clientId, keyId: "key-e2e", privateKey: { e2e: true } }, "active");
    transaction.objectStore("node-bindings").put({ ownerId, nodeId: "node-office", name: "Office Mac", online: true }, `${ownerId}\u001fnode-office`);
    transaction.objectStore("node-bindings").put({ ownerId, nodeId: "node-home", name: "Home PC", online: true }, `${ownerId}\u001fnode-home`);
    transaction.objectStore("runtime-settings").put({ relayUrl: "wss://relay.test/web/connect", pairingUrl: "https://relay.test/pair" }, "active");
    transaction.objectStore("preferences").put("zh-CN", "language");
    await new Promise<void>((resolve, reject) => {
      transaction.oncomplete = () => resolve();
      transaction.onerror = () => reject(transaction.error);
      transaction.onabort = () => reject(transaction.error);
    });
    database.close();
  }, { ownerId: OWNER_ID, clientId: CLIENT_ID });
}

async function openInspector(page: Page, tab: "审批" | "文件"): Promise<void> {
  if ((page.viewportSize()?.width ?? 1280) < 768) {
    await page.getByRole("button", { name: "打开任务 Inspector" }).click();
  }
  await page.getByRole("tab", { name: new RegExp(tab) }).click();
}

async function installFakeRelay(page: Page): Promise<void> {
  await page.addInitScript(({ ownerId, clientId }) => {
    const sequences: Record<string, number> = {};
    let serverSequence = 0;
    const onlineNodes = new Set(["node-office", "node-home"]);
    (window as unknown as { __yuanshuSetNodeOnline: (nodeId: string, online: boolean) => void }).__yuanshuSetNodeOnline = (nodeId, online) => {
      if (online) onlineNodes.add(nodeId); else onlineNodes.delete(nodeId);
    };
    Object.defineProperty(crypto.subtle, "sign", {
      configurable: true,
      value: async () => new Uint8Array([1, 2, 3, 4]).buffer,
    });

    class FakeWebSocket {
      static readonly OPEN = 1;
      readonly protocol = "yuanshu-relay-v1";
      readyState = 0;
      onopen: (() => void) | null = null;
      onmessage: ((event: MessageEvent<string>) => void) | null = null;
      onerror: (() => void) | null = null;
      onclose: (() => void) | null = null;

      constructor() {
        setTimeout(() => {
          this.readyState = FakeWebSocket.OPEN;
          this.onopen?.();
          const rejected = localStorage.getItem("yuanshu-e2e-reauth") === "1";
          this.emit({ version: "1", type: "challenge", role: "control", connectionId: "connection-e2e", subjectId: rejected ? "revoked-client" : clientId, nonce: "nonce-e2e", expiresAt: new Date(Date.now() + 120_000).toISOString() });
        });
        (window as unknown as { __yuanshuEmitRuntime: (nodeId: string, state: string) => void }).__yuanshuEmitRuntime = (nodeId, state) => {
          const sequence = (sequences[nodeId] ?? 0) + 1;
          sequences[nodeId] = sequence;
          this.emit({ protocolVersion: "1.1", messageId: `event-${nodeId}-${sequence}`, type: "runtime.status", ownerId, nodeId, streamId: "node-events-v1.1", sequence, correlationId: "runtime-e2e", sentAt: new Date().toISOString(), payload: { state } });
        };
        (window as unknown as { __yuanshuEmitThread: (nodeId: string, workspaceId: string, threadId: string) => void }).__yuanshuEmitThread = (nodeId, workspaceId, threadId) => {
          const sequence = (sequences[nodeId] ?? 0) + 1;
          sequences[nodeId] = sequence;
          this.emit({ protocolVersion: "1.1", messageId: `event-${nodeId}-${sequence}`, type: "task.started", ownerId, nodeId, agentInstanceId: "codex-default", workspaceId, taskId: threadId, streamId: "node-events-v1.1", sequence, correlationId: "external-thread-e2e", sentAt: new Date().toISOString(), payload: { status: "running", title: "External task" } });
        };
      }

      send(data: string): void {
        const message = JSON.parse(data) as Record<string, unknown>;
        if (message.type === "authenticate") {
          if (localStorage.getItem("yuanshu-e2e-reauth") === "1") {
            this.close();
            return;
          }
          this.emit({ version: "1", type: "authenticated" });
          return;
        }
        this.handleControl(message);
      }

      close(): void {
        if (this.readyState === 3) return;
        this.readyState = 3;
        setTimeout(() => this.onclose?.());
      }

      private handleControl(control: Record<string, unknown>): void {
        const type = String(control.type);
        const nodeId = String(control.nodeId);
        const workspaceId = typeof control.workspaceId === "string" ? control.workspaceId : undefined;
        const taskId = typeof control.taskId === "string" ? control.taskId : undefined;
        const payload = control.payload as Record<string, unknown>;
        if (!onlineNodes.has(nodeId) && !new Set(["lease.acquire", "lease.renew", "lease.release", "lease.status", "notifications.list", "notifications.read"]).has(type)) return;

        if (type === "events.replay") {
          this.nodeEvent(control, "control.result", { status: "confirmed" });
          return;
        }
        if (type === "device.sync" || type === "workspace.list") {
          const office = nodeId === "node-office";
          this.nodeEvent(control, "device.status", { status: "online", runtime: "ready", name: office ? "Office Mac" : "Home PC", workspaces: [{ id: office ? "workspace-office" : "workspace-home", name: office ? "Release repo" : "Home repo", permissionProfile: "workspace-write", allowNetwork: false, agents: [{ agentInstanceId: "codex-default", default: true }] }] });
          this.nodeEvent(control, "control.result", { status: "confirmed" });
          return;
        }
        if (type === "agent.list" || type === "agent.read") {
          this.nodeEvent(control, "agent.snapshot", { agents: [{ id: "codex-default", adapterType: "codex", displayName: "Codex", version: "0.144.6", runtimeMode: "managed", status: "ready", providerType: "custom", customEndpoint: true, authenticationAvailable: true, configurationFingerprint: "sha256:e2e", capabilities: [{ id: "task.read", level: "full" }, { id: "task.start", level: "full" }, { id: "run.start", level: "full" }, { id: "run.steer", level: "full" }, { id: "run.interrupt", level: "full" }, { id: "interaction.resolve", level: "full" }] }] });
          this.nodeEvent(control, "control.result", { status: "confirmed" });
          return;
        }
        if (type === "task.list") {
          const office = nodeId === "node-office";
          this.nodeEvent(control, "task.snapshot", { tasks: [{ id: office ? "thread-release" : "thread-home", agentInstanceId: "codex-default", workspaceId: office ? "workspace-office" : "workspace-home", title: office ? "Office release" : "Home maintenance", preview: office ? "Ship the personal alpha" : "Update local scripts", status: office ? "running" : "completed", updatedAt: "2026-08-03T08:00:00Z", pendingInteractions: office ? 1 : 0 }] });
          this.nodeEvent(control, "control.result", { status: "confirmed" });
          return;
        }
        if (type === "task.read" && payload.includeDiffs === true) {
          this.nodeEvent(control, "task.snapshot", { task: { id: taskId, agentInstanceId: "codex-default", workspaceId, status: "running", historyState: "complete" }, runs: [{ id: "turn-release", status: "running" }] });
          this.nodeEvent({ ...control, runId: "turn-release", activityId: "diff-app" }, "diff.updated", { path: "src/app.ts", changeType: "modified", diff: "+const ready = true;", digest: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", totalBytes: 20, truncated: false });
          this.nodeEvent(control, "control.result", { status: "confirmed" });
          return;
        }
        if (type === "task.read") {
          if (taskId === "thread-new") {
            this.nodeEvent(control, "task.snapshot", { task: { id: "thread-new", agentInstanceId: "codex-default", workspaceId, title: "Explicit target task", status: "running", historyState: "complete" }, runs: [] });
            this.nodeEvent(control, "control.result", { status: "confirmed" });
            return;
          }
          this.nodeEvent(control, "task.snapshot", { task: { id: taskId, agentInstanceId: "codex-default", workspaceId, title: "Office release", preview: "Ship the personal alpha", status: "running", historyState: "complete" }, runs: [{ id: "turn-release", status: "running" }] });
          this.nodeEvent({ ...control, runId: "turn-release", activityId: "agent-1" }, "message.completed", { text: "Remote workbench ready" });
          this.nodeEvent({ ...control, runId: "turn-release", activityId: "file-app" }, "file.changed", { path: "src/app.ts", changeType: "modified" });
          this.nodeEvent({ ...control, runId: "turn-release", interactionId: "approval-1" }, "interaction.requested", { id: "approval-1", kind: "command_approval", status: "pending", operationDigest: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", risk: "high", summary: "Run release verification", expiresAt: new Date(Date.now() + 60_000).toISOString() });
          this.nodeEvent(control, "control.result", { status: "confirmed" });
          return;
        }
        if (type === "lease.status") {
          this.serverResult(control, { status: "confirmed", lease: { state: "none", epoch: 0 } });
          return;
        }
        if (type === "lease.acquire" || type === "lease.renew") {
          this.serverResult(control, { status: "confirmed", lease: { state: "held", leaseId: "lease-e2e", holderClientId: clientId, epoch: 1, expiresAt: new Date(Date.now() + 60_000).toISOString() } });
          return;
        }
        if (type === "lease.release") {
          this.serverResult(control, { status: "confirmed", lease: { state: "none", epoch: 2 } });
          return;
        }
        if (type === "notifications.list") {
          this.serverResult(control, { status: "confirmed", onlineNodeIds: [...onlineNodes], notifications: [{ id: "notification-1", nodeId: "node-office", workspaceId: "workspace-office", threadId: "thread-release", type: "approval.required", summary: "Office release is waiting for approval", sourceSequence: 5, createdAt: "2026-08-03T08:05:00Z", read: false }] });
          return;
        }
        if (type === "notifications.read") {
          this.serverResult(control, { status: "confirmed" });
          return;
        }
        if (type === "task.start") {
          (window as unknown as { __yuanshuStartedTarget?: unknown }).__yuanshuStartedTarget = { nodeId, agentInstanceId: control.agentInstanceId, workspaceId, input: payload.input };
          setTimeout(() => {
            const sequence = (sequences[nodeId] ?? 0) + 1;
            sequences[nodeId] = sequence;
            this.emit({ protocolVersion: "1.1", messageId: `event-${nodeId}-${sequence}`, type: "task.started", ownerId, nodeId, agentInstanceId: "codex-default", workspaceId, taskId: "thread-new", streamId: "node-events-v1.1", sequence, correlationId: String(control.messageId), sentAt: new Date().toISOString(), payload: { status: "running", title: "Explicit target task" } });
            this.nodeEvent(control, "control.result", { status: "confirmed" });
          }, 0);
          return;
        }
        if (type === "run.steer") {
          this.nodeEvent(control, "message.completed", { text: "Streaming follow-up received" }, "agent-follow-up");
          this.nodeEvent(control, "control.result", { status: "confirmed" });
          return;
        }
        if (type === "interaction.resolve") {
          this.nodeEvent(control, "interaction.resolved", { id: "approval-1", kind: "command_approval", status: payload.decision === "accept" ? "accepted" : "declined", operationDigest: payload.operationDigest, expiresAt: new Date(Date.now() + 60_000).toISOString() });
          this.nodeEvent(control, "control.result", { status: "confirmed" });
          return;
        }
        this.nodeEvent(control, "control.result", { status: "confirmed" });
      }

      private nodeEvent(control: Record<string, unknown>, type: string, payload: Record<string, unknown>, activityId?: string): void {
        const nodeId = String(control.nodeId);
        const sequence = (sequences[nodeId] ?? 0) + 1;
        sequences[nodeId] = sequence;
        this.emit({ protocolVersion: "1.1", messageId: `event-${nodeId}-${sequence}`, type, ownerId, nodeId, streamId: "node-events-v1.1", sequence, correlationId: String(control.messageId), sentAt: new Date().toISOString(), payload, ...(control.agentInstanceId ? { agentInstanceId: control.agentInstanceId } : {}), ...(control.workspaceId ? { workspaceId: control.workspaceId } : {}), ...(control.taskId ? { taskId: control.taskId } : {}), ...(control.runId ? { runId: control.runId } : {}), ...(control.activityId || activityId ? { activityId: control.activityId ?? activityId } : {}), ...(control.interactionId ? { interactionId: control.interactionId } : {}) });
      }

      private serverResult(control: Record<string, unknown>, payload: Record<string, unknown>): void {
        serverSequence += 1;
        this.emit({ protocolVersion: "1.1", messageId: `server-${serverSequence}`, type: "control.result", ownerId, nodeId: String(control.nodeId), streamId: `server-control-v1-${clientId}`, sequence: serverSequence, correlationId: String(control.messageId), sentAt: new Date().toISOString(), payload, ...(control.workspaceId ? { workspaceId: control.workspaceId } : {}), ...(control.taskId ? { taskId: control.taskId } : {}) });
      }

      private emit(message: Record<string, unknown>): void {
        setTimeout(() => this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(message) })));
      }
    }

    Object.defineProperty(window, "WebSocket", { value: FakeWebSocket, configurable: true });
  }, { ownerId: OWNER_ID, clientId: CLIENT_ID });
}
