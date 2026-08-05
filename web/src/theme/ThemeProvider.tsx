import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

import { THEME_STORAGE_KEY, applyTheme, isThemeMode, readThemeMode, saveThemeMode, systemTheme, type ResolvedTheme, type ThemeMode } from "./theme";

type ThemeContextValue = { mode: ThemeMode; resolved: ResolvedTheme; setMode: (mode: ThemeMode) => void };
const ThemeContext = createContext<ThemeContextValue>({ mode: "system", resolved: "light", setMode: () => undefined });

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [mode, setModeState] = useState<ThemeMode>(() => readThemeMode());
  const [systemDark, setSystemDark] = useState(() => systemTheme() === "dark");
  const resolved = mode === "system" ? (systemDark ? "dark" : "light") : mode;

  useEffect(() => {
    const media = typeof window !== "undefined" && typeof window.matchMedia === "function" ? window.matchMedia("(prefers-color-scheme: dark)") : undefined;
    if (!media) return undefined;
    setSystemDark(media.matches);
    const onChange = (event: MediaQueryListEvent) => setSystemDark(event.matches);
    if (media.addEventListener) media.addEventListener("change", onChange);
    else media.addListener?.(onChange);
    return () => {
      if (media.removeEventListener) media.removeEventListener("change", onChange);
      else media.removeListener?.(onChange);
    };
  }, []);

  useEffect(() => { applyTheme(mode, systemDark); }, [mode, systemDark]);

  useEffect(() => {
    if (typeof window === "undefined") return undefined;
    const onStorage = (event: StorageEvent) => { if (event.key === THEME_STORAGE_KEY) setModeState(isThemeMode(event.newValue) ? event.newValue : "system"); };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);

  const setMode = useCallback((value: ThemeMode) => { setModeState(value); saveThemeMode(value); }, []);
  const value = useMemo(() => ({ mode, resolved, setMode }), [mode, resolved, setMode]);
  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeContextValue { return useContext(ThemeContext); }
