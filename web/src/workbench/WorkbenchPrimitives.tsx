import type { ReactNode } from "react";

import type { IconName } from "./Icon";
import { Icon } from "./Icon";
import type { ResourceState } from "./session";

export function EmptyState({ icon, title, detail }: { icon: IconName; title: string; detail: string }) {
  return <div className="empty-state"><Icon name={icon} /><b>{title}</b><p>{detail}</p></div>;
}

export function ResourceMessage({ resource, compact = false, onRetry }: { resource: ResourceState; compact?: boolean; onRetry?: () => void }) {
  if (resource.state !== "error" && resource.state !== "stale") return null;
  return <div className={`resource-message ${compact ? "compact" : ""}`}><Icon name="warning" /><div><b>{errorTitle(resource.errorCode)}</b><p>{errorDetail(resource.errorCode)}</p></div>{onRetry && <button className="button secondary" type="button" onClick={onRetry}>重试</button>}</div>;
}

export function SkeletonRows({ count }: { count: number }) {
  return <div className="skeleton-rows" aria-label="正在加载">{Array.from({ length: count }, (_, index) => <span key={index} />)}</div>;
}

export function SkeletonTimeline() {
  return <div className="skeleton-timeline" aria-label="正在读取任务历史"><span /><span /><span /></div>;
}

export function CodePanel({ value, label }: { value: string; label: string }) {
  return <div className="code-panel"><button type="button" onClick={() => void navigator.clipboard?.writeText(value)} aria-label={label}><Icon name="copy" /></button><pre>{value}</pre></div>;
}

export function StatusPill({ tone, children }: { tone?: "accent" | "warning" | "danger" | "quiet"; children: ReactNode }) {
  return <span className={`status-pill ${tone ?? "quiet"}`}>{children}</span>;
}

export function connectionLabel(value: string) {
  return ({ idle: "未连接", connecting: "连接中", authenticating: "安全认证", connected: "实时连接", reconnecting: "正在重连", paused: "已暂停", closed: "已关闭", reauth_required: "需要重新配对" } as Record<string, string>)[value] ?? "未知状态";
}

export function statusLabel(value?: string) {
  return ({ running: "执行中", active: "执行中", inProgress: "执行中", completed: "已完成", failed: "失败", interrupted: "已停止", idle: "等待指令", waiting: "等待指令", waiting_approval: "等待审批", uncertain: "待确认", ambiguous: "结果不确定", unavailable: "Codex 不可用", reconnecting: "恢复中" } as Record<string, string>)[value ?? ""] ?? "待同步";
}

export function actionLabel(value: string) {
  return ({ sent: "已发送", executing: "正在执行", confirmed: "设备已确认", rejected: "已拒绝", failed: "执行失败", ambiguous: "结果不确定", unknown: "结果未知", offline: "设备离线" } as Record<string, string>)[value] ?? value;
}

export function controlTypeLabel(value: string) {
  return ({ "thread.resume": "恢复任务", "turn.start": "开始本轮执行", "turn.steer": "纠偏当前执行", "turn.interrupt": "停止当前执行", "approval.resolve": "处理审批" } as Record<string, string>)[value] ?? "任务操作";
}

export function formatTime(value?: string) {
  if (!value) return "刚刚";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "刚刚" : date.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

export function shortID(value?: string) {
  if (!value) return "未提供";
  return value.length > 14 ? `${value.slice(0, 8)}...${value.slice(-4)}` : value;
}

function errorTitle(code?: string) {
  if (!code) return "读取失败";
  if (code.includes("offline")) return "设备离线";
  if (code.includes("reauth")) return "需要重新配对";
  if (code.includes("history")) return "部分历史不可用";
  if (code.includes("unsupported")) return "当前能力不受支持";
  return "读取失败";
}

function errorDetail(code?: string) {
  if (!code) return "请检查连接后重试。";
  if (code.includes("offline")) return "设备恢复在线后可以重新读取。";
  if (code.includes("reauth")) return "当前浏览器身份已失效，请重新配对。";
  return `错误代码：${code}`;
}
