/**
 * Yuanshu Protocol v1.1 Agent-neutral control and event envelope.
 */
export interface YuanshuMessage {
    readonly activityId?:      string;
    readonly agentInstanceId?: string;
    readonly correlationId:    string;
    readonly expiresAt?:       string;
    readonly interactionId?:   string;
    readonly messageId:        string;
    readonly nodeId:           string;
    readonly nonce?:           string;
    readonly ownerId:          string;
    readonly payload:          { [key: string]: unknown };
    readonly protocolVersion:  ProtocolVersion;
    readonly runId?:           string;
    readonly sentAt:           string;
    readonly sequence:         number;
    readonly signature?:       string;
    readonly signer?:          Signer;
    readonly streamId:         string;
    readonly taskId?:          string;
    readonly type:             Type;
    readonly workspaceId?:     string;
}

export type ProtocolVersion = "1.1";

export interface Signer {
    readonly clientId: string;
    readonly keyId:    string;
}

export type Type = "device.sync" | "workspace.list" | "agent.list" | "agent.read" | "task.list" | "task.read" | "task.start" | "task.resume" | "run.start" | "run.steer" | "run.interrupt" | "interaction.resolve" | "events.replay" | "snapshot.request" | "lease.acquire" | "lease.renew" | "lease.release" | "lease.status" | "notifications.list" | "notifications.read" | "config.read" | "config.update" | "device.status" | "runtime.status" | "agent.snapshot" | "agent.status" | "task.snapshot" | "task.started" | "task.updated" | "run.started" | "run.completed" | "run.failed" | "run.interrupted" | "message.delta" | "message.completed" | "reasoning.summary.delta" | "reasoning.summary.completed" | "plan.updated" | "activity.started" | "activity.updated" | "activity.completed" | "interaction.requested" | "interaction.resolved" | "file.changed" | "diff.updated" | "control.result" | "lease.changed" | "history.gap" | "error";
