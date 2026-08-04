import { useEffect, useState } from "react";

import { BrandMark } from "./BrandMark";
import { IndexedDBControlStorage, type ControlStorage, type StoredControlIdentity } from "./relay/storage";
import { loadRuntimeSettings, type RuntimeSettings } from "./relay/runtime-config";
import { ConnectionSettings } from "./workbench/Settings";
import { Workbench } from "./workbench/Workbench";
import { WorkbenchSession } from "./workbench/session";

type BootState =
  | { status: "loading" }
  | { status: "pairing"; reason?: string; storage?: ControlStorage; settings: RuntimeSettings }
  | { status: "config"; identity: StoredControlIdentity; storage: ControlStorage; settings: RuntimeSettings }
  | { status: "ready"; session: WorkbenchSession; storage: ControlStorage; settings: RuntimeSettings };

export function WorkbenchApp() {
  const [boot, setBoot] = useState<BootState>({ status: "loading" });
  const [generation, setGeneration] = useState(0);

  useEffect(() => {
    let disposed = false;
    let activeSession: WorkbenchSession | undefined;
    const bootstrap = async () => {
      let storage: ControlStorage | undefined;
      let settings: RuntimeSettings = { relayUrl: "", pairingUrl: "/pair" };
      try {
        storage = new IndexedDBControlStorage();
        const identity = await storage.getActiveIdentity();
        settings = await loadRuntimeSettings(storage);
        if (!identity) {
          if (!disposed) setBoot({ status: "pairing", reason: "尚未找到控制端身份", storage, settings });
          return;
        }
        if (!settings.relayUrl) {
          if (!disposed) setBoot({ status: "config", identity, storage, settings });
          return;
        }
        activeSession = new WorkbenchSession({ identity, settings, storage });
        await activeSession.initialize();
        if (disposed) {
          activeSession.close();
          return;
        }
        setBoot({ status: "ready", session: activeSession, storage, settings });
        activeSession.connect();
      } catch (error) {
        activeSession?.close();
        if (!disposed) setBoot({ status: "pairing", reason: error instanceof Error ? error.message : "浏览器安全存储不可用", storage, settings });
      }
    };
    setBoot({ status: "loading" });
    void bootstrap();
    return () => {
      disposed = true;
      activeSession?.close();
    };
  }, [generation]);

  if (boot.status === "loading") return <LoadingScreen />;
  if (boot.status === "pairing" || boot.status === "config") return <PairingScreen pairingURL={boot.settings.pairingUrl} settings={boot.settings} storage={boot.storage} reason={boot.status === "pairing" ? boot.reason : undefined} configured={boot.status === "config"} onRestart={() => setGeneration((value) => value + 1)} />;
  return <Workbench session={boot.session} storage={boot.storage} settings={boot.settings} onSettingsSaved={() => setGeneration((value) => value + 1)} />;
}

export const App = WorkbenchApp;

function LoadingScreen() {
  return <main className="loading-screen"><BrandMark className="brand-mark-large" /><h1>正在恢复工作台</h1><p>从浏览器安全存储读取控制端身份和本地上下文。</p><div className="loading-line" /></main>;
}

function PairingScreen({ pairingURL, settings, storage, reason, configured, onRestart }: { pairingURL: string; settings: RuntimeSettings; storage?: ControlStorage; reason?: string; configured: boolean; onRestart: () => void }) {
  return <main className="pairing-screen"><div className="pairing-card"><BrandMark className="brand-mark-large" /><p className="pairing-kicker">个人控制端</p><h1>连接你的 Codex 工作区</h1><p>{configured ? "填写办公室或家庭电脑的 HTTPS 和 WSS 地址，保存后即可从手机连接。" : "先从办公室或家庭电脑生成配对链接。控制端身份只保存在当前浏览器。"}</p>{reason && <small className="form-error">{reason}</small>}{!configured && pairingURL && <a className="button primary pairing-link" href={pairingURL}>打开配对页</a>}{storage ? <ConnectionSettings initial={settings} storage={storage} compact onSaved={onRestart} /> : <small className="form-error">浏览器 IndexedDB 不可用，无法保存连接设置。</small>}<div className="trust-note">私钥不会进入 URL、日志或 Server。</div></div></main>;
}
