import { useEffect, useMemo, useState, type FormEvent } from "react";

import type { AgentProjection, NodeProjection, WorkspaceProjection } from "../state/projection";
import { Dialog } from "./Dialog";
import { Icon } from "./Icon";
import { canStartTask, taskStartUnavailableReason } from "./selectors";
import type { WorkbenchSession } from "./session";
import { StatusPill } from "./WorkbenchPrimitives";

type Step = "target" | "compose" | "review";

export interface NewTaskTarget {
  nodeId: string;
  agentInstanceId?: string;
  workspaceId: string;
}

export function NewTaskFlow({ session, connectionState, nodes, agents, workspaces, initialTarget, onClose, onConfirmed, onDraftChange }: {
  session: WorkbenchSession;
  connectionState: string;
  nodes: NodeProjection[];
  agents: AgentProjection[];
  workspaces: WorkspaceProjection[];
  initialTarget?: NewTaskTarget;
  onClose: () => void;
  onConfirmed: (messageId: string) => void;
  onDraftChange?: (dirty: boolean) => void;
}) {
  const initial = validInitialTarget(initialTarget, nodes, agents, workspaces);
  const candidates = useMemo(() => taskTargets(nodes, agents, workspaces), [agents, nodes, workspaces]);
  const automaticTarget = initial ?? (candidates.length === 1 ? candidates[0] : undefined);
  const [step, setStep] = useState<Step>(automaticTarget ? "compose" : "target");
  const [targetWasAutomatic, setTargetWasAutomatic] = useState(Boolean(automaticTarget));
  const [nodeId, setNodeId] = useState(automaticTarget?.nodeId ?? "");
  const [agentInstanceId, setAgentInstanceId] = useState(automaticTarget?.agentInstanceId ?? "");
  const [workspaceId, setWorkspaceId] = useState(automaticTarget?.workspaceId ?? "");
  const [input, setInput] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const node = nodes.find((candidate) => candidate.nodeId === nodeId);
  const agent = agents.find((candidate) => candidate.nodeId === nodeId && candidate.agentInstanceId === agentInstanceId);
  const workspace = workspaces.find((candidate) => candidate.nodeId === nodeId && candidate.workspaceId === workspaceId && (!candidate.agentInstanceIds?.length || candidate.agentInstanceIds.includes(agentInstanceId)));
  const availableAgents = useMemo(() => agents.filter((candidate) => candidate.nodeId === nodeId && candidate.runtimeMode !== "detected-only"), [agents, nodeId]);
  const availableWorkspaces = useMemo(() => workspaces.filter((candidate) => candidate.nodeId === nodeId && (!candidate.agentInstanceIds?.length || candidate.agentInstanceIds.includes(agentInstanceId))), [agentInstanceId, nodeId, workspaces]);
  const targetReady = canStartTask(node) && canCreateTask(agent) && Boolean(workspace);
  const startReady = targetReady && input.trim().length > 0 && connectionState === "connected";
  const availabilityMessage = connectionState !== "connected" ? "实时连接尚未恢复" : taskStartUnavailableReason(node) || agentUnavailableReason(agent) || (!workspace ? "所选工作区已不可用" : "");

  useEffect(() => {
    return () => onDraftChange?.(false);
  }, [onDraftChange]);

  useEffect(() => {
    if (initialTarget || targetWasAutomatic || step !== "target" || nodeId || agentInstanceId || workspaceId || candidates.length !== 1) return;
    const target = candidates[0];
    if (!target.agentInstanceId) return;
    setNodeId(target.nodeId);
    setAgentInstanceId(target.agentInstanceId);
    setWorkspaceId(target.workspaceId);
    setTargetWasAutomatic(true);
    setStep("compose");
  }, [agentInstanceId, candidates, initialTarget, nodeId, step, targetWasAutomatic, workspaceId]);

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
      const handle = await session.startThread(node.nodeId, workspace.workspaceId, input.trim(), agent!.agentInstanceId);
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

  const canContinue = step === "target" ? targetReady : step === "compose" ? targetReady && Boolean(input.trim()) : startReady;
  return <Dialog className="new-task-dialog" title="开始新任务" onClose={requestClose} actions={<FlowActions step={step} direct={targetWasAutomatic} busy={busy} canContinue={canContinue} onBack={() => setStep(step === "review" ? "compose" : "target")} onNext={() => setStep(step === "target" ? "compose" : "review")} onClose={requestClose} onSubmit={() => void submit()} />}>
    <div className="new-task-flow">
      {targetWasAutomatic ? <ol className="new-task-steps" aria-label="新任务步骤"><li className="done">目标已选择</li><li className={step === "compose" ? "active" : ""}>输入任务</li></ol> : <ol className="new-task-steps" aria-label="新任务步骤"><li className={step === "target" ? "active" : "done"}>1 选择位置</li><li className={step === "compose" ? "active" : step === "review" ? "done" : ""}>2 输入任务</li><li className={step === "review" ? "active" : ""}>3 确认执行</li></ol>}
      {step === "target" && <TargetStep nodes={nodes} agents={availableAgents} workspaces={availableWorkspaces} nodeId={nodeId} agentInstanceId={agentInstanceId} workspaceId={workspaceId} onNode={(value) => { setNodeId(value); setAgentInstanceId(""); setWorkspaceId(""); setMessage(""); }} onAgent={(value) => { setAgentInstanceId(value); setWorkspaceId(""); setMessage(""); }} onWorkspace={(value) => { setWorkspaceId(value); setMessage(""); }} />}
      {step === "compose" && <form className="new-task-compose" onSubmit={(event) => { event.preventDefault(); if (!targetReady || !input.trim()) return; if (targetWasAutomatic) void submit(event); else setStep("review"); }}><TargetSummary node={node} agent={agent} workspace={workspace} compact /><label htmlFor="new-task-input">你希望 {agent?.displayName ?? "Agent"} 完成什么？</label><textarea id="new-task-input" autoFocus value={input} onChange={(event) => { setInput(event.target.value); onDraftChange?.(event.target.value.trim().length > 0); }} placeholder="描述目标、约束和完成标准" rows={8} disabled={busy} /></form>}
      {step === "review" && <div className="new-task-review"><TargetSummary node={node} agent={agent} workspace={workspace} /><section><span>任务要求</span><p>{input.trim()}</p></section><div className="new-task-notice"><Icon name="lock" /><p>浏览器只发送已登记的设备、Agent 和工作区标识；实际路径、Provider 配置与凭据仍留在设备。</p></div></div>}
      {!startReady && step !== "target" && availabilityMessage && <div className="operation-message" role="status">{availabilityMessage}</div>}
      {message && <div className="operation-message" role="alert">{friendlyStartError(message)}</div>}
    </div>
  </Dialog>;
}

