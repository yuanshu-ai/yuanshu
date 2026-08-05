export type ThemeMode = "system" | "light" | "dark";
export type ResolvedTheme = Exclude<ThemeMode, "system">;

export const THEME_STORAGE_KEY = "yuanshu.theme";
export const THEME_COLORS: Record<ResolvedTheme, string> = { light: "#F4F4F2", dark: "#151515" };

function browserStorage(): Storage | undefined {
  if (typeof window === "undefined") return undefined;
  try { return window.localStorage; } catch { return undefined; }
}

export function isThemeMode(value: unknown): value is ThemeMode { return value === "system" || value === "light" || value === "dark"; }
export function readThemeMode(storage: Pick<Storage, "getItem"> | undefined = browserStorage()): ThemeMode { try { const value = storage?.getItem(THEME_STORAGE_KEY); return isThemeMode(value) ? value : "system"; } catch { return "system"; } }
export function systemTheme(mediaQuery: Pick<MediaQueryList, "matches"> | undefined = typeof window !== "undefined" && typeof window.matchMedia === "function" ? window.matchMedia("(prefers-color-scheme: dark)") : undefined): ResolvedTheme { return mediaQuery?.matches ? "dark" : "light"; }
export function resolveTheme(mode: ThemeMode, prefersDark = typeof window !== "undefined" && typeof window.matchMedia === "function" ? window.matchMedia("(prefers-color-scheme: dark)").matches : false): ResolvedTheme { return mode === "system" ? (prefersDark ? "dark" : "light") : mode; }
export function applyTheme(mode: ThemeMode, prefersDark?: boolean): ResolvedTheme { const resolved = resolveTheme(mode, prefersDark); if (typeof document === "undefined") return resolved; document.documentElement.dataset.theme = resolved; document.documentElement.style.colorScheme = resolved; const meta = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]'); if (meta) meta.content = THEME_COLORS[resolved]; return resolved; }
export function saveThemeMode(mode: ThemeMode, storage: Pick<Storage, "setItem"> | undefined = browserStorage()): void { try { storage?.setItem(THEME_STORAGE_KEY, mode); } catch { /* Session-only fallback. */ } }
