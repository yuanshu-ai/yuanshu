import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

import { catalogs, type Locale, type MessageKey } from "./catalog.generated";

type LanguageContextValue = { locale: Locale; setLocale: (locale: Locale) => void; t: (key: MessageKey, values?: Record<string, string | number>) => string };
const LanguageContext = createContext<LanguageContextValue | null>(null);

export function LanguageProvider({ initialLocale, onChange, children }: { initialLocale?: string; onChange?: (locale: Locale) => void; children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(initialLocale === "en-US" ? "en-US" : "zh-CN");
  const setLocale = useCallback((value: Locale) => {
    document.documentElement.lang = value;
    setLocaleState(value);
    onChange?.(value);
  }, [onChange]);
  useEffect(() => { document.documentElement.lang = locale; }, [locale]);
  const value = useMemo<LanguageContextValue>(() => ({ locale, setLocale, t: (key, values) => interpolate(catalogs[locale][key], values) }), [locale, setLocale]);
  return <LanguageContext.Provider value={value}>{children}</LanguageContext.Provider>;
}

export function useI18n() {
  const value = useContext(LanguageContext);
  return value ?? fallbackLanguage;
}

const fallbackLanguage: LanguageContextValue = {
  locale: "zh-CN",
  setLocale: () => {},
  t: (key, values) => interpolate(catalogs["zh-CN"][key], values),
};

export function LanguageSwitch() {
  const { locale, setLocale, t } = useI18n();
  return <div className="language-switch" aria-label={t("language.switch")}>
    <button type="button" className={locale === "zh-CN" ? "active" : ""} onClick={() => setLocale("zh-CN")}>中文</button>
    <span>|</span>
    <button type="button" className={locale === "en-US" ? "active" : ""} onClick={() => setLocale("en-US")}>English</button>
  </div>;
}

function interpolate(message: string, values?: Record<string, string | number>): string {
  if (!values) return message;
  return message.replace(/\{([A-Za-z][A-Za-z0-9]*)\}/g, (_, key: string) => String(values[key] ?? `{${key}}`));
}