function TargetStep({ nodes, agents, workspaces, nodeId, agentInstanceId, workspaceId, onNode, onAgent, onWorkspace }: {
  nodes: NodeProjection[];
  agents: AgentProjection[];
  workspaces: WorkspaceProjection[];
  nodeId: string;
  agentInstanceId: string;
  workspaceId: string;
  onNode: (value: string) => void;
  onAgent: (value: string) => void;
  onWorkspace: (value: string) => void;
}) {
  return <div className="new-task-target"><fieldset><legend>1. 选择设备</legend><div className="target-options">{nodes.map((node) => {
    const available = canStartTask(node);
    return <button type="button" className={node.nodeId === nodeId ? "selected" : ""} disabled={!available} onClick={() => onNode(node.nodeId)} key={node.nodeId}><span className={`node-monogram ${available ? "online" : "offline"}`}>{(node.name ?? "设").slice(0, 1).toUpperCase()}</span><span><b>{node.name ?? "未命名设备"}</b><small>{available ? "在线 · Codex 可用" : node.online ? "Codex 暂不可用" : "设备离线"}</small></span>{node.nodeId === nodeId && <Icon name="check" />}</button>;
  })}</div></fieldset>{nodeId && <fieldset><legend>2. 选择 Agent</legend>{agents.length ? <div className="target-options">{agents.map((agent) => <button type="button" className={agent.agentInstanceId === agentInstanceId ? "selected" : ""} disabled={!canCreateTask(agent)} onClick={() => onAgent(agent.agentInstanceId)} key={agent.key}><span className="node-monogram agent">{agent.displayName.slice(0, 1).toUpperCase()}</span><span><b>{agent.displayName}</b><small>{agent.runtimeMode} · {canCreateTask(agent) ? "可创建任务" : agentUnavailableReason(agent)}</small></span>{agent.agentInstanceId === agentInstanceId && <Icon name="check" />}</button>)}</div> : <p className="target-empty">该设备还没有可控 Agent。</p>}</fieldset>}{agentInstanceId && <fieldset><legend>3. 选择工作区</legend>{workspaces.length ? <div className="target-options workspaces">{workspaces.map((workspace) => <button type="button" className={workspace.workspaceId === workspaceId ? "selected" : ""} onClick={() => onWorkspace(workspace.workspaceId)} key={workspace.key}><Icon name="folder" /><span><b>{workspace.name ?? "未命名工作区"}</b><small>{permissionLabel(workspace.permissionProfile)} · {networkLabel(workspace.allowNetwork)}</small></span>{workspace.workspaceId === workspaceId && <Icon name="check" />}</button>)}</div> : <p className="target-empty">这个 Agent 尚未授权任何工作区。</p>}</fieldset>}</div>;
}

