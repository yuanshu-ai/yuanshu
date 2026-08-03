import { forwardRef, lazy, Suspense, useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from "react";

import type { LeaseScope, LeaseState } from "../relay/control-client";
import { threadKey, turnKey, type ApprovalProjection, type FileChangeProjection, type ThreadItemProjection, type TurnProjection } from "../state/projection";
import { Dialog } from "./Dialog";
import { Icon } from "./Icon";
import { selectThreadApprovals } from "./selectors";
import type { ResourceState, WorkbenchSession } from "./session";
import { actionLabel, CodePanel, connectionLabel, controlTypeLabel, EmptyState, formatTime, ResourceMessage, shortID, SkeletonTimeline, statusLabel, StatusPill } from "./WorkbenchPrimitives";

const MarkdownContent = lazy(() => import("./MarkdownContent"));

type Confirmation =
  | { kind: "takeover" }
  | { kind: "approval"; approval: ApprovalProjection; decision: "accept" | "decline"; step: 1 | 2 };

export function ThreadDetail({ session, snapshotRevision, connectionState, state, resource, selectedNodeId, selectedWorkspaceId, selectedThreadId, onBack, onRead, onDraftChange }: { session: WorkbenchSession; snapshotRevision: number; connectionState: string; state: WorkbenchSession["projection"]["state"]; resource?: ResourceState; selectedNodeId: string; selectedWorkspaceId: string; selectedThreadId: string; onBack: () => void; onRead: (key: string, sequence: number) => void; onDraftChange?: (dirty: boolean) => void }) {
  const thread = selectedThreadId ? state.threads[threadKey(selectedNodeId, selectedWorkspaceId, selectedThreadId)] : undefined;
  const node = state.nodes[selectedNodeId];
  const workspace = state.workspaces[`${selectedNodeId}\u001f${selectedWorkspaceId}`];
  const turns = useMemo(() => thread ? thread.turnIds.map((turnId) => state.turns[turnKey(selectedNodeId, selectedWorkspaceId, selectedThreadId, turnId)]).filter((turn): turn is TurnProjection => Boolean(turn)) : [], [state, thread, selectedNodeId, selectedWorkspaceId, selectedThreadId, snapshotRevision]);
  const activeTurn = [...turns].reverse().find((turn) => ["running", "inProgress", "active"].includes(turn.status ?? ""));
  const approvals = selectThreadApprovals(state, selectedNodeId, selectedWorkspaceId, selectedThreadId);
  const files = Object.values(state.files).filter((file) => file.nodeId === selectedNodeId && file.workspaceId === selectedWorkspaceId && file.threadId === selectedThreadId).sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
  const scope: LeaseScope | undefined = selectedThreadId && selectedWorkspaceId && selectedNodeId ? { nodeId: selectedNodeId, workspaceId: selectedWorkspaceId, threadId: selectedThreadId } : undefined;
  const lease = scope ? session.getLease(scope) : { state: "none", epoch: 0 } as LeaseState;
  const leaseHeld = Boolean(scope && session.canMutate(scope, "turn.start"));
  const actions = Object.values(state.actions).filter((action) => action.nodeId === selectedNodeId && action.threadId === selectedThreadId).sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [confirmation, setConfirmation] = useState<Confirmation>();
  const [visibleItems, setVisibleItems] = useState(100);
  const [atBottom, setAtBottom] = useState(true);
  const [newItems, setNewItems] = useState(0);
  const timeline = useRef<HTMLDivElement>(null);
  const approvalPanel = useRef<HTMLElement>(null);
  const textarea = useRef<HTMLTextAreaElement>(null);
  const itemCount = turns.reduce((count, turn) => count + turn.items.length, 0);
  const latestSequence = thread?.latestSequence ?? 0;
  const previousUpdate = useRef({ threadId: selectedThreadId, itemCount, sequence: latestSequence });

  useEffect(() => {
    setVisibleItems(100);
    setMessage("");
    setInput("");
    setNewItems(0);
    setAtBottom(true);
    previousUpdate.current = { threadId: selectedThreadId, itemCount, sequence: latestSequence };
    requestAnimationFrame(() => scrollTimeline(timeline.current));
  }, [selectedThreadId]);

  useEffect(() => {
    const previous = previousUpdate.current;
    if (previous.threadId !== selectedThreadId) return;
    if (latestSequence <= previous.sequence && itemCount <= previous.itemCount) return;
    const added = Math.max(1, itemCount - previous.itemCount);
    previousUpdate.current = { threadId: selectedThreadId, itemCount, sequence: latestSequence };
    if (atBottom) {
      requestAnimationFrame(() => scrollTimeline(timeline.current));
      if (thread) onRead(thread.key, latestSequence);
    } else {
      setNewItems((value) => value + added);
    }
  }, [atBottom, itemCount, latestSequence, onRead, selectedThreadId, thread]);

  useEffect(() => {
    if (atBottom && thread) onRead(thread.key, latestSequence);
  }, [atBottom, latestSequence, onRead, thread]);

  useEffect(() => {
    if (!textarea.current) return;
    textarea.current.style.height = "0px";
    textarea.current.style.height = `${Math.min(180, Math.max(72, textarea.current.scrollHeight))}px`;
  }, [input]);

  useEffect(() => {
    onDraftChange?.(input.trim().length > 0);
  }, [input, onDraftChange]);

  useEffect(() => () => onDraftChange?.(false), [onDraftChange]);

  const run = async (type: "thread.resume" | "turn.start" | "turn.steer" | "turn.interrupt", payload: Record<string, unknown>, target: { threadId?: string; turnId?: string }, clearInput = false) => {
    setBusy(true);
    setMessage("");
    try {
      const result = await session.request(type, payload, { nodeId: selectedNodeId, workspaceId: selectedWorkspaceId, ...target });
      const status = typeof result.payload.status === "string" ? result.payload.status : "rejected";
      if (status !== "confirmed") throw new Error(typeof result.payload.errorCode === "string" ? result.payload.errorCode : status);
      if (clearInput) setInput("");
      setMessage(type === "turn.interrupt" ? "停止请求已确认" : "设备已确认请求");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "操作结果不确定");
    } finally {
      setBusy(false);
    }
  };

  const submit = async (event?: FormEvent) => {
    event?.preventDefault();
    const value = input.trim();
    if (!value || !selectedNodeId || !selectedWorkspaceId || busy) return;
    if (!leaseHeld) {
      setMessage("当前是只读状态，请先获取任务控制权");
      return;
    }
    if (activeTurn) await run("turn.steer", { input: value }, { threadId: selectedThreadId, turnId: activeTurn.turnId }, true);
    else await run("turn.start", { input: value }, { threadId: selectedThreadId }, true);
  };

  const changeLease = async (force: boolean) => {
    if (!scope) return;
    if (force) { setConfirmation({ kind: "takeover" }); return; }
    try {
      const next = await session.acquireLease(scope, { expectedEpoch: lease.epoch });
      setMessage(next.state === "held" ? "已获得控制权" : "任务仍由其他控制端持有");
    } catch (error) { setMessage(error instanceof Error ? error.message : "获取控制权失败"); }
  };

  const takeover = async () => {
    if (!scope) return;
    setConfirmation(undefined);
    try {
      const next = await session.acquireLease(scope, { force: true, expectedEpoch: lease.epoch });
      setMessage(next.state === "held" ? "已接管控制权" : "接管失败");
    } catch (error) { setMessage(error instanceof Error ? error.message : "接管失败"); }
  };

  const resolveApproval = async (approval: ApprovalProjection, decision: "accept" | "decline") => {
    if (!approval.operationDigest || !scope) { setMessage("审批缺少可验证摘要，已保持只读"); return; }
    setBusy(true);
    setConfirmation(undefined);
    try {
      const result = await session.request("approval.resolve", { approvalId: approval.approvalId, decision, operationDigest: approval.operationDigest }, { nodeId: selectedNodeId, workspaceId: selectedWorkspaceId, threadId: selectedThreadId, turnId: approval.turnId, itemId: approval.itemId });
      if (result.payload.status !== "confirmed") throw new Error(typeof result.payload.errorCode === "string" ? result.payload.errorCode : "approval_rejected");
      setMessage(decision === "accept" ? "批准已由设备确认" : "拒绝已由设备确认");
    } catch (error) { setMessage(error instanceof Error ? error.message : "审批结果不确定"); }
    finally { setBusy(false); }
  };

  const onComposerKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if ((event.metaKey || event.ctrlKey) && event.key === "Enter") { event.preventDefault(); void submit(); }
  };

  const revealLatest = () => {
    scrollTimeline(timeline.current, "smooth");
    setAtBottom(true);
    setNewItems(0);
    if (thread) onRead(thread.key, latestSequence);
  };

  const visible = limitedTurns(turns, visibleItems);
  const hiddenCount = Math.max(0, itemCount - visibleItems);
  const placeholder = activeTurn ? "补充要求或纠偏当前执行" : "继续这个任务";
  const taskStatus = activeTurn?.status ?? thread?.status;

  if (!selectedThreadId) {
    return <section className="thread-detail empty-detail" aria-label="任务详情"><EmptyState icon="tasks" title="选择一个任务查看详情" detail="新任务需要先明确选择设备、工作区并确认本地权限。" /></section>;
  }

  return <section className="thread-detail" aria-label="任务详情">
    <div className="thread-detail-heading"><button className="mobile-back" type="button" onClick={onBack} aria-label="返回任务列表"><Icon name="back" /></button><div className="thread-heading-copy"><p>{node?.name ?? "未命名设备"} · {workspace?.name ?? "工作区"}</p><h1>{thread?.title || thread?.preview || (selectedThreadId ? `任务 ${selectedThreadId.slice(0, 10)}` : "开始新任务")}</h1><div className="thread-context-status"><StatusPill tone={connectionState === "connected" ? "accent" : "warning"}>{connectionLabel(connectionState)}</StatusPill>{thread && <StatusPill tone={taskStatus === "failed" ? "danger" : taskStatus === "running" ? "accent" : "quiet"}>{statusLabel(taskStatus)}</StatusPill>}<span>{leaseHeld ? "当前浏览器可操作" : "当前为只读查看"}</span></div></div>{thread && <div className="thread-heading-actions"><LeaseControl lease={lease} held={leaseHeld} onAcquire={() => void changeLease(false)} onTakeover={() => void changeLease(true)} onRelease={() => scope && void session.releaseLease(scope).catch(() => undefined)} />{!activeTurn && <button className="button secondary" type="button" disabled={busy || connectionState !== "connected" || !leaseHeld} onClick={() => void run("thread.resume", {}, { threadId: selectedThreadId })}>继续执行</button>}</div>}</div>
    {thread && (thread.recovery !== "none" || thread.historyState === "partial" || thread.historyState === "unavailable") && <div className="inline-alert warning"><Icon name="warning" /><div><b>{thread.recovery === "history_gap" ? "部分历史不可用" : "历史内容不完整"}</b><p>当前视图可能只包含设备能够恢复的部分内容。</p></div></div>}
    {node && (!node.online || node.runtimeStatus === "unavailable") && <div className="inline-alert danger"><Icon name="warning" /><div><b>Codex 暂不可用</b><p>本地状态会继续保留，设备恢复连接后重新同步。</p></div></div>}
    {resource?.state === "error" && <ResourceMessage resource={resource} onRetry={() => void session.loadThread(selectedNodeId, selectedWorkspaceId, selectedThreadId, true)} />}

    <div className="thread-scroll" ref={timeline} onScroll={(event) => { const element = event.currentTarget; const bottom = element.scrollHeight - element.scrollTop - element.clientHeight < 64; setAtBottom(bottom); if (bottom) setNewItems(0); }}>
      {hiddenCount > 0 && <button className="older-button" type="button" onClick={() => setVisibleItems((value) => value + 100)}>显示更早的 {Math.min(100, hiddenCount)} 项</button>}
      {resource?.state === "loading" && !turns.length ? <SkeletonTimeline /> : visible.length ? visible.map((turn) => <TurnCard turn={turn} key={turn.key} />) : <EmptyState icon="tasks" title="任务中还没有可展示内容" detail="消息、命令、工具活动和文件变化会按顺序显示。" />}
      {approvals.length > 0 && <ApprovalPanel ref={approvalPanel} approvals={approvals} leaseHeld={leaseHeld} busy={busy} onResolve={(approval, decision) => setConfirmation({ kind: "approval", approval, decision, step: 1 })} />}
      {files.length > 0 && <FileChanges files={files} session={session} />}
      {newItems > 0 && <button className="new-items-button" type="button" onClick={revealLatest}>查看 {newItems} 条新内容</button>}
    </div>

    <div className="thread-footer">
      <div className="composer-context"><span><Icon name="node" />{node?.name ?? "未命名设备"}</span><span><Icon name="folder" />{workspace?.name ?? "工作区"}</span><em>{leaseHeld ? "可操作" : "只读"}</em></div>
      {approvals.length > 0 && <button className="mobile-approval-action" type="button" onClick={() => approvalPanel.current?.scrollIntoView({ behavior: "smooth", block: "start" })}><Icon name="warning" />处理 {approvals.length} 项待审批</button>}
      {actions[0] && <div className={`action-status ${actions[0].state}`} aria-live="polite"><span className="semantic-state" /><b>{actionLabel(actions[0].state)}</b><span>{controlTypeLabel(actions[0].type)}</span>{actions[0].errorCode && <code>{actions[0].errorCode}</code>}</div>}
      {message && <div className="operation-message" role="status">{message}</div>}
      <form className="composer" onSubmit={(event) => void submit(event)}>
        <label className="sr-only" htmlFor="task-input">任务指令</label>
        <textarea ref={textarea} id="task-input" value={input} onChange={(event) => setInput(event.target.value)} onKeyDown={onComposerKeyDown} placeholder={placeholder} disabled={busy || connectionState !== "connected" || !selectedWorkspaceId} rows={3} />
        <div className="composer-actions"><span>{activeTurn ? "纠偏当前执行" : "追加本轮执行"}<small>Cmd/Ctrl + Enter</small></span><div>{activeTurn && <button className="button danger" type="button" disabled={busy || !leaseHeld} onClick={() => void run("turn.interrupt", {}, { threadId: selectedThreadId, turnId: activeTurn.turnId })}><Icon name="stop" />停止</button>}<button className="button primary" type="submit" disabled={busy || !input.trim() || connectionState !== "connected" || !selectedWorkspaceId || !leaseHeld}>{busy ? "发送中" : !leaseHeld ? "需要控制权" : "发送"}<Icon name="send" /></button></div></div>
      </form>
    </div>

    {confirmation?.kind === "takeover" && <Dialog title="接管任务控制权" destructive onClose={() => setConfirmation(undefined)} actions={<><button className="button secondary" type="button" onClick={() => setConfirmation(undefined)}>取消</button><button className="button danger solid" type="button" onClick={() => void takeover()}>确认接管</button></>}><p>接管后，当前持有者会立即变为只读，旧控制消息将被拒绝。</p><dl><dt>持有者</dt><dd>{shortID(lease.holderClientId)}</dd><dt>剩余时间</dt><dd>{lease.expiresAt ? formatLeaseTime(lease.expiresAt) : "未知"}</dd></dl></Dialog>}
    {confirmation?.kind === "approval" && <ApprovalDialog value={confirmation} nodeName={node?.name ?? "未命名设备"} workspaceName={workspace?.name ?? "工作区"} onClose={() => setConfirmation(undefined)} onNext={() => setConfirmation({ ...confirmation, step: 2 })} onConfirm={() => void resolveApproval(confirmation.approval, confirmation.decision)} />}
  </section>;
}

