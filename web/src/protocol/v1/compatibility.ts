import {
  CONTROL_FRAME_MAX_BYTES,
  CURRENT_VERSION,
  EVENT_FRAME_MAX_BYTES,
  KNOWN_CONTROL_TYPES,
  KNOWN_EVENT_TYPES,
  type ControlResultStatus,
} from "./catalog.generated";

export type MessageKind = "control" | "event";
export type Classification =
  | "known_control"
  | "known_event"
  | "unknown_event"
  | "incompatible_version"
  | "unsupported_control";

const controls = new Set<string>(KNOWN_CONTROL_TYPES);
const events = new Set<string>(KNOWN_EVENT_TYPES);

export function classify(version: string, kind: MessageKind, messageType: string): Classification {
  const parsed = /^(\d+)\.(\d+)$/.exec(version);
  if (!parsed || Number(parsed[1]) !== 1) return "incompatible_version";

  if (kind === "control") {
    if (version !== CURRENT_VERSION || !controls.has(messageType)) return "unsupported_control";
    return "known_control";
  }
  return events.has(messageType) ? "known_event" : "unknown_event";
}

export function isKnownControl(messageType: string): boolean {
  return controls.has(messageType);
}

export function isKnownEvent(messageType: string): boolean {
  return events.has(messageType);
}

export function isTerminalControlResult(status: ControlResultStatus): boolean {
  return status === "confirmed" || status === "rejected" || status === "ambiguous";
}

export function frameWithinLimit(kind: MessageKind, size: number): boolean {
  if (!Number.isSafeInteger(size) || size < 0) return false;
  return size <= (kind === "control" ? CONTROL_FRAME_MAX_BYTES : EVENT_FRAME_MAX_BYTES);
}
