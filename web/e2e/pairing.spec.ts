import { expect, test, type Page, type Route } from "@playwright/test";

type PairingScenario = {
  approved: boolean;
  status?: "claimed" | "declined" | "expired";
  nodeId: string;
  ownerId: string;
  claims: Array<Record<string, string>>;
};

test.beforeEach(async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chromium", "Pairing browser flow is origin-level behavior; run once in Chromium");
  await installPairingBrowser(page);
});

test("pairs a fresh browser, persists a non-exportable identity, and opens the workbench", async ({ page }) => {
  const scenario = pairingScenario("node-office");
  await mockPairingAPI(page, scenario);
  await page.route("**/yuanshu.config.json", route => route.fulfill({
    contentType: "application/json",
    body: JSON.stringify({ relayUrl: "wss://relay.test/web/connect", pairingUrl: "/pair" }),
  }));

  await page.goto("/pair/#pair-fresh.secret-fresh");
  await page.getByLabel("设备名称").fill("Phone Safari");
  await page.getByRole("button", { name: "请求连接" }).click();

  await expect(page.getByText("HTTPS/WSS 已安全连接")).toBeVisible();
  expect(scenario.claims).toHaveLength(1);
  expect(scenario.claims[0].publicKey).toMatch(/^[A-Za-z0-9_-]{43}$/);
  const stored = await readStoredPairing(page, scenario.ownerId);
  expect(stored.version).toBe(4);
  expect(stored.stores).toEqual(["control-sequences", "event-cursors", "keys", "node-bindings", "runtime-settings"]);
  expect(stored.clientId).toBe(scenario.claims[0].clientId);
  expect(stored.privateKeyExtractable).toBe(false);
  expect(stored.nodeIds).toEqual(["node-office"]);
  expect(stored.pendingCount).toBe(0);
  expect(await page.evaluate(() => ({ hash: location.hash, localStorage: { ...localStorage } }))).toEqual({ hash: "", localStorage: {} });

  await page.getByRole("link", { name: "打开工作台" }).click();
  await expect(page.getByText("远枢", { exact: true }).first()).toBeVisible();
});

test("upgrades a v3 database, preserves cursors, and reuses one identity for a second Node", async ({ page }) => {
  await page.goto("/yuanshu.config.example.json");
  await seedVersionThreeIdentity(page);

  const first = pairingScenario("node-office");
  await mockPairingAPI(page, first);
  await page.goto("/pair/#pair-office.secret-office");
  await page.getByRole("button", { name: "请求连接" }).click();
  await expect(page.getByText("HTTPS/WSS 已安全连接")).toBeVisible();

  await page.unroute("**/v1/control-client-pairings/**");
  const second = pairingScenario("node-home");
  await mockPairingAPI(page, second);
  await page.goto("/pair/#pair-home.secret-home");
  await page.getByLabel("设备名称").fill("Same browser");
  await page.getByRole("button", { name: "请求连接" }).click();
  await expect(page.getByText("HTTPS/WSS 已安全连接")).toBeVisible();

  expect(first.claims[0].clientId).toBe(second.claims[0].clientId);
  expect(first.claims[0].keyId).toBe(second.claims[0].keyId);
  expect(first.claims[0].publicKey).toBe(second.claims[0].publicKey);
  const stored = await readStoredPairing(page, second.ownerId);
  expect(stored.nodeIds).toEqual(["node-home", "node-office"]);
  expect(stored.cursor).toBe(37);
  expect(stored.controlSequence).toBe(11);
  expect(stored.runtimeSettingsStore).toBe(true);
});

