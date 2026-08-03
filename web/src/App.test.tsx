import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { App } from "./App";

describe("App", () => {
  it("shows a pairing entry when this browser has no control identity", async () => {
    render(<App />);

    expect(await screen.findByRole("heading", { name: "连接你的 Codex 工作区" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /打开配对页/ })).toBeInTheDocument();
  });
});
