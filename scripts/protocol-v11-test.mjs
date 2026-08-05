import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const schema = JSON.parse(await readFile(resolve(root, "schemas", "yuanshu", "v1.1", "yuanshu-protocol.schema.json"), "utf8"));
const ajv = new Ajv2020({ allErrors: true, strict: true, strictTypes: false });
addFormats(ajv);
const validate = ajv.compile(schema);
const controls = schema.$defs.controlType.enum;
const events = schema.$defs.eventType.enum;
const base = {
  protocolVersion: "1.1", messageId: "msg", sentAt: "2026-08-05T08:00:00Z",
  ownerId: "owner", nodeId: "node", streamId: "control-stream", sequence: 1,
  correlationId: "correlation", expiresAt: "2026-08-05T08:01:00Z",
  nonce: "AAAAAAAAAAAAAAAAAAAAAA", signer: { clientId: "client", keyId: "key" },
  signature: "A".repeat(86), payload: {},
};
const lease = { leaseId: "lease", epoch: 1 };
const control = (type, index) => {
  const value = { ...base, type, messageId: `control-${index}`, sequence: index + 1, payload: {} };
  if (["agent.read", "task.list", "task.start", "task.read", "task.resume", "run.start", "run.steer", "run.interrupt", "interaction.resolve"].includes(type)) value.agentInstanceId = "codex-default";
  if (["task.list", "task.start", "task.read", "task.resume", "run.start", "run.steer", "run.interrupt", "interaction.resolve"].includes(type)) value.workspaceId = "workspace";
  if (["task.read", "task.resume", "run.start", "run.steer", "run.interrupt", "interaction.resolve"].includes(type)) value.taskId = "task";
  if (["run.steer", "run.interrupt", "interaction.resolve"].includes(type)) value.runId = "run";
  if (type === "interaction.resolve") value.interactionId = "interaction";
  if (["workspace.list", "task.list", "notifications.list"].includes(type)) value.payload = { limit: 50 };
  if (type === "task.read") value.payload = { includeRuns: true };
  if (type === "task.start") value.payload = { input: "Inspect the project" };
  if (["run.start", "run.steer"].includes(type)) value.payload = { input: "Continue", lease };
  if (type === "run.interrupt") value.payload = { lease };
  if (type === "interaction.resolve") value.payload = { answer: "Use option A", operationDigest: "A".repeat(43), lease };
  if (type === "events.replay") value.payload = { afterSequence: 0 };
  if (type === "lease.acquire") value.payload = { force: false };
  if (["lease.renew", "lease.release"].includes(type)) value.payload = { leaseId: "lease", epoch: 1 };
  if (type === "notifications.read") value.payload = { notificationId: "notification" };
  if (type === "config.update") value.payload = { baseRevision: "revision", changes: { hostName: "Office Mac" } };
  return value;
};
const event = (type, index) => {
  const { expiresAt, nonce, signer, signature, ...eventBase } = base;
  const value = { ...eventBase, type, messageId: `event-${index}`, sequence: index + 1, streamId: "node-events-v1.1", payload: {} };
  if (type === "agent.snapshot") value.payload = { agents: [{ id: "codex-default", adapterType: "codex", displayName: "Codex", runtimeMode: "managed", status: "ready", capabilities: [{ id: "task.start", level: "full" }] }] };
  if (type === "task.snapshot") value.payload = { task: { id: "task", agentInstanceId: "codex-default", workspaceId: "workspace", status: "idle" }, runs: [], activities: [], interactions: [] };
  if (type === "interaction.requested") value.payload = { id: "interaction", kind: "question", status: "pending", operationDigest: "A".repeat(43), expiresAt: "2026-08-05T08:05:00Z" };
  if (type === "control.result") value.payload = { status: "confirmed" };
  return value;
};

let failures = 0;
for (const [index, type] of controls.entries()) {
  const value = control(type, index);
  if (!validate(value)) { process.stderr.write(`invalid 1.1 control ${type}: ${ajv.errorsText(validate.errors)}\n`); failures += 1; }
}
for (const [index, type] of events.entries()) {
  const value = event(type, index);
  if (!validate(value)) { process.stderr.write(`invalid 1.1 event ${type}: ${ajv.errorsText(validate.errors)}\n`); failures += 1; }
}
const leakedNative = { ...control("task.read", 999), threadId: "native-session" };
if (validate(leakedNative)) { process.stderr.write("1.1 accepted a native threadId field\n"); failures += 1; }
const missingAgent = control("task.start", 1000);
delete missingAgent.agentInstanceId;
if (validate(missingAgent)) { process.stderr.write("1.1 accepted task.start without Agent instance\n"); failures += 1; }
if (failures) process.exitCode = 1;
else process.stdout.write(`validated Protocol 1.1 ${controls.length} controls and ${events.length} events\n`);
