import { useEffect, useState } from "react";

import { BrandMark } from "./BrandMark";
import { LanguageSwitch, useI18n } from "./i18n";
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
  const { t } = useI18n();
  return <main className="loading-screen"><LanguageSwitch /><BrandMark className="brand-mark-large" /><h1>{t("workbench.loading.title")}</h1><p>{t("workbench.loading.description")}</p><div className="loading-line" /></main>;
}

function PairingScreen({ pairingURL, settings, storage, reason, configured, onRestart }: { pairingURL: string; settings: RuntimeSettings; storage?: ControlStorage; reason?: string; configured: boolean; onRestart: () => void }) {
  const { t } = useI18n();
  return <main className="pairing-screen"><LanguageSwitch /><div className="pairing-card"><BrandMark className="brand-mark-large" /><p className="pairing-kicker">{t("workbench.pairing.kicker")}</p><h1>{t("workbench.pairing.title")}</h1><p>{configured ? t("setup.node.serverURL.help") : t("workbench.pairing.description")}</p>{reason && <small className="form-error">{reason}</small>}{!configured && pairingURL && <a className="button primary pairing-link" href={pairingURL}>{t("workbench.openPairing")}</a>}{storage ? <ConnectionSettings initial={settings} storage={storage} compact onSaved={onRestart} /> : <small className="form-error">{t("error.indexedDBUnavailable")}</small>}<div className="trust-note">{t("workbench.securityNote")}</div></div></main>;
}