test("resumes the same pending identity after refresh and an idempotent repeated claim", async ({ page }) => {
  const scenario = pairingScenario("node-office");
  scenario.approved = false;
  await mockPairingAPI(page, scenario);

  await page.goto("/pair/#pair-resume.secret-resume");
  await page.getByLabel("设备名称").fill("Travel browser");
  await page.getByRole("button", { name: "请求连接" }).click();
  await expect.poll(() => scenario.claims.length).toBe(1);
  await expect(page.getByText("等待办公室电脑确认此指纹")).toBeVisible();

  await page.reload();
  scenario.approved = true;
  await page.getByLabel("设备名称").fill("Travel browser");
  await page.getByRole("button", { name: "请求连接" }).click();
  await expect(page.getByText("HTTPS/WSS 已安全连接")).toBeVisible();
  expect(scenario.claims).toHaveLength(2);
  expect(scenario.claims[0].clientId).toBe(scenario.claims[1].clientId);
  expect(scenario.claims[0].publicKey).toBe(scenario.claims[1].publicKey);
});

test("shows actionable expiry and revoked-identity states", async ({ page }) => {
  const expired = pairingScenario("node-office");
  expired.status = "expired";
  await mockPairingAPI(page, expired);
  await page.goto("/pair/#pair-expired.secret-expired");
  await page.getByRole("button", { name: "请求连接" }).click();
  await expect(page.getByText("配对链接已过期，请从办公室电脑重新生成")).toBeVisible();
  await expect(page.getByRole("button", { name: "请求连接" })).toBeEnabled();

  await seedCurrentIdentity(page);
  await page.evaluate(() => { window.name = "revoked"; });
  await page.goto("/pair");
  await expect(page.getByText("控制端身份未通过认证，请重新配对")).toBeVisible();
});

function pairingScenario(nodeId: string): PairingScenario {
  return { approved: true, nodeId, ownerId: "owner-pairing-e2e", claims: [] };
}

async function mockPairingAPI(page: Page, scenario: PairingScenario): Promise<void> {
  await page.route("**/v1/control-client-pairings/**", async (route: Route) => {
    const request = route.request();
    if (request.url().endsWith("/claim")) {
      scenario.claims.push(request.postDataJSON() as Record<string, string>);
      await route.fulfill({ status: 202, contentType: "application/json", body: JSON.stringify({ status: "claimed", fingerprint: "ABCD EFGH" }) });
      return;
    }
    const status = scenario.status ?? (scenario.approved ? "approved" : "claimed");
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(status === "approved" ? { status, ownerId: scenario.ownerId, nodeId: scenario.nodeId } : { status }),
    });
  });
}

async function installPairingBrowser(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const nativeSetTimeout = window.setTimeout.bind(window);
    Object.defineProperty(window, "setTimeout", {
      configurable: true,
      value: (handler: TimerHandler, timeout?: number, ...args: unknown[]) => nativeSetTimeout(handler, Math.min(timeout ?? 0, 10), ...args),
    });
    (window as unknown as { __pairSocketMode: string }).__pairSocketMode = window.name || "success";
    class PairingWebSocket {
      static readonly OPEN = 1;
      readyState = 0;
      onopen: (() => void) | null = null;
      onmessage: ((event: MessageEvent<string>) => void) | null = null;
      onerror: (() => void) | null = null;
      onclose: (() => void) | null = null;
      private readonly clientId: string;

      constructor(url: string) {
        this.clientId = new URL(url).searchParams.get("clientId") ?? "";
        nativeSetTimeout(() => {
          this.readyState = PairingWebSocket.OPEN;
          this.onopen?.();
          const mode = (window as unknown as { __pairSocketMode: string }).__pairSocketMode;
          if (mode === "revoked") {
            this.close();
            return;
          }
          this.emit({ version: "1", type: "challenge", role: "control", connectionId: "pairing-e2e", subjectId: this.clientId, nonce: "pairing-nonce", expiresAt: new Date(Date.now() + 120_000).toISOString() });
        });
      }

      send(data: string): void {
        const value = JSON.parse(data) as { type?: string };
        if (value.type === "authenticate") this.emit({ version: "1", type: "authenticated" });
      }

      close(): void {
        if (this.readyState === 3) return;
        this.readyState = 3;
        nativeSetTimeout(() => this.onclose?.());
      }

      private emit(value: Record<string, string>): void {
        nativeSetTimeout(() => this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(value) })));
      }
    }
    Object.defineProperty(window, "WebSocket", { configurable: true, value: PairingWebSocket });
  });
}

