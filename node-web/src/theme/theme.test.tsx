import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { applyTheme, readThemeMode, resolveTheme, saveThemeMode } from "./theme";
import { ThemeProvider, useTheme } from "./ThemeProvider";

function Probe() {
  const { mode, resolved } = useTheme();
  return <output>{mode}:{resolved}</output>;
}

describe("Node theme runtime", () => {
  it("supports system, light and dark modes", () => {
    localStorage.clear();
    expect(resolveTheme("system", true)).toBe("dark");
    expect(resolveTheme("system", false)).toBe("light");
    saveThemeMode("dark");
    expect(readThemeMode()).toBe("dark");
    render(<ThemeProvider><Probe /></ThemeProvider>);
    expect(screen.getByText("dark:dark")).toBeInTheDocument();
  });

  it("updates the Node document theme", () => {
    document.head.innerHTML = '<meta name="theme-color" content="">';
    applyTheme("light");
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')?.content).toBe("#F4F4F2");
  });
});
