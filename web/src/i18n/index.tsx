import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

import { CONTROL_STORES, openControlDatabase, requestValue, transactionComplete } from "../../../internal/server/pairing-web/storage.js";
import { catalogs, type Locale, type MessageKey } from "./catalog.generated";

const preferenceKey = "language";
type LanguageContextValue = { locale: Locale; setLocale: (locale: Locale) => Promise<void>; t: (key: MessageKey, values?: Record<string, string | number>) => string };
const LanguageContext = createContext<LanguageContextValue | null>(null);

export function LanguageProvider({ children }: { children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale | null>(null);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    let active = true;
    void loadLanguage().then((value) => {
      if (active) {
        setLocaleState(value);
        setLoaded(true);
      }
    });
    return () => { active = false; };
  }, []);

  const setLocale = useCallback(async (value: Locale) => {
    await saveLanguage(value);
    document.documentElement.lang = value;
    setLocaleState(value);
  }, []);

  useEffect(() => {
    if (locale) document.documentElement.lang = locale;
  }, [locale]);

  const context = useMemo<LanguageContextValue | null>(() => locale ? ({
    locale,
    setLocale,
    t: (key, values) => interpolate(catalogs[locale][key], values),
  }) : null, [locale, setLocale]);

  if (!loaded) return <main className="language-loading" aria-busy="true" />;
  if (!context) return <LanguageChoice onChoose={setLocale} />;
  return <LanguageContext.Provider value={context}>{children}</LanguageContext.Provider>;
}

export function useI18n(): LanguageContextValue {
  const value = useContext(LanguageContext);
  return value ?? fallbackLanguage;
}

export function LanguageSwitch({ compact = false }: { compact?: boolean }) {
  const { locale, setLocale, t } = useI18n();
  return <div className={`language-switch${compact ? " compact" : ""}`} aria-label={t("language.switch")}>
    <button type="button" className={locale === "zh-CN" ? "active" : ""} aria-pressed={locale === "zh-CN"} onClick={() => void setLocale("zh-CN")}>中文</button>
    <span aria-hidden="true">|</span>
    <button type="button" className={locale === "en-US" ? "active" : ""} aria-pressed={locale === "en-US"} onClick={() => void setLocale("en-US")}>English</button>
  </div>;
}

function LanguageChoice({ onChoose }: { onChoose: (locale: Locale) => Promise<void> }) {
  return <main className="language-choice"><section>
    <p className="eyebrow">Yuanshu</p>
    <h1>选择语言 · Choose a language</h1>
    <p>选择界面语言，之后可以随时切换。<br />Choose the interface language. You can switch at any time.</p>
    <div className="language-choice-actions">
      <button type="button" className="button primary" onClick={() => void onChoose("zh-CN")}>中文</button>
      <button type="button" className="button" onClick={() => void onChoose("en-US")}>English</button>
    </div>
  </section></main>;
}

async function loadLanguage(): Promise<Locale | null> {
  try {
    const database = await openControlDatabase();
    const value = await requestValue<string | undefined>(database.transaction(CONTROL_STORES.preferences, "readonly").objectStore(CONTROL_STORES.preferences).get(preferenceKey));
    database.close();
    return value === "zh-CN" || value === "en-US" ? value : null;
  } catch {
    return null;
  }
}

async function saveLanguage(locale: Locale): Promise<void> {
  const database = await openControlDatabase();
  const transaction = database.transaction(CONTROL_STORES.preferences, "readwrite");
  transaction.objectStore(CONTROL_STORES.preferences).put(locale, preferenceKey);
  await transactionComplete(transaction);
  database.close();
}

function interpolate(message: string, values?: Record<string, string | number>): string {
  if (!values) return message;
  return message.replace(/\{([A-Za-z][A-Za-z0-9]*)\}/g, (_, key: string) => String(values[key] ?? `{${key}}`));
}

const fallbackLanguage: LanguageContextValue = {
  locale: "zh-CN",
  setLocale: async () => {},
  t: (key, values) => interpolate(catalogs["zh-CN"][key], values),
};
