import assert from "node:assert/strict";
import { test } from "node:test";
import type { BrowserStatus, ProviderSummary, SkillEntry, WebSearchStatus } from "@hermesh/her-protocol";
import { allSitesEdits, browsers, brokenSkills, bySize, chromeName, extensionEdits, providers, searchMoved, searchSwitchEdits, siteEdits, siteOrigin, skillItems } from "./abilities.ts";

function skill(name: string, o: Partial<SkillEntry> = {}): SkillEntry {
  return { name, path: `/Users/me/.her/skills/${name}/SKILL.md`, scope: "user", state: "active", description: `What ${name} does`, default_enabled: true, model_invocable: true, ...o } as SkillEntry;
}

test("skills are the environment's, by group, with where each came from", () => {
  const list = {
    enabled: true,
    warnings: [],
    skills: [
      skill("weekly-report", { source: { kind: "upload" } }),
      skill("brand-voice"),
      skill("meeting-notes", { state: "disabled" }),
      skill("skill-creator", { scope: "system" }),
      skill("old-copy", { state: "shadowed" }),
      skill("broken", { state: "invalid", detail: "no name" }),
      skill("repo-only", { scope: "project" }),
    ],
  };
  const items = skillItems(list);
  assert.deepEqual(items.map((i) => i.entry.name), ["weekly-report", "brand-voice", "meeting-notes", "skill-creator"]);
  assert.deepEqual(items.map((i) => i.source), ["upload", "local", "local", "system"]);
  assert.deepEqual(items.map((i) => i.on), [true, true, false, true]);
  assert.equal(items[0].dir, "/Users/me/.her/skills/weekly-report");
  assert.deepEqual(brokenSkills(list).map((e) => e.name), ["broken"]);
});

test("web search's switches and order are written as the console writes them", () => {
  const status = {
    enabled: true,
    current: "exa",
    sources: [
      { id: "exa", name: "Exa", configured: true, enabled: true, needs: "nothing" },
      { id: "deepseek", name: "DeepSeek", configured: true, enabled: false, needs: "account" },
      { id: "tavily", name: "Tavily", configured: false, enabled: true, needs: "key" },
    ],
  } as WebSearchStatus;
  assert.deepEqual(searchSwitchEdits(status, "exa", false), [{ key_path: "web_search.disabled", value: ["exa", "deepseek"] }]);
  assert.deepEqual(searchSwitchEdits(status, "deepseek", true), [{ key_path: "web_search.disabled", value: [] }]);
  assert.deepEqual(searchMoved(status, 2, 0), ["tavily", "exa", "deepseek"]);
  assert.deepEqual(searchMoved(status, 0, 2), ["deepseek", "tavily", "exa"]);
});

test("the browsers as they stand, from browser/status and [browser]", () => {
  const none = browsers({ embedded: false, window: false, os: "linux" } as BrowserStatus, {});
  assert.equal(none.controlling, false);
  assert.equal(none.yours, "off");
  assert.equal(none.allSites, "ask");
  assert.equal(none.os, "linux");
  const used = browsers({ embedded: false, window: true, program: "Google Chrome", extension: { browser: "Google Chrome 141.0.1" } } as BrowserStatus, {
    browser: { extension: true, default: "yours", sites: { "*": "navigate", "https://github.com": "act" } },
  });
  assert.equal(used.controlling, true);
  assert.equal(used.yours, "on");
  assert.equal(used.opensInYours, true);
  assert.equal(chromeName(used.chrome!), "Google Chrome");
  assert.equal(used.allSites, "navigate");
  assert.deepEqual(used.exceptions, [["https://github.com", "act"]]);
  const lost = browsers({ embedded: false, window: false } as BrowserStatus, { browser: { extension: true, enabled: false } });
  assert.equal(lost.yours, "lost");
  assert.equal(lost.controlling, false);
  const old = browsers({ embedded: true, window: false, extension: { problem: "update_extension" } } as BrowserStatus, { browser: { backend: "extension" } });
  assert.equal(old.yours, "outdated");
  assert.equal(old.legacyBackend, true);
  assert.deepEqual(allSitesEdits("ask"), [{ key_path: 'browser.sites."*"', value: null }]);
  assert.deepEqual(siteEdits("https://github.com", "act"), [{ key_path: 'browser.sites."https://github.com"', value: "act" }]);
  assert.deepEqual(extensionEdits(true, true), [{ key_path: "browser.extension", value: true }, { key_path: "browser.backend", value: null }]);
});

test("a site is kept by its origin", () => {
  assert.equal(siteOrigin("github.com"), "https://github.com");
  assert.equal(siteOrigin(" https://GitHub.com/a?b "), "https://github.com");
  assert.equal(siteOrigin("http://localhost:3000/x"), "http://localhost:3000");
  assert.equal(siteOrigin("ftp://example.com"), null);
  assert.equal(siteOrigin(""), null);
});

test("the Models page groups the providers", () => {
  const p = (id: string, o: Partial<ProviderSummary>) => ({ id, name: id, auth: "api_key", credential: "missing", ...o }) as ProviderSummary;
  const plan = { api: "openai-chat", base_url: "https://x", dynamic_models: false, key_url: "https://k" };
  const g = providers([
    p("deepseek", { credential: "stored" }),
    p("openai", {}),
    p("anthropic", { credential: "env" }),
    p("ollama", { auth: "none", credential: "not_required" }),
    p("zhipu-coding-plan", { credential: "stored", plan }),
    p("bailian-token-plan", { plan }),
    // Services added by their address, a local one without a key.
    p("acme", { credential: "stored", custom: { base_url: "https://llm.acme.com/v1", protocols: [] } }),
    p("vllm", { auth: "none", credential: "not_required", custom: { base_url: "http://127.0.0.1:8000/v1", protocols: [] } }),
  ]);
  assert.deepEqual(g.keyed.map((x) => x.id), ["deepseek", "anthropic", "acme", "vllm"]);
  assert.deepEqual(g.addedPlans.map((x) => x.id), ["zhipu-coding-plan"]);
  assert.deepEqual(g.plansLeft.map((x) => x.id), ["bailian-token-plan"]);
});

test("her data by size", () => {
  assert.deepEqual(
    bySize([
      { kind: "files", bytes: 10 },
      { kind: "conversations", bytes: 30 },
      { kind: "memory", bytes: 1 },
    ]).map((x) => x.kind),
    ["conversations", "files", "memory"],
  );
});
