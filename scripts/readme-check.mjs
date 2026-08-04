import { existsSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";

const root = process.cwd();
const readmes = ["README.md", "README.zh-CN.md"];
const sections = [
  "status",
  "capabilities",
  "quick-start",
  "deployment",
  "platforms",
  "architecture",
  "data-boundaries",
  "limitations",
  "documentation",
  "development",
  "community",
  "license",
];
const requiredFiles = [
  "CONTRIBUTING.md",
  "SECURITY.md",
  "SUPPORT.md",
  "CODE_OF_CONDUCT.md",
  ".github/ISSUE_TEMPLATE/bug_report.yml",
  ".github/ISSUE_TEMPLATE/feature_request.yml",
  ".github/ISSUE_TEMPLATE/config.yml",
  ".github/PULL_REQUEST_TEMPLATE.md",
  ".github/assets/readme/desktop-workbench.png",
  ".github/assets/readme/mobile-home.png",
  ".github/assets/readme/mobile-task-detail.png",
  "guides/README.md",
  "guides/self-hosting.md",
  "guides/configuration.md",
  "guides/node-control-center.md",
  "guides/web-workbench.md",
  "guides/server-admin.md",
  "guides/codex-compatibility.md",
];

const failures = [];
const contents = new Map(
  readmes.map((file) => [file, readFileSync(resolve(root, file), "utf8")]),
);

for (const [file, content] of contents) {
  const foundSections = [
    ...content.matchAll(/<!--\s*readme-section:\s*([a-z0-9-]+)\s*-->/g),
  ].map((match) => match[1]);
  if (JSON.stringify(foundSections) !== JSON.stringify(sections)) {
    failures.push(
      `${file}: section markers differ; expected ${sections.join(", ")}, found ${foundSections.join(", ")}`,
    );
  }

  for (const command of [
    "yuanshu server setup",
    "yuanshu server --config",
    "yuanshu node setup",
    "yuanshu node",
  ]) {
    if (!content.includes(command)) {
      failures.push(`${file}: missing Quick Start command ${command}`);
    }
  }

  for (const required of [
    "PF-052",
    "pre-alpha",
    "security/advisories/new",
    "lan-managed",
    "public-ip-acme",
  ]) {
    if (!content.toLowerCase().includes(required.toLowerCase())) {
      failures.push(`${file}: missing required status or safety text ${required}`);
    }
  }

  const sensitivePatterns = [
    { label: "macOS user path", regex: /\/Users\/[^/\s]+\// },
    { label: "Linux user path", regex: /\/home\/[^/\s]+\// },
    { label: "Windows user path", regex: /[A-Za-z]:\\Users\\[^\\\s]+\\/ },
    { label: "private key block", regex: /BEGIN [A-Z ]*PRIVATE KEY/ },
    { label: "GitHub token", regex: /\bgh[opsu]_[A-Za-z0-9]{20,}\b/ },
    { label: "OpenAI-style secret", regex: /\bsk-[A-Za-z0-9_-]{20,}\b/ },
    { label: "PoC token assignment", regex: /YUANSHU_[A-Z0-9_]*TOKEN\s*=/ },
  ];
  for (const { label, regex } of sensitivePatterns) {
    if (regex.test(content)) {
      failures.push(`${file}: contains forbidden ${label}`);
    }
  }

  const targets = [
    ...content.matchAll(/\]\(([^)]+)\)/g),
    ...content.matchAll(/\bsrc=["']([^"']+)["']/g),
  ].map((match) => match[1].trim().split(/[?#]/, 1)[0]);

  for (const target of targets) {
    if (
      !target ||
      target.startsWith("#") ||
      /^[a-z][a-z0-9+.-]*:/i.test(target)
    ) {
      continue;
    }
    const localPath = resolve(root, dirname(file), target);
    if (!existsSync(localPath)) {
      failures.push(`${file}: local link target does not exist: ${target}`);
    }
  }
}

for (const file of requiredFiles) {
  if (!existsSync(resolve(root, file))) {
    failures.push(`missing community or README asset: ${file}`);
  }
}

const linkedDocuments = [
  ...readmes,
  "CONTRIBUTING.md",
  "SECURITY.md",
  "SUPPORT.md",
  "CODE_OF_CONDUCT.md",
  ...requiredFiles.filter((file) => file.startsWith("guides/")),
];
for (const file of linkedDocuments) {
  const content = readFileSync(resolve(root, file), "utf8");
  if (content.includes("github.com/yuanshu-ai/docs")) {
    failures.push(`${file}: links to the non-public engineering docs repository`);
  }
  const targets = [...content.matchAll(/\]\(([^)]+)\)/g)].map((match) =>
    match[1].trim().split(/[?#]/, 1)[0]
  );
  for (const target of targets) {
    if (
      !target ||
      target.startsWith("#") ||
      /^[a-z][a-z0-9+.-]*:/i.test(target)
    ) {
      continue;
    }
    if (!existsSync(resolve(root, dirname(file), target))) {
      failures.push(`${file}: local link target does not exist: ${target}`);
    }
  }
}

const bugForm = readFileSync(
  resolve(root, ".github/ISSUE_TEMPLATE/bug_report.yml"),
  "utf8",
);
for (const field of ["component", "commit", "environment", "deployment", "reproduce", "safety"]) {
  if (!bugForm.includes(`id: ${field}`)) {
    failures.push(`bug report form: missing field ${field}`);
  }
}

const featureForm = readFileSync(
  resolve(root, ".github/ISSUE_TEMPLATE/feature_request.yml"),
  "utf8",
);
for (const field of ["problem", "scenario", "proposal", "security", "scope"]) {
  if (!featureForm.includes(`id: ${field}`)) {
    failures.push(`feature request form: missing field ${field}`);
  }
}

if (failures.length > 0) {
  console.error(failures.map((failure) => `- ${failure}`).join("\n"));
  process.exit(1);
}

console.log(
  `validated ${readmes.length} READMEs, ${sections.length} paired sections, local links, safety text, community files, and issue forms`,
);
