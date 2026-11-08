#!/usr/bin/env node
// Writes docs/cli/protocol-reference.md from crates/her-protocol/schema.json: the methods and
// notifications a client can rely on, with their tier. `--check` fails when the file is out of date.

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const schema = JSON.parse(fs.readFileSync(path.join(root, "crates/her-protocol/schema.json"), "utf8"));
const target = path.join(root, "docs/cli/protocol-reference.md");

const link = (name) => (name ? `\`${name}\`` : "—");
const describe = (name) => (schema.definitions[name]?.description ?? "").split("\n")[0];

const out = [];
out.push("# her protocol reference", "");
out.push(`Protocol version ${schema["x-protocol-version"]}. Generated from \`her protocol schema\` by \`tools/gen-protocol-docs.mjs\`; do not edit by hand.`, "");
out.push("**stable** methods follow the compatibility rules in [json-rpc.md](json-rpc.md). **experimental** ones may change with any version.", "");

const tiers = schema["x-stability"];
for (const tier of ["stable", "experimental"]) {
  out.push(`## ${tier[0].toUpperCase()}${tier.slice(1)} methods`, "");
  out.push("| Method | Parameters | Result | What it does |", "|---|---|---|---|");
  for (const [name, entry] of Object.entries(schema["x-methods"])) {
    if (tiers.methods[name] !== tier) continue;
    out.push(`| \`${name}\` | ${link(entry.params)} | ${link(entry.result)} | ${describe(entry.params ?? entry.result).replaceAll("|", "\\|")} |`);
  }
  out.push("");
}

for (const [title, table] of [["Client notifications", "x-client-notifications"], ["Server notifications", "x-server-notifications"]]) {
  out.push(`## ${title}`, "");
  out.push("| Notification | Parameters | Tier |", "|---|---|---|");
  for (const [name, params] of Object.entries(schema[table])) {
    if (tiers.notifications[name] === "internal") continue;
    out.push(`| \`${name}\` | ${link(params)} | ${tiers.notifications[name]} |`);
  }
  out.push("");
}

out.push("## Error codes", "", "| Code | `data.kind` |", "|---|---|");
for (const [code, kind] of Object.entries(schema["x-error-codes"])) out.push(`| ${code} | \`${kind}\` |`);
out.push("");

const text = out.join("\n");
if (process.argv.includes("--check")) {
  if (!fs.existsSync(target) || fs.readFileSync(target, "utf8") !== text) {
    console.error("docs/cli/protocol-reference.md is out of date: run node tools/gen-protocol-docs.mjs");
    process.exit(1);
  }
} else {
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.writeFileSync(target, text);
}