function TurnCard({ turn }: { turn: TurnProjection }) {
  return <article className={`turn-card ${turn.status ?? ""}`}><header><span>本轮执行</span><b>{statusLabel(turn.status)}</b><time>{formatTime(turn.updatedAt)}</time></header><div className="turn-items">{turn.items.map((item) => <ItemCard item={item} key={item.id} />)}{!turn.items.length && <p className="turn-empty">设备尚未发送可展示内容</p>}</div></article>;
}

function ItemCard({ item }: { item: ThreadItemProjection }) {
  if (item.kind === "agent_message" || item.kind === "user_message") return <div className={`message-item ${item.kind}`}><span>{item.kind === "user_message" ? "你" : "Codex"}</span><Suspense fallback={<p className="plain-message">{item.text || "（空消息）"}</p>}><MarkdownContent value={item.text || "（空消息）"} /></Suspense></div>;
  if (item.kind === "command" || item.kind === "command_output") return <details className="activity-item" open={item.status === "running"}><summary><Icon name="terminal" /><span><b>{item.command || "命令输出"}</b><small>{item.status ?? "执行中"}{item.exitCode !== undefined ? ` / exit ${item.exitCode}` : ""}{item.truncated ? " / 已截断" : ""}</small></span></summary>{item.output && <CodePanel value={item.output} label="复制命令输出" />}</details>;
  if (item.kind === "tool") return <div className="activity-item compact"><Icon name="tool" /><span><b>{item.toolName || "工具调用"}</b><small>{item.status ?? "已记录"}</small></span></div>;
  if (item.kind === "file_change" || item.kind === "diff") return <div className="activity-item compact"><Icon name="file" /><span><b>{item.path || "文件变更"}</b><small>{item.changeType || "Diff 已更新"}{item.truncated ? " / 已截断" : ""}</small></span></div>;
  return <div className="activity-item compact error"><Icon name="warning" /><span><b>{item.errorCode || "未识别活动"}</b><small>{item.errorMessage || "Codex 返回了当前版本无法识别的历史项。"}</small></span></div>;
}

