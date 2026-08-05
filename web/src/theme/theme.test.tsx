import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { applyTheme, readThemeMode, resolveTheme, saveThemeMode } from "./theme";
import { ThemeProvider, useTheme } from "./ThemeProvider";

function Probe() {
  const { mode, resolved, setMode } = useTheme();
  return <div><output>{mode}:{resolved}</output><button onClick={() => setMode("dark")}>dark</button></div>;
}

describe("theme runtime", () => {
  beforeEach(() => {
    localStorage.clear();
    document.head.innerHTML = '<meta name="theme-color" content="">';
    document.body.innerHTML = '<div id="root"></div>';
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    document.documentElement.removeAttribute("data-theme");
    document.documentElement.style.colorScheme = "";
  });

  it("defaults invalid stored values to system and resolves explicitly", () => {
    localStorage.setItem("yuanshu.theme", "sepia");
    expect(readThemeMode()).toBe("system");
    expect(resolveTheme("system", true)).toBe("dark");
    expect(resolveTheme("system", false)).toBe("light");
  });

  it("applies the resolved theme to document chrome", () => {
    const meta = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')!;
    expect(applyTheme("dark")).toBe("dark");
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(document.documentElement.style.colorScheme).toBe("dark");
    expect(meta.content).toBe("#151515");
    expect(applyTheme("light")).toBe("light");
    expect(meta.content).toBe("#F4F4F2");
  });

  it("persists a selected mode without affecting application state", () => {
    saveThemeMode("light");
    expect(localStorage.getItem("yuanshu.theme")).toBe("light");
    render(<ThemeProvider><Probe /></ThemeProvider>);
    expect(screen.getByText("light:light")).toBeInTheDocument();
    act(() => screen.getByRole("button", { name: "dark" }).click());
    expect(screen.getByText("dark:dark")).toBeInTheDocument();
    expect(localStorage.getItem("yuanshu.theme")).toBe("dark");
  });

  it("follows system preference changes and removes its listener", () => {
    let listener: ((event: MediaQueryListEvent) => void) | undefined;
    const media = {
      matches: false,
      addEventListener: (_type: string, callback: (event: MediaQueryListEvent) => void) => { listener = callback; },
      removeEventListener: vi.fn(),
    } as unknown as MediaQueryList;
    vi.stubGlobal("matchMedia", vi.fn(() => media));
    const view = render(<ThemeProvider><Probe /></ThemeProvider>);
    expect(screen.getByText("system:light")).toBeInTheDocument();
    act(() => listener?.({ matches: true } as MediaQueryListEvent));
    expect(screen.getByText("system:dark")).toBeInTheDocument();
    view.unmount();
    expect(media.removeEventListener).toHaveBeenCalledTimes(1);
  });
});
