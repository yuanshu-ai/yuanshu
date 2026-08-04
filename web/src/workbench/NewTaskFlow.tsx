import { useEffect, useMemo, useState, type FormEvent } from "react";

import type { NodeProjection, WorkspaceProjection } from "../state/projection";
import { Dialog } from "./Dialog";
import { Icon } from "./Icon";
import { canStartTask, taskStartUnavailableReason } from "./selectors";
import type { WorkbenchSession } from "./session";
import { StatusPill } from "./WorkbenchPrimitives";

type Step = "target" | "compose" | "review";

export interface NewTaskTarget {
  nodeId: string;
  workspaceId: string;
}

export function NewTaskFlow({ session, connectionState, nodes, workspaces, initialTarget, onClose, onConfirmed, onDraftChange }: {
  session: WorkbenchSession;
  connectionState: string;
  nodes: NodeProjection[];
  workspaces: WorkspaceProjection[];
  initialTarget?: NewTaskTarget;
  onClose: () => void;
  onConfirmed: (messageId: string) => void;
  onDraftChange?: (dirty: boolean) => void;
}) {
  const initial = validInitialTarget(initialTarget, nodes, workspaces);
  const [step, setStep] = useState<Step>(initial ? "compose" : "target");
  const [nodeId, setNodeId] = useState(initial?.nodeId ?? "");
  const [workspaceId, setWorkspaceId] = useState(initial?.workspaceId ?? "");
  const [input, setInput] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const node = nodes.find((candidate) => candidate.nodeId === nodeId);
  const workspace = workspaces.find((candidate) => candidate.nodeId === nodeId && candidate.workspaceId === workspaceId);
  const availableWorkspaces = useMemo(() => workspaces.filter((candidate) => candidate.nodeId === nodeId), [nodeId, workspaces]);
  const targetReady = canStartTask(node) && Boolean(workspace);
  const startReady = targetReady && input.trim().length > 0 && connectionState === "connected";
  const availabilityMessage = connectionState !== "connected" ? "实时连接尚未恢复" : taskStartUnavailableReason(node) || (!workspace ? "所选工作区已不可用" : "");

  useEffect(() => {
    return () => onDraftChange?.(false);
  }, [onDraftChange]);

  const requestClose = () => {
    if (busy) return;
    onClose();
  };

  const submit = async (event?: FormEvent) => {
    event?.preventDefault();
    if (!node || !workspace || !startReady || busy) {
      setMessage(availabilityMessage || "请完成任务信息后再启动");
      return;
    }
    setBusy(true);
    setMessage("");
    try {
      const handle = await session.startThread(node.nodeId, workspace.workspaceId, input.trim());
      const result = await handle.result;
      const status = typeof result.payload.status === "string" ? result.payload.status : "rejected";
      if (status !== "confirmed") throw new Error(typeof result.payload.errorCode === "string" ? result.payload.errorCode : status);
      setInput("");
      onConfirmed(handle.messageId);
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "创建结果不确定，请先获取最新状态");
    } finally {
      setBusy(false);
    }
  };

  return <Dialog className="new-task-dialog" title="开始新任务" onClose={requestClose} actions={<FlowActions step={step} busy={busy} canContinue={step === "target" ? targetReady : step === "compose" ? targetReady && Boolean(input.trim()) : startReady} onBack={() => setStep(step === "review" ? "compose" : "target")} onNext={() => setStep(step === "target" ? "compose" : "review")} onClose={requestClose} onSubmit={() => void submit()} />}>
    <div className="new-task-flow">
      <ol className="new-task-steps" aria-label="新任务步骤"><li className={step === "target" ? "active" : "done"}>1 选择位置</li><li className={step === "compose" ? "active" : step === "review" ? "done" : ""}>2 输入任务</li><li className={step === "review" ? "active" : ""}>3 确认执行</li></ol>
      {step === "target" && <TargetStep nodes={nodes} workspaces={availableWorkspaces} nodeId={nodeId} workspaceId={workspaceId} onNode={(value) => { setNodeId(value); setWorkspaceId(""); setMessage(""); }} onWorkspace={(value) => { setWorkspaceId(value); setMessage(""); }} />}
      {step === "compose" && <form className="new-task-compose" onSubmit={(event) => { event.preventDefault(); if (targetReady && input.trim()) setStep("review"); }}><TargetSummary node={node} workspace={workspace} compact /><label htmlFor="new-task-input">你希望 Codex 完成什么？</label><textarea id="new-task-input" autoFocus value={input} onChange={(event) => { setInput(event.target.value); onDraftChange?.(event.target.value.trim().length > 0); }} placeholder="描述目标、约束和完成标准" rows={8} disabled={busy} /></form>}
      {step === "review" && <div className="new-task-review"><TargetSummary node={node} workspace={workspace} /><section><span>任务要求</span><p>{input.trim()}</p></section><div className="new-task-notice"><Icon name="lock" /><p>浏览器只发送已登记的设备和工作区标识；实际路径与权限仍由设备本地校验。</p></div></div>}
      {!startReady && step !== "target" && availabilityMessage && <div className="operation-message" role="status">{availabilityMessage}</div>}
      {message && <div className="operation-message" role="alert">{friendlyStartError(message)}</div>}
    </div>
  </Dialog>;
}