function FileChanges({ files, session }: { files: FileChangeProjection[]; session: WorkbenchSession }) {
  return <section className="file-changes"><div className="section-heading"><div><Icon name="file" /><h2>文件变化</h2></div><span>{files.length}</span></div><div>{files.map((file) => <details key={file.key} onToggle={(event) => { if (event.currentTarget.open && !file.diff) void session.loadDiff(file.nodeId, file.workspaceId, file.threadId, file.path).catch(() => undefined); }}><summary><span><b>{file.path}</b><small>{changeLabel(file.changeType)} / 版本 {file.revision}{file.truncated ? " / 已截断" : ""}</small></span><Icon name="chevron" /></summary>{file.diff ? <CodePanel value={file.diff} label="复制 Diff" /> : <p className="diff-loading">展开后从设备读取最多 64 KiB Diff</p>}{file.truncated && <p className="truncate-note">仅展示 {Math.min(file.totalBytes ?? 65_536, 65_536)} / {file.totalBytes ?? "未知"} bytes，摘要 {shortID(file.digest)}</p>}</details>)}</div></section>;
}

const ApprovalPanel = forwardRef<HTMLElement, { approvals: ApprovalProjection[]; leaseHeld: boolean; busy: boolean; onResolve: (approval: ApprovalProjection, decision: "accept" | "decline") => void }>(function ApprovalPanel({ approvals, leaseHeld, busy, onResolve }, ref) {
  return <section className="approval-panel" ref={ref}><div className="section-heading"><div><Icon name="warning" /><h2>等待审批</h2></div><span>{approvals.length}</span></div>{approvals.map((approval) => <article key={approval.key}><div><b>{approval.kind ?? "未知风险操作"}</b><p>{approval.summary ?? "Codex 正在等待审批决定"}</p><small>到期 {formatTime(approval.expiresAt)} / 操作摘要 {shortID(approval.operationDigest)}</small></div><div><button className="button secondary" type="button" disabled={!leaseHeld || busy || !approval.operationDigest} onClick={() => onResolve(approval, "decline")}>拒绝</button><button className="button warning" type="button" disabled={!leaseHeld || busy || !approval.operationDigest} onClick={() => onResolve(approval, "accept")}>批准</button></div></article>)}</section>;
});