function TargetSummary({ node, agent, workspace, compact = false }: { node?: NodeProjection; agent?: AgentProjection; workspace?: WorkspaceProjection; compact?: boolean }) {
  return <section className={`target-summary ${compact ? "compact" : ""}`} aria-label="执行目标"><div><span>设备</span><b>{node?.name ?? "未选择"}</b><StatusPill tone={canStartTask(node) ? "accent" : "warning"}>{canStartTask(node) ? "在线" : "不可用"}</StatusPill></div><div><span>Agent</span><b>{agent?.displayName ?? "未选择"}</b></div><div><span>工作区</span><b>{workspace?.name ?? "未选择"}</b></div><div><span>权限</span><b>{permissionLabel(workspace?.permissionProfile)}</b></div><div><span>网络</span><b>{networkLabel(workspace?.allowNetwork)}</b></div></section>;
}

function FlowActions({ step, direct, busy, canContinue, onBack, onNext, onClose, onSubmit }: { step: Step; direct: boolean; busy: boolean; canContinue: boolean; onBack: () => void; onNext: () => void; onClose: () => void; onSubmit: () => void }) {
  const submitStep = step === "review" || (step === "compose" && direct);
  return <><button className="button quiet" type="button" disabled={busy} onClick={onClose}>取消</button>{step !== "target" && !(direct && step === "compose") && <button className="button secondary" type="button" disabled={busy} onClick={onBack}>上一步</button>}{submitStep ? <button className="button primary" type="button" disabled={busy || !canContinue} onClick={onSubmit}>{busy ? "正在启动" : "确认并启动"}</button> : <button className="button primary" type="button" disabled={!canContinue} onClick={onNext}>下一步</button>}</>;
}

function validInitialTarget(target: NewTaskTarget | undefined, nodes: NodeProjection[], agents: AgentProjection[], workspaces: WorkspaceProjection[]): NewTaskTarget | undefined {
  if (!target) return undefined;
  const node = nodes.find((candidate) => candidate.nodeId === target.nodeId);
  const workspace = workspaces.find((candidate) => candidate.nodeId === target.nodeId && candidate.workspaceId === target.workspaceId);
	const agentInstanceId = target.agentInstanceId ?? workspace?.defaultAgentInstanceId;
	const agent = agents.find((candidate) => candidate.nodeId === target.nodeId && candidate.agentInstanceId === agentInstanceId);
  return canStartTask(node) && canCreateTask(agent) && workspace && agentInstanceId ? { ...target, agentInstanceId } : undefined;
}

function taskTargets(nodes: NodeProjection[], agents: AgentProjection[], workspaces: WorkspaceProjection[]): NewTaskTarget[] {
  const result: NewTaskTarget[] = [];
  for (const node of nodes) {
    if (!canStartTask(node)) continue;
    for (const agent of agents.filter((candidate) => candidate.nodeId === node.nodeId && canCreateTask(candidate))) {
      for (const workspace of workspaces) {
        if (workspace.nodeId !== node.nodeId || workspace.agentInstanceIds?.length && !workspace.agentInstanceIds.includes(agent.agentInstanceId)) continue;
        result.push({ nodeId: node.nodeId, agentInstanceId: agent.agentInstanceId, workspaceId: workspace.workspaceId });
      }
    }
  }
  return result;
}

function canCreateTask(agent?: AgentProjection): boolean {
  return agent?.runtimeMode === "managed" && agent.capabilities.some((capability) => capability.id === "task.start" && capability.level === "full");
}

function agentUnavailableReason(agent?: AgentProjection): string {
  if (!agent) return "请选择 Agent";
  if (canCreateTask(agent)) return "";
  if (agent.runtimeMode === "detected-only") return "仅检测到，当前任务不能安全接入";
  if (agent.runtimeMode !== "managed") return "当前运行模式只支持查看";
  if (agent.status !== "ready") return "Runtime 暂不可用";
  return "该 Agent 未声明创建任务能力";
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