function TargetStep({ nodes, workspaces, nodeId, workspaceId, onNode, onWorkspace }: {
  nodes: NodeProjection[];
  workspaces: WorkspaceProjection[];
  nodeId: string;
  workspaceId: string;
  onNode: (value: string) => void;
  onWorkspace: (value: string) => void;
}) {
  return <div className="new-task-target"><fieldset><legend>选择设备</legend><div className="target-options">{nodes.map((node) => {
    const available = canStartTask(node);
    return <button type="button" className={node.nodeId === nodeId ? "selected" : ""} disabled={!available} onClick={() => onNode(node.nodeId)} key={node.nodeId}><span className={`node-monogram ${available ? "online" : "offline"}`}>{(node.name ?? "设").slice(0, 1).toUpperCase()}</span><span><b>{node.name ?? "未命名设备"}</b><small>{available ? "在线 · Codex 可用" : node.online ? "Codex 暂不可用" : "设备离线"}</small></span>{node.nodeId === nodeId && <Icon name="check" />}</button>;
  })}</div></fieldset>{nodeId && <fieldset><legend>选择工作区</legend>{workspaces.length ? <div className="target-options workspaces">{workspaces.map((workspace) => <button type="button" className={workspace.workspaceId === workspaceId ? "selected" : ""} onClick={() => onWorkspace(workspace.workspaceId)} key={workspace.key}><Icon name="folder" /><span><b>{workspace.name ?? "未命名工作区"}</b><small>{permissionLabel(workspace.permissionProfile)} · {networkLabel(workspace.allowNetwork)}</small></span>{workspace.workspaceId === workspaceId && <Icon name="check" />}</button>)}</div> : <p className="target-empty">这台设备还没有可用于远程任务的工作区。</p>}</fieldset>}</div>;
}

function TargetSummary({ node, workspace, compact = false }: { node?: NodeProjection; workspace?: WorkspaceProjection; compact?: boolean }) {
  return <section className={`target-summary ${compact ? "compact" : ""}`} aria-label="执行目标"><div><span>设备</span><b>{node?.name ?? "未选择"}</b><StatusPill tone={canStartTask(node) ? "accent" : "warning"}>{canStartTask(node) ? "在线" : "不可用"}</StatusPill></div><div><span>工作区</span><b>{workspace?.name ?? "未选择"}</b></div><div><span>权限</span><b>{permissionLabel(workspace?.permissionProfile)}</b></div><div><span>网络</span><b>{networkLabel(workspace?.allowNetwork)}</b></div></section>;
}

function FlowActions({ step, busy, canContinue, onBack, onNext, onClose, onSubmit }: { step: Step; busy: boolean; canContinue: boolean; onBack: () => void; onNext: () => void; onClose: () => void; onSubmit: () => void }) {
  return <><button className="button quiet" type="button" disabled={busy} onClick={onClose}>取消</button>{step !== "target" && <button className="button secondary" type="button" disabled={busy} onClick={onBack}>上一步</button>}{step === "review" ? <button className="button primary" type="button" disabled={busy || !canContinue} onClick={onSubmit}>{busy ? "正在启动" : "确认并启动"}</button> : <button className="button primary" type="button" disabled={!canContinue} onClick={onNext}>下一步</button>}</>;
}

function validInitialTarget(target: NewTaskTarget | undefined, nodes: NodeProjection[], workspaces: WorkspaceProjection[]): NewTaskTarget | undefined {
  if (!target) return undefined;
  const node = nodes.find((candidate) => candidate.nodeId === target.nodeId);
  const workspace = workspaces.find((candidate) => candidate.nodeId === target.nodeId && candidate.workspaceId === target.workspaceId);
  return canStartTask(node) && workspace ? target : undefined;
}

function permissionLabel(value?: string): string {
  return value === "workspace-write" || value === "workspaceWrite" ? "可修改工作区文件" : "只读";
}

function networkLabel(value?: boolean): string {
  return value === true ? "允许访问网络" : value === false ? "网络关闭" : "由设备本地策略控制";
}

function friendlyStartError(value: string): string {
  if (/ambiguous/i.test(value)) return "创建结果不确定。请获取最新状态，避免重复启动任务。";
  if (/runtime_unavailable/i.test(value)) return "Codex 当前不可用，请在设备恢复后重试。";
  if (/forbidden|unauthorized/i.test(value)) return "当前浏览器无权在这个目标启动任务。";
  return value;
}
