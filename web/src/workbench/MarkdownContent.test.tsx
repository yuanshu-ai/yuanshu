import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import MarkdownContent from "./MarkdownContent";

describe("safe Markdown rendering", () => {
  it("renders structured text without HTML, remote images, or dangerous links", () => {
    const { container } = render(<MarkdownContent value={'## Result\n\n| A | B |\n| - | - |\n| 1 | 2 |\n\n<script>alert(1)</script>\n\n![track](https://tracker.test/a.png)\n\n[bad](javascript:alert(1))\n\n[good](https://example.test)'} />);
    expect(screen.getByRole("heading", { name: "Result" })).toBeInTheDocument();
    expect(screen.getByRole("table")).toBeInTheDocument();
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("img")).toBeNull();
    expect(screen.queryByRole("link", { name: "bad" })).toBeNull();
    expect(screen.getByRole("link", { name: "good" })).toHaveAttribute("rel", "noopener noreferrer");
  });
});
