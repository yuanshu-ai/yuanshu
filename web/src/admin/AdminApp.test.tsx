import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { AdminApp } from "./AdminApp";

describe("AdminApp", () => {
  it("does not enter management without a paired browser identity", async () => {
    render(<AdminApp />);
    expect(await screen.findByRole("heading", { name: "无法进入 Server 管理" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "打开配对页面" })).toHaveAttribute("href", "/pair");
  });
});