function ApprovalDialog({ value, nodeName, workspaceName, onClose, onNext, onConfirm }: { value: Extract<Confirmation, { kind: "approval" }>; nodeName: string; workspaceName: string; onClose: () => void; onNext: () => void; onConfirm: () => void }) {
  const highRisk = isHighRisk(value.approval);
  const final = !highRisk || value.step === 2;
  return <Dialog title={final ? (value.decision === "accept" ? "确认批准操作" : "确认拒绝操作") : "检查高风险操作"} destructive={value.decision === "accept" && highRisk} onClose={onClose} actions={<><button className="button secondary" type="button" onClick={onClose}>取消</button>{final ? <button className={`button ${value.decision === "accept" ? "warning" : "primary"}`} type="button" onClick={onConfirm}>{value.decision === "accept" ? "发送批准" : "发送拒绝"}</button> : <button className="button warning" type="button" onClick={onNext}>继续确认</button>}</>}><p>{value.approval.summary ?? "Codex 正在等待审批决定"}</p><dl><dt>设备</dt><dd>{nodeName}</dd><dt>工作区</dt><dd>{workspaceName}</dd><dt>风险</dt><dd>{value.approval.risk ?? value.approval.kind ?? "未知"}</dd><dt>操作摘要</dt><dd>{shortID(value.approval.operationDigest)}</dd><dt>到期</dt><dd>{formatTime(value.approval.expiresAt)}</dd></dl>{highRisk && <div className="dialog-warning">这项操作可能执行命令或修改文件。设备会再次校验目标、操作摘要、控制权和有效期。</div>}</Dialog>;
}

