import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const fixtureRoot = resolve(root, "schemas", "yuanshu", "v1", "fixtures");
const load = async (path) => JSON.parse(await readFile(path, "utf8"));
const schema = await load(resolve(root, "schemas", "yuanshu", "v1", "yuanshu-protocol.schema.json"));
const valid = await load(resolve(fixtureRoot, "valid-messages.json"));
const invalid = await load(resolve(fixtureRoot, "invalid-cases.json"));
const signing = await load(resolve(fixtureRoot, "signing-vectors.json"));

const ajv = new Ajv2020({ allErrors: true, strict: true, strictTypes: false });
addFormats(ajv);
const validate = ajv.compile(schema);

const instantiate = (base, item, index) => ({
  ...base,
  ...item,
  messageId: `${base.messageId}_${index}`,
  sequence: base.sequence + index,
  payload: { ...item.payload },
});

const controls = valid.controls.map((item, index) => instantiate(valid.controlBase, item, index));
const events = valid.events.map((item, index) => instantiate(valid.eventBase, item, index));
const forwardEvents = valid.forwardCompatibleEvents.map((item, index) => instantiate(valid.eventBase, item, events.length + index));
const byType = {
  control: new Map(controls.map((message) => [message.type, message])),
  event: new Map(events.map((message) => [message.type, message])),
};

let failures = 0;
for (const message of [...controls, ...events, ...forwardEvents]) {
  if (!validate(message)) {
    process.stderr.write(`expected valid ${message.type}: ${ajv.errorsText(validate.errors)}\n`);
    failures += 1;
  }
}

for (const message of [signing.control.message, signing.approval.message]) {
  if (!validate(message)) {
    process.stderr.write(`expected valid signing vector ${message.type}: ${ajv.errorsText(validate.errors)}\n`);
    failures += 1;
  }
}

for (const testCase of invalid.cases) {
  const source = byType[testCase.source].get(testCase.type);
  const message = structuredClone(source);
  if (testCase.remove) delete message[testCase.remove];
  Object.assign(message, testCase.set ?? {});
  Object.assign(message.payload, testCase.payloadSet ?? {});
  if (validate(message)) {
    process.stderr.write(`expected invalid: ${testCase.name}\n`);
    failures += 1;
  }
}

const oversizedControl = structuredClone(byType.control.get("turn.start"));
oversizedControl.payload.input = "x".repeat(262144);
if (Buffer.byteLength(JSON.stringify(oversizedControl), "utf8") <= 262144) {
  process.stderr.write("control frame size fixture did not exceed 256 KiB\n");
  failures += 1;
}
const oversizedEvent = structuredClone(byType.event.get("agent.message.delta"));
oversizedEvent.payload.text = "x".repeat(1048576);
if (Buffer.byteLength(JSON.stringify(oversizedEvent), "utf8") <= 1048576) {
  process.stderr.write("event frame size fixture did not exceed 1 MiB\n");
  failures += 1;
}

if (failures > 0) process.exitCode = 1;
else process.stdout.write(`validated ${controls.length} controls, ${events.length} events, ${forwardEvents.length} forward-compatible events, 2 signing vectors, and ${invalid.cases.length} invalid cases\n`);
