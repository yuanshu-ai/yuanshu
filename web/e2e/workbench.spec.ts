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
  await expect(page.locator(".lease-badge.held").getByText("可操作", { exact: true })).toBeVisible();

  const composer = page.getByLabel("任务指令");
  await composer.fill("Continue the release checks");
  await composer.press(process.platform === "darwin" ? "Meta+Enter" : "Control+Enter");
  await expect(page.getByText("设备已确认请求")).toBeVisible();
  await expect(page.getByText("Streaming follow-up received")).toBeVisible();
});

test("uses application dialogs for high-risk approval and loads Diff on demand", async ({ page }) => {
  await page.getByRole("button", { name: /Office release/ }).first().click();
  await page.getByRole("button", { name: "获取控制权" }).click();

  await page.getByRole("button", { name: "批准" }).click();
  await expect(page.getByRole("dialog", { name: "检查高风险操作" })).toBeVisible();
  await page.getByRole("button", { name: "继续确认" }).click();
  await expect(page.getByRole("dialog", { name: "确认批准操作" })).toBeVisible();
  await page.getByRole("button", { name: "发送批准" }).click();
  await expect(page.getByText("批准已由设备确认")).toBeVisible();

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

test("uses phone, tablet and desktop layout contracts", async ({ page }) => {
  const width = page.viewportSize()?.width ?? 1280;
  const contextRail = page.getByRole("complementary", { name: "设备和工作区" });
  const mobileNavigation = page.locator(".mobile-nav");
  if (width < 768) {
    await expect(contextRail).toBeHidden();
    await expect(mobileNavigation).toBeVisible();
  } else if (width < 1200) {
    await expect(contextRail).toBeHidden();
    await expect(mobileNavigation).toBeHidden();
    await expect(page.locator(".task-pane")).toBeVisible();
    await expect(page.getByRole("region", { name: "任务详情" })).toBeVisible();
  } else {
    await expect(contextRail).toBeVisible();
    await expect(mobileNavigation).toBeHidden();
  }
  if (width < 1200) {
    const selector = width < 768
      ? ".mobile-nav button, .topbar-attention"
      : ".desktop-nav button, .topbar-attention, .connection-state";
    const touchTargets = await page.locator(selector).evaluateAll((elements) =>
      elements.map((element) => {
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
  await dialog.getByRole("button", { name: /Release repo.*可修改工作区文件/ }).click();
  await dialog.getByRole("button", { name: "下一步" }).click();
  await dialog.getByLabel("你希望 Codex 完成什么？").fill("Create an explicit-target task");
  await dialog.getByRole("button", { name: "下一步" }).click();
  await expect(dialog.getByRole("region", { name: "执行目标" })).toContainText("Office Mac");
  await expect(dialog.getByRole("region", { name: "执行目标" })).toContainText("Release repo");
  await dialog.getByRole("button", { name: "确认并启动" }).click();

  await expect.poll(() => page.evaluate(() => (window as unknown as { __yuanshuStartedTarget?: unknown }).__yuanshuStartedTarget)).toEqual({ nodeId: "node-office", workspaceId: "workspace-office", input: "Create an explicit-target task" });
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
      const request = indexedDB.open("yuanshu-control-client", 4);
      request.onupgradeneeded = () => {
        for (const store of ["keys", "event-cursors", "control-sequences", "node-bindings", "runtime-settings"]) {
          if (!request.result.objectStoreNames.contains(store)) request.result.createObjectStore(store);
        }
      };
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    const transaction = database.transaction(["keys", "node-bindings", "runtime-settings"], "readwrite");
    transaction.objectStore("keys").put({ ownerId, clientId, keyId: "key-e2e", privateKey: { e2e: true } }, "active");
    transaction.objectStore("node-bindings").put({ ownerId, nodeId: "node-office", name: "Office Mac", online: true }, `${ownerId}\u001fnode-office`);
    transaction.objectStore("node-bindings").put({ ownerId, nodeId: "node-home", name: "Home PC", online: true }, `${ownerId}\u001fnode-home`);
    transaction.objectStore("runtime-settings").put({ relayUrl: "wss://relay.test/web/connect", pairingUrl: "https://relay.test/pair" }, "active");
    await new Promise<void>((resolve, reject) => {
      transaction.oncomplete = () => resolve();
      transaction.onerror = () => reject(transaction.error);
      transaction.onabort = () => reject(transaction.error);
    });
    database.close();
  }, { ownerId: OWNER_ID, clientId: CLIENT_ID });
}

async function installFakeRelay(page: Page): Promise<void> {
  await page.addInitScript(({ ownerId, clientId }) => {
    const sequences: Record<string, number> = {};
    let serverSequence = 0;
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
        const threadId = typeof control.threadId === "string" ? control.threadId : undefined;
        const turnId = typeof control.turnId === "string" ? control.turnId : undefined;
        const payload = control.payload as Record<string, unknown>;

        if (type === "events.replay") {
          this.nodeEvent(control, "control.result", { status: "confirmed" });
          return;
        }
        if (type === "device.sync" || type === "workspace.list") {
          const office = nodeId === "node-office";
          this.nodeEvent(control, "device.status", { status: "online", runtime: "ready", name: office ? "Office Mac" : "Home PC", workspaces: [{ id: office ? "workspace-office" : "workspace-home", name: office ? "Release repo" : "Home repo", permissionProfile: "workspace-write", allowNetwork: false }] });
          this.nodeEvent(control, "control.result", { status: "confirmed" });
          return;
        }
        if (type === "thread.list") {
          const office = nodeId === "node-office";
          this.nodeEvent(control, "thread.snapshot", { threads: [{ id: office ? "thread-release" : "thread-home", title: office ? "Office release" : "Home maintenance", preview: office ? "Ship the personal alpha" : "Update local scripts", status: office ? "running" : "completed", updatedAt: "2026-08-03T08:00:00Z", pendingApprovals: office ? 1 : 0 }] });
          this.nodeEvent(control, "control.result", { status: "confirmed" });
          return;
        }
        if (type === "thread.read" && payload.includeDiffs === true) {
          this.nodeEvent(control, "thread.snapshot", { historyState: "complete", turns: [{ id: "turn-release", status: "running", items: [{ id: "diff-app", kind: "diff", path: "src/app.ts", changeType: "modified", diff: "+const ready = true;", digest: "sha256-diff", totalBytes: 20 }] }] });
          this.nodeEvent(control, "control.result", { status: "confirmed" });
          return;
        }
        if (type === "thread.read") {
          if (control.threadId === "thread-new") {
            this.nodeEvent(control, "thread.snapshot", { title: "Explicit target task", status: "running", historyState: "complete", turns: [] });
            this.nodeEvent(control, "control.result", { status: "confirmed" });
            return;
          }
          this.nodeEvent(control, "thread.snapshot", { title: "Office release", preview: "Ship the personal alpha", status: "running", historyState: "complete", turns: [{ id: "turn-release", status: "running", items: [{ id: "agent-1", kind: "agent_message", status: "completed", text: "Remote workbench ready" }, { id: "file-app", kind: "file_change", status: "completed", path: "src/app.ts", changeType: "modified" }] }], pendingApprovals: [{ approvalId: "approval-1", turnId: "turn-release", itemId: "command-1", operationDigest: "sha256-approval", kind: "command", risk: "high", summary: "Run release verification" }] });
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
          this.serverResult(control, { status: "confirmed", notifications: [{ id: "notification-1", nodeId: "node-office", workspaceId: "workspace-office", threadId: "thread-release", type: "approval.required", summary: "Office release is waiting for approval", sourceSequence: 5, createdAt: "2026-08-03T08:05:00Z", read: false }] });
          return;
        }
        if (type === "notifications.read") {
          this.serverResult(control, { status: "confirmed" });
          return;
        }
        if (type === "thread.start") {
          (window as unknown as { __yuanshuStartedTarget?: unknown }).__yuanshuStartedTarget = { nodeId, workspaceId, input: payload.input };
          setTimeout(() => {
            const sequence = (sequences[nodeId] ?? 0) + 1;
            sequences[nodeId] = sequence;
            this.emit({ protocolVersion: "1.0", messageId: `event-${nodeId}-${sequence}`, type: "thread.started", ownerId, nodeId, workspaceId, threadId: "thread-new", streamId: "node-events-v1", sequence, correlationId: String(control.messageId), sentAt: new Date().toISOString(), payload: { status: "running", title: "Explicit target task" } });
            this.nodeEvent(control, "control.result", { status: "confirmed" });
          }, 0);
          return;
        }
        if (type === "turn.steer") {
          this.nodeEvent(control, "agent.message.completed", { text: "Streaming follow-up received" }, "agent-follow-up");
          this.nodeEvent(control, "control.result", { status: "confirmed" });
          return;
        }
        if (type === "approval.resolve") {
          this.nodeEvent(control, "approval.resolved", { approvalId: "approval-1", decision: payload.decision, operationDigest: payload.operationDigest }, "command-1");
          this.nodeEvent(control, "control.result", { status: "confirmed" });
          return;
        }
        this.nodeEvent(control, "control.result", { status: "confirmed" });
      }

      private nodeEvent(control: Record<string, unknown>, type: string, payload: Record<string, unknown>, itemId?: string): void {
        const nodeId = String(control.nodeId);
        const sequence = (sequences[nodeId] ?? 0) + 1;
        sequences[nodeId] = sequence;
        this.emit({ protocolVersion: "1.0", messageId: `event-${nodeId}-${sequence}`, type, ownerId, nodeId, streamId: "node-events-v1", sequence, correlationId: String(control.messageId), sentAt: new Date().toISOString(), payload, ...(control.workspaceId ? { workspaceId: control.workspaceId } : {}), ...(control.threadId ? { threadId: control.threadId } : {}), ...(control.turnId ? { turnId: control.turnId } : {}), ...(itemId ? { itemId } : {}) });
      }

      private serverResult(control: Record<string, unknown>, payload: Record<string, unknown>): void {
        serverSequence += 1;
        this.emit({ protocolVersion: "1.0", messageId: `server-${serverSequence}`, type: "control.result", ownerId, nodeId: String(control.nodeId), streamId: `server-control-v1-${clientId}`, sequence: serverSequence, correlationId: String(control.messageId), sentAt: new Date().toISOString(), payload, ...(control.workspaceId ? { workspaceId: control.workspaceId } : {}), ...(control.threadId ? { threadId: control.threadId } : {}) });
      }

      private emit(message: Record<string, unknown>): void {
        setTimeout(() => this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(message) })));
      }
    }

    Object.defineProperty(window, "WebSocket", { value: FakeWebSocket, configurable: true });
  }, { ownerId: OWNER_ID, clientId: CLIENT_ID });
}
