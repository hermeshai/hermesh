import assert from "node:assert/strict";
import { test } from "node:test";
import type { McpServerView, SkillEntry, SkillsListResult } from "@hermesh/her-protocol";
import { brokenSkills, credentialName, credentialRef, isCommand, joinCommand, monogram, nextInLine, quoteKey, skillItems, splitCommand, statusOf, withCredential } from "./model.ts";

// The Connectors and Skills pages' reading of her's answers.

const skill = (over: Partial<SkillEntry>): SkillEntry => ({
  name: "weekly",
  description: "Weekly report.",
  path: "/home/me/.her/skills/weekly/SKILL.md",
  scope: "user",
  state: "active",
  default_enabled: true,
  model_invocable: true,
  removable: true,
  ...over,
});

const list = (skills: SkillEntry[]): SkillsListResult => ({ skills, enabled: true, warnings: [] });

test("skills: the folder, the group, on or off, and another app's folder; broken and shadowed ones apart", () => {
  const skills = list([
    skill({}),
    skill({ name: "notes", path: "/home/me/.her/skills/notes/SKILL.md", state: "disabled" }),
    skill({ name: "review", path: "/home/me/.claude/skills/review/SKILL.md", removable: false }),
    skill({ name: "skill-creator", path: "/home/me/.her/skills/.system/skill-creator/SKILL.md", scope: "system", removable: false }),
    skill({ name: "weekly", path: "/home/me/.claude/skills/weekly/SKILL.md", state: "shadowed", detail: "/home/me/.her/skills/weekly/SKILL.md", removable: false }),
    skill({ name: "", path: "/home/me/.her/skills/bad/SKILL.md", state: "invalid", detail: "no name in the header" }),
  ]);
  const items = skillItems(skills);
  assert.deepEqual(
    items.map((i) => [i.entry.name, i.dir, i.group, i.on, i.from]),
    [
      ["weekly", "/home/me/.her/skills/weekly", "user", true, null],
      ["notes", "/home/me/.her/skills/notes", "user", false, null],
      ["review", "/home/me/.claude/skills/review", "user", true, ".claude"],
      ["skill-creator", "/home/me/.her/skills/.system/skill-creator", "system", true, null],
    ],
  );
  assert.deepEqual(
    brokenSkills(skills).map((s) => s.detail),
    ["no name in the header"],
  );
  assert.equal(nextInLine(skills, items[0])?.path, "/home/me/.claude/skills/weekly/SKILL.md");
  assert.equal(nextInLine(skills, items[1]), null);
  assert.deepEqual(skillItems(null), []);
});

const server = (over: Partial<McpServerView>): McpServerView => ({
  name: "github",
  auth: "none",
  editable: true,
  enabled: true,
  layer: "user",
  status: { state: "unknown" },
  summary: "https://api.githubcopilot.com/mcp",
  transport: "http",
  ...over,
});

test("a server's state in one line: off, being checked, then her's", () => {
  assert.deepEqual(statusOf(server({ enabled: false, status: { state: "ok", tools: 3 } }), false), { kind: "off" });
  assert.deepEqual(statusOf(server({}), true), { kind: "checking" });
  assert.deepEqual(statusOf(server({ status: { state: "ok", tools: 3, checked_at: "2026-11-08T10:00:00Z", source: "cache" } }), false), { kind: "ok", tools: 3, sessions: null, checkedAt: "2026-11-08T10:00:00Z" });
  assert.deepEqual(statusOf(server({ status: { state: "ok", tools: 2, sessions: 4, source: "session" } }), false), { kind: "ok", tools: 2, sessions: 4, checkedAt: null });
  assert.deepEqual(statusOf(server({ status: { state: "failed", error: "connection refused" } }), false), { kind: "failed", error: "connection refused" });
  assert.deepEqual(statusOf(server({ status: { state: "needs_login" } }), false), { kind: "needs_login" });
  assert.deepEqual(statusOf(server({ connector: "feishu", auth_state: "needs_login", enabled: false }), false), { kind: "needs_login" });
  assert.deepEqual(statusOf(server({}), false), { kind: "unknown" });
  assert.equal(isCommand(server({ transport: "stdio" })), true);
  assert.equal(isCommand(server({})), false);
});

test("command lines split and join as a shell would quote them", () => {
  assert.deepEqual(splitCommand(`npx -y "@scope/server name" --root 'a b' c\\ d`), ["npx", "-y", "@scope/server name", "--root", "a b", "c d"]);
  assert.deepEqual(splitCommand('say "he said \\"hi\\""'), ["say", 'he said "hi"']);
  assert.deepEqual(splitCommand("  "), []);
  assert.equal(joinCommand("npx", ["-y", "a b"]), 'npx -y "a b"');
  assert.deepEqual(splitCommand(joinCommand("run", ["it's", "x"])), ["run", "it's", "x"]);
});

test("secrets: the credential a value is kept under, and the reference written for it", () => {
  assert.equal(credentialName("github", "header.X-Api Key"), "mcp.github.header.X-Api_Key");
  assert.equal(credentialRef("Bearer ${credential:mcp.github}"), "mcp.github");
  assert.equal(credentialRef("plain"), null);
  assert.equal(withCredential("Bearer ${credential:mcp.old}", "mcp.new"), "Bearer ${credential:mcp.new}");
  assert.equal(withCredential(null, "mcp.new"), "${credential:mcp.new}");
});

test("names: a tile's letters, and a key path's segment", () => {
  assert.equal(monogram("google-drive"), "GD");
  assert.equal(monogram("sim"), "SI");
  assert.equal(quoteKey("github_2"), "github_2");
  assert.equal(quoteKey("my.server"), '"my.server"');
});