async function seedVersionThreeIdentity(page: Page): Promise<void> {
  await page.evaluate(async () => {
    const keys = await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"]);
    const publicKey = await crypto.subtle.exportKey("raw", keys.publicKey);
    const encoded = btoa(String.fromCharCode(...new Uint8Array(publicKey))).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
    const database = await new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open("yuanshu-control-client", 3);
      request.onupgradeneeded = () => {
        for (const store of ["keys", "event-cursors", "control-sequences", "node-bindings"]) request.result.createObjectStore(store);
      };
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    const transaction = database.transaction(["keys", "event-cursors", "control-sequences"], "readwrite");
    transaction.objectStore("keys").put({ ownerId: "owner-pairing-e2e", clientId: "client-existing", keyId: "key-existing", name: "Existing browser", publicKey: encoded, privateKey: keys.privateKey }, "active");
    transaction.objectStore("event-cursors").put(37, "owner-pairing-e2e\u001fnode-old\u001fnode-events-v1");
    transaction.objectStore("control-sequences").put(11, "owner-pairing-e2e\u001fnode-old\u001fclient-existing\u001fkey-existing");
    await new Promise<void>((resolve, reject) => {
      transaction.oncomplete = () => resolve();
      transaction.onerror = () => reject(transaction.error);
      transaction.onabort = () => reject(transaction.error);
    });
    database.close();
  });
}

async function seedCurrentIdentity(page: Page): Promise<void> {
  await page.evaluate(async () => {
    const keys = await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"]);
    const publicKey = await crypto.subtle.exportKey("raw", keys.publicKey);
    const encoded = btoa(String.fromCharCode(...new Uint8Array(publicKey))).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
    const database = await new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open("yuanshu-control-client", 4);
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    const transaction = database.transaction("keys", "readwrite");
    transaction.objectStore("keys").put({ ownerId: "owner-pairing-e2e", clientId: "client-revoked", keyId: "key-revoked", name: "Revoked browser", publicKey: encoded, privateKey: keys.privateKey }, "active");
    await new Promise<void>((resolve, reject) => {
      transaction.oncomplete = () => resolve();
      transaction.onerror = () => reject(transaction.error);
      transaction.onabort = () => reject(transaction.error);
    });
    database.close();
  });
}

async function readStoredPairing(page: Page, ownerId: string) {
  return page.evaluate(async owner => {
    const database = await new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open("yuanshu-control-client", 4);
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    const transaction = database.transaction(["keys", "node-bindings", "event-cursors", "control-sequences"], "readonly");
    const read = <T>(request: IDBRequest<T>) => new Promise<T>((resolve, reject) => {
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    const active = await read<Record<string, unknown>>(transaction.objectStore("keys").get("active"));
    const pending = await read<IDBValidKey[]>(transaction.objectStore("keys").getAllKeys(IDBKeyRange.bound("pending:", "pending:\uffff")));
    const nodes = await read<Array<{ ownerId: string; nodeId: string }>>(transaction.objectStore("node-bindings").getAll());
    const cursor = await read<number>(transaction.objectStore("event-cursors").get(`${owner}\u001fnode-old\u001fnode-events-v1`));
    const controlSequence = await read<number>(transaction.objectStore("control-sequences").get(`${owner}\u001fnode-old\u001fclient-existing\u001fkey-existing`));
    const stores = [...database.objectStoreNames].sort();
    const result = {
      version: database.version,
      stores,
      clientId: active?.clientId,
      privateKeyExtractable: (active?.privateKey as CryptoKey | undefined)?.extractable,
      nodeIds: nodes.filter(node => node.ownerId === owner).map(node => node.nodeId).sort(),
      pendingCount: pending.length,
      cursor,
      controlSequence,
      runtimeSettingsStore: database.objectStoreNames.contains("runtime-settings"),
    };
    database.close();
    return result;
  }, ownerId);
}