function LeaseControl({ lease, held, onAcquire, onTakeover, onRelease }: { lease: LeaseState; held: boolean; onAcquire: () => void; onTakeover: () => void; onRelease: () => void }) {
  if (held) return <div className="lease-control"><span className="lease-badge held"><Icon name="check" />可操作</span><button type="button" onClick={onRelease}>释放</button></div>;
  if (lease.state === "occupied") return <div className="lease-control"><span className="lease-badge occupied"><Icon name="lock" />占用中 {lease.expiresAt ? formatLeaseTime(lease.expiresAt) : ""}</span><button type="button" onClick={onTakeover}>接管</button></div>;
  return <div className="lease-control"><span className="lease-badge"><Icon name="lock" />只读</span><button type="button" onClick={onAcquire}>获取控制权</button></div>;
}

function limitedTurns(turns: TurnProjection[], limit: number): TurnProjection[] {
  let remaining = limit;
  const result: TurnProjection[] = [];
  for (let index = turns.length - 1; index >= 0 && remaining > 0; index -= 1) {
    const turn = turns[index];
    const items = turn.items.slice(Math.max(0, turn.items.length - remaining));
    remaining -= items.length;
    result.unshift({ ...turn, items });
  }
  return result;
}

function formatLeaseTime(value: string) {
  const seconds = Math.max(0, Math.round((Date.parse(value) - Date.now()) / 1_000));
  return seconds > 0 ? `${seconds}s` : "已过期";
}

function changeLabel(value?: string) {
  return ({ created: "新增", modified: "修改", deleted: "删除", renamed: "重命名" } as Record<string, string>)[value ?? ""] ?? "变更";
}

function isHighRisk(approval: ApprovalProjection) {
  return !approval.risk || !approval.kind || /high|unknown|command|file|write|delete|shell/i.test(`${approval.risk} ${approval.kind}`);
}

function scrollTimeline(element: HTMLDivElement | null, behavior?: ScrollBehavior) {
  if (!element) return;
  if (typeof element.scrollTo === "function") element.scrollTo({ top: element.scrollHeight, behavior });
  else element.scrollTop = element.scrollHeight;
}
