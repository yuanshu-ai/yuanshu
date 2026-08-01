import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import type { YuanshuMessage } from "./types.generated";
import { KNOWN_CONTROL_TYPES, KNOWN_EVENT_TYPES } from "./catalog.generated";
import {
  classify,
  frameWithinLimit,
  isKnownControl,
  isKnownEvent,
  isTerminalControlResult,
  type Classification,
  type MessageKind,
} from "./compatibility";

const schemaRoot = resolve(process.cwd(), "..", "schemas", "yuanshu", "v1");
const compatibility = JSON.parse(readFileSync(resolve(schemaRoot, "compatibility.json"), "utf8")) as {
  cases: Array<{ name: string; version: string; kind: MessageKind; type: string; expected: Classification }>;
  controlResultStates: Array<{ state: "received" | "validated" | "dispatching" | "confirmed" | "rejected" | "ambiguous"; terminal: boolean }>;
};
const fixtures = JSON.parse(readFileSync(resolve(schemaRoot, "fixtures", "valid-messages.json"), "utf8")) as {
  controlBase: Record<string, unknown>;
  controls: Array<Record<string, unknown>>;
  eventBase: Record<string, unknown>;
  events: Array<Record<string, unknown>>;
  forwardCompatibleEvents: Array<Record<string, unknown>>;
};

describe("protocol v1 compatibility", () => {
  for (const testCase of compatibility.cases) {
    it(testCase.name, () => {
      expect(classify(testCase.version, testCase.kind, testCase.type)).toBe(testCase.expected);
    });
  }

  it("classifies every control result state", () => {
    for (const testCase of compatibility.controlResultStates) {
      expect(isTerminalControlResult(testCase.state)).toBe(testCase.terminal);
    }
  });

  it("keeps generated catalogs synchronized with shared fixtures", () => {
    expect(fixtures.controls.map((item) => item.type)).toEqual([...KNOWN_CONTROL_TYPES]);
    expect(fixtures.events.map((item) => item.type)).toEqual([...KNOWN_EVENT_TYPES]);
    expect(KNOWN_CONTROL_TYPES.every(isKnownControl)).toBe(true);
    expect(KNOWN_EVENT_TYPES.every(isKnownEvent)).toBe(true);
  });

  it("round-trips all shared messages through the generated envelope type", () => {
    const instantiate = (base: Record<string, unknown>, items: Array<Record<string, unknown>>) =>
      items.map((item, index) => ({ ...base, ...item, sequence: Number(base.sequence) + index } as YuanshuMessage));
    const messages = [
      ...instantiate(fixtures.controlBase, fixtures.controls),
      ...instantiate(fixtures.eventBase, fixtures.events),
      ...instantiate(fixtures.eventBase, fixtures.forwardCompatibleEvents),
    ];
    for (const message of messages) {
      expect(JSON.parse(JSON.stringify(message))).toEqual(message);
    }
  });

  it("enforces byte boundaries", () => {
    expect(frameWithinLimit("control", 256 * 1024)).toBe(true);
    expect(frameWithinLimit("control", 256 * 1024 + 1)).toBe(false);
    expect(frameWithinLimit("event", 1024 * 1024)).toBe(true);
    expect(frameWithinLimit("event", 1024 * 1024 + 1)).toBe(false);
    expect(frameWithinLimit("event", -1)).toBe(false);
  });
});
