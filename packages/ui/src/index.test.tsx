import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Alert, Badge, Button, Card, Dialog, DialogContent, DialogFooter, DialogTitle, DialogTrigger, Tabs, TabsContent, TabsList, TabsTrigger } from "./index";

describe("Yuanshu shared UI", () => {
  it("renders semantic button, badge, card and alert states", () => {
    render(<Card><Button variant="primary">保存</Button><Badge variant="warning">需要注意</Badge><Alert variant="success">已连接</Alert></Card>);
    expect(screen.getByRole("button", { name: "保存" })).toHaveClass("yu-button-primary");
    expect(screen.getByText("需要注意")).toHaveClass("yu-badge-warning");
    expect(screen.getByRole("status")).toHaveClass("yu-alert-success");
  });

  it("opens a dialog and restores a usable close path", () => {
    render(<Dialog><DialogTrigger asChild><Button variant="secondary">打开</Button></DialogTrigger><DialogContent><DialogTitle>设置</DialogTitle><DialogFooter><Button variant="quiet">取消</Button></DialogFooter></DialogContent></Dialog>);
    fireEvent.click(screen.getByRole("button", { name: "打开" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("keeps tabs keyboard-friendly and exposes the active panel", () => {
    render(<Tabs defaultValue="overview"><TabsList aria-label="设置"><TabsTrigger value="overview">概览</TabsTrigger><TabsTrigger value="details">详情</TabsTrigger></TabsList><TabsContent value="overview">状态</TabsContent><TabsContent value="details">诊断</TabsContent></Tabs>);
    expect(screen.getByRole("tab", { name: "概览" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("状态")).toBeVisible();
  });
});
