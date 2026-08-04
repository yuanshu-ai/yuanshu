import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import process from "node:process";

const root = resolve(import.meta.dirname, "..");
const mappings = [
  ["assets/brand/yuanshu-mark-on-dark-compact.svg", "web/public/brand/yuanshu-mark.svg"],
  ["assets/brand/yuanshu-app-icon.svg", "web/public/brand/yuanshu-app-icon.svg"],
  ["assets/brand/yuanshu-app-icon-512.png", "web/public/brand/yuanshu-app-icon-512.png"],
  ["assets/brand/yuanshu-mark-on-dark-compact.svg", "node-web/public/brand/yuanshu-mark.svg"],
  ["assets/brand/yuanshu-app-icon.svg", "node-web/public/brand/yuanshu-app-icon.svg"],
  ["assets/brand/yuanshu-mark-primary-compact.svg", "internal/server/pairing-web/logo.svg"],
  ["assets/brand/yuanshu-menubar-template-18@2x.png", "internal/node/brand/yuanshu-menubar-template-18@2x.png"],
  ["assets/brand/yuanshu-tray.ico", "internal/node/brand/yuanshu-tray.ico"],
  ["assets/brand/yuanshu-mark-primary-light-preview.png", ".github/assets/readme/yuanshu-logo.png"],
];

const mode = process.argv[2] ?? "check";
if (mode !== "sync" && mode !== "check") {
  console.error("Usage: node scripts/brand-assets.mjs <sync|check>");
  process.exit(2);
}

const drift = [];
for (const [sourceName, targetName] of mappings) {
  const source = await readFile(resolve(root, sourceName));
  const target = resolve(root, targetName);
  if (mode === "sync") {
    await mkdir(dirname(target), { recursive: true });
    await writeFile(target, source);
    continue;
  }
  try {
    const current = await readFile(target);
    if (!source.equals(current)) drift.push(targetName);
  } catch {
    drift.push(targetName);
  }
}

if (drift.length) {
  console.error(`Brand assets are missing or stale:\n${drift.map((item) => `- ${item}`).join("\n")}`);
  console.error("Run: pnpm brand:sync");
  process.exit(1);
}

console.log(mode === "sync" ? `Synchronized ${mappings.length} brand assets.` : `Verified ${mappings.length} brand assets.`);
