import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { App } from "./App";

describe("App", () => {
  it("shows the pre-alpha project status without claiming remote control", () => {
    render(<App />);

    expect(screen.getByRole("heading", { name: "Yuanshu · 远枢" })).toBeInTheDocument();
    expect(screen.getByText("Pre-alpha")).toBeInTheDocument();
    expect(screen.getByText(/当前构建不具备远程控制能力/)).toBeInTheDocument();
  });
});
