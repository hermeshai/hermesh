import type {
  CommandInfo, ConfigEdit, ConfigLayer, ConfigLayerInfo, ConfigReadResult, ConfigWriteResult, McpCheckResult, McpListResult, McpLoginParams, McpLoginResult,
  McpProbeParams, McpProbeResult, McpProbeStep, McpServerView, McpToolSummary,
  McpToolsResult, SkillEntry, SkillsFilesResult, SkillsInstallParams, SkillsReadResult, SkillsInstallResult, SkillsListResult, SkillsSetEnabledResult,
  WebSearchNeeds, WebSearchSetKeyResult, WebSearchSource, WebSearchStatus, WebSearchTestResult,
} from "@hermesh/her-protocol";

// The 自定义 page's side of the mock (customize-design §10.8): skills, MCP servers, the
// configuration's layers and the keychain, with the prototype's data
// (docs/desktop/customize-demo.html). Changes tell every connection, as her does.

const home = "/Users/demo";
const project = "/Users/demo/code/hermesh";
const files: Record<"user" | "user_project" | "project", string> = {
  user: `${home}/.her/config.toml`,
  user_project: `${home}/.her/projects/hermesh-3fa81c02/config.toml`,
  project: `${project}/.her/config.toml`,
};

/** 联网搜索's services in their default order (web-search-design §3). */
const searchServices: { id: string; name: string; needs: WebSearchNeeds; key_url: string | null }[] = [
  { id: "deepseek", name: "DeepSeek", needs: "account", key_url: null },
  { id: "exa", name: "Exa", needs: "nothing", key_url: "https://dashboard.exa.ai/api-keys" },
  { id: "parallel", name: "Parallel", needs: "nothing", key_url: "https://platform.parallel.ai/" },
  { id: "tavily", name: "Tavily", needs: "key", key_url: "https://app.tavily.com/home" },
  { id: "brave", name: "Brave", needs: "key", key_url: "https://api-dashboard.search.brave.com/app/keys" },
  { id: "bocha", name: "博查", needs: "key", key_url: "https://open.bochaai.com/" },
];

type Skill = { name: string; description: string; dir: string; scope: SkillEntry["scope"]; on: boolean; offByDefault?: boolean; removable?: boolean; source?: SkillEntry["source"]; userOnly?: boolean; files: [string, number][] };

/** Four colour bands, for a skill's image file. */
const previewPng = "iVBORw0KGgoAAAANSUhEUgAAAKAAAABgCAIAAAAVRe7OAAAA+ElEQVR42u3RMQ0AIAwAMJyghOCGhwcVmEEMkoYD3j1N6qClzkjR704Rp+UYK0URLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsWLFiwYMGCBQsW/PMAyCy8+2BYNjoAAAAASUVORK5CYII=";
const skillFiles = (...extra: [string, number][]): [string, number][] => [["SKILL.md", 2100], ...extra];

function demoSkills(): Skill[] {
  return [
    { name: "gen-report", description: "生成团队周报：读取本周的提交和任务，按固定格式写出 report.md", dir: `${home}/.her/skills/gen-report`, scope: "user", on: true, removable: true, source: { kind: "upload" }, files: skillFiles(["references/format.md", 3000]) },
    { name: "pdf-tools", description: "提取 PDF 里的文字和表格。读取、转换或检查 PDF 文件时使用。", dir: `${home}/.her/skills/pdf-tools`, scope: "user", on: true, removable: true, source: { kind: "upload" }, files: skillFiles(["scripts/extract.py", 6400], ["references/formats.md", 3000], ["assets/preview.png", 305]) },
    { name: "release-notes", description: "Turns the commits between two versions into release notes, grouped as new, improved and fixed.", dir: `${home}/.her/skills/release-notes`, scope: "user", on: true, removable: true, source: { kind: "catalog", id: "release-notes", version: "1.0.0" }, files: skillFiles() },
    { name: "commit-style", description: "本仓库提交信息的格式要求，写提交信息时使用", dir: `${project}/.agents/skills/commit-style`, scope: "project", on: true, files: skillFiles() },
    { name: "kernel-e2e", description: "用真实模型跑一遍内核验收：启动 her、发起会话、核对事件流和落盘的会话文件。", dir: `${project}/.agents/skills/kernel-e2e`, scope: "project", on: true, files: skillFiles(["scripts/run.sh", 1100]) },
    // In the repository's .claude: another app's folder, off until turned on (skill-design D-173).
    { name: "frontend-design", description: "做网页和界面时使用：给出有个性的排版、配色和动效，避免千篇一律的模板感。", dir: `${project}/.claude/skills/frontend-design`, scope: "project", on: false, offByDefault: true, files: skillFiles() },
    { name: "skill-creator", description: "Create, improve, and evaluate skills using realistic test cases, baseline comparisons, and user feedback.", dir: `${home}/.her/skills/.system/skill-creator`, scope: "system", on: true, userOnly: true, files: skillFiles() },
  ];
}

type Server = { name: string; layer: "user" | "user_project" | "project"; config: Record<string, unknown>; status: McpServerView["status"]; tools: McpToolSummary[]; fails?: number; oauth?: boolean };

const tools = (...names: [string, string][]): McpToolSummary[] => names.map(([name, description]) => ({ name, description }));
const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();

function demoServers(): Server[] {
  return [
    {
      name: "github",
      layer: "user",
      config: { url: "https://api.githubcopilot.com/mcp/", headers: { Authorization: "Bearer ${credential:mcp.github.Authorization}" } },
      status: { state: "ok", source: "session", sessions: 2, tools: 6, checked_at: ago(30) },
      tools: tools(["create_issue", "在仓库里新建一个议题"], ["get_pull_request", "读取拉取请求的标题、描述和改动"], ["list_commits", "列出一个分支上的提交"], ["search_code", "在仓库里搜索代码"], ["merge_pull_request", "合并一个拉取请求"], ["get_workflow_run", "查看一次 CI 运行的结果和日志"]),
    },
    {
      name: "playwright",
      layer: "user",
      config: { command: "npx", args: ["@playwright/mcp@latest"] },
      status: { state: "ok", source: "cache", tools: 5, checked_at: ago(180) },
      tools: tools(["browser_navigate", "打开一个网址"], ["browser_click", "点击页面上的元素"], ["browser_type", "在输入框里打字"], ["browser_snapshot", "读取页面的结构，供模型定位元素"], ["browser_take_screenshot", "给当前页面截图"]),
    },
    { name: "fetch", layer: "user", config: { command: "uvx", args: ["mcp-server-fetch"] }, status: { state: "unknown" }, tools: tools(["fetch", "抓取一个网址并转成 Markdown"]) },
    { name: "linear", layer: "user", config: { url: "https://mcp.linear.app/mcp" }, status: { state: "needs_login" }, tools: [], oauth: true },
    {
      name: "sentry",
      layer: "user",
      config: { url: "https://mcp.sentry.dev/mcp", headers: { Authorization: "Bearer ${credential:mcp.sentry.Authorization}" } },
      status: { state: "ok", source: "cache", tools: 3, checked_at: ago(60 * 24 * 6) },
      tools: tools(["search_issues", "按条件查找错误"], ["get_issue_details", "读取一个错误的堆栈和发生次数"], ["list_projects", "列出组织里的项目"]),
    },
    {
      name: "postgres-dev",
      layer: "user_project",
      config: { command: "uvx", args: ["mcp-server-postgres"], env: { DATABASE_URL: "${credential:mcp.postgres-dev.DATABASE_URL}" } },
      status: { state: "failed", source: "cache", error: "connection refused: localhost:5432\n进程退出，代码 1", checked_at: ago(10) },
      tools: tools(["query", "执行只读查询"], ["list_tables", "列出数据表"]),
      fails: 1,
    },
    {
      name: "docs-search",
      layer: "project",
      config: { url: "https://docs.internal.example/mcp", description: "在团队的内部文档里搜索，返回段落和原文链接。" },
      status: { state: "ok", source: "cache", tools: 2, checked_at: ago(60 * 24) },
      tools: tools(["search_docs", "按关键词搜索内部文档"], ["read_page", "读取一篇文档的全文"]),
    },
    {
      name: "staging-db",
      layer: "project",
      config: { command: "npx", args: ["-y", "@acme/staging-db-mcp"], description: "只读查询预发布环境的数据库。" },
      status: { state: "ok", source: "cache", tools: 3, checked_at: ago(60 * 72) },
      tools: tools(["list_tables", "列出数据表"], ["describe_table", "查看表结构"], ["query", "执行只读查询"]),
    },
  ];
}

export class Failure extends Error {
  readonly code: number;
  readonly kind: string;
  /** More of the error's data, beside its kind. */
  readonly extra: Record<string, unknown>;
  constructor(code: number, kind: string, message: string, extra: Record<string, unknown> = {}) {
    super(message);
    this.code = code;
    this.kind = kind;
    this.extra = extra;
  }
}

/**
 * A page opened with `?many`: dozens more skills and servers, names short and long, for
 * checking how long lists look (the "+" menu's submenus, the 自定义 page).
 */
const many = typeof location !== "undefined" && new URLSearchParams(location.search).has("many");

const manyNames = [
  "notion", "slack", "jira", "confluence", "figma", "google-drive", "gmail", "google-calendar", "asana", "trello", "hubspot", "salesforce-crm-production",
  "stripe", "shopify", "zendesk", "intercom", "datadog", "grafana-observability", "pagerduty", "cloudflare", "vercel", "supabase", "mongodb-atlas", "redis",
  "airtable", "dropbox", "box", "miro", "canva", "zoom",
];

function manySkills(): Skill[] {
  const names = [
    "api-docs", "brand-voice", "changelog", "code-review-checklist", "csv-cleaner", "db-migrations", "deploy-staging", "design-tokens", "docx", "excel-reports",
    "i18n-strings", "incident-postmortem-writer-with-timeline-and-action-items", "itinerary-planner", "meeting-notes", "onboarding-guide", "perf-profiling",
    "pptx", "pr-description", "security-audit", "seo-audit", "sql-optimizer", "test-plan", "translate-zh-en", "ui-copy", "weekly-okr", "xlsx",
    "数据看板", "合同审阅", "周会纪要", "竞品分析报告", "面试题库", "用户访谈整理", "旅行攻略", "招聘JD", "发布检查清单", "客服话术",
  ];
  return names.map((name, i) => ({
    name,
    description: i % 3 === 0 ? `用于${name}相关的任务：读取上下文，按团队的固定格式产出结果，并在结尾列出待确认的事项。` : `Helps with ${name}: reads what is there, follows the team's format, and lists what to check.`,
    dir: `${home}/.her/skills/${name}`,
    scope: "user" as const,
    on: i % 7 !== 3,
    removable: true,
    source: { kind: "upload" as const },
    files: skillFiles(),
  }));
}

function manyServers(): Server[] {
  return manyNames.map((name, i) => ({
    name,
    layer: "user" as const,
    config: { url: `https://mcp.${name}.example/mcp` },
    status: i % 9 === 4 ? { state: "needs_login" as const } : i % 11 === 6 ? { state: "failed" as const, source: "cache" as const, error: "connection refused", checked_at: ago(5) } : { state: "ok" as const, source: "cache" as const, tools: 3, checked_at: ago(60) },
    tools: tools(["search", "Search"], ["read", "Read one item"], ["list", "List items"]),
  }));
}

export class CustomizeMock {
  private skills = many ? [...demoSkills(), ...manySkills()] : demoSkills();
  private servers = many ? [...demoServers(), ...manyServers()] : demoServers();
  /** `mcp_servers.<name>.enabled` as each of the person's files says it. */
  private enabled: Record<"user" | "user_project", Map<string, boolean>> = { user: new Map([["sentry", false]]), user_project: new Map([["staging-db", false]]) };
  private readonly saved = new Set(["mcp.github.Authorization", "mcp.postgres-dev.DATABASE_URL", "mcp.sentry.Authorization"]);
  /** `[browser]` and `[desktop]` of the person's own file: the system connectors' switches, sites and apps (connectors-entry-design §4.3). */
  private readonly system: Record<string, Record<string, unknown>> = {
    browser: { extension: true, sites: { "*": "navigate", "https://github.com": "act" } },
    desktop: { apps: { "com.apple.Safari": "allow" } },
    web_search: {},
  };
  private versions: Record<string, number> = { user: 1, user_project: 1, project: 1 };
  private readonly notify: (method: string, params: unknown) => void;

  constructor(notify: (method: string, params: unknown) => void) {
    this.notify = notify;
  }

  // ---- skills ----

  skillsList(): SkillsListResult {
    const entries: SkillEntry[] = this.skills.map((s) => ({
      name: s.name,
      description: s.description,
      path: `${s.dir}/SKILL.md`,
      scope: s.scope,
      model_invocable: !s.userOnly,
      state: s.on ? "active" : "disabled",
      default_enabled: !s.offByDefault,
      ...(s.removable ? { removable: true } : {}),
      ...(s.source ? { source: s.source } : {}),
    }));
    entries.push({ name: "old-notes", description: "", path: `${home}/.her/skills/old-notes/SKILL.md`, scope: "user", model_invocable: true, state: "invalid", default_enabled: true, detail: "缺少 description" });
    const commands: CommandInfo[] = [
      { name: "compact", description: "Summarize the conversation so far to free up the context window" },
      { name: "context", description: "Show what fills the context window" },
      ...this.skills.filter((s) => s.on).map((s) => ({ name: `skill:${s.name}`, description: s.description, skill: `${s.dir}/SKILL.md` })),
    ];
    return { skills: entries, commands, enabled: true, warnings: [] };
  }

  private skillAt(path: string): Skill {
    const dir = path.replace(/\/SKILL\.md$/i, "");
    const s = this.skills.find((x) => x.dir === dir);
    if (!s) throw new Failure(-32602, "invalid_params", `no skill at ${path}`);
    return s;
  }

  skillsInstall(p: SkillsInstallParams): SkillsInstallResult {
    const from = p.from.replace(/\/SKILL\.md$/i, "").replace(/\.(zip|skill)$/i, "");
    const name = from.split("/").pop() ?? "skill";
    if (!/^[a-z0-9][a-z0-9-]*$/.test(name)) throw new Failure(-32027, "skill_invalid", `not a usable skill: '${name}' is not a skill name`);
    const catalog = p.source?.kind === "catalog";
    const dir = `${home}/.her/skills/${name}`;
    const conflict = this.skills.some((s) => s.dir === dir) ? dir : null;
    const description = catalog ? `The ${name} skill from her's catalog.` : "上传的技能：按说明完成对应的任务。";
    const skill: Skill = { name, description, dir, scope: "user", on: true, removable: true, source: p.source ?? { kind: "upload" }, files: skillFiles() };
    const entry: SkillEntry = { name, description, path: `${dir}/SKILL.md`, scope: "user", model_invocable: true, state: "active", default_enabled: true, removable: true };
    if (p.dry_run) return { skill: entry, target: dir, ...(conflict ? { conflict } : {}) };
    if (conflict && !p.replace) throw new Failure(-32028, "skill_conflict", `a skill named ${name} is already at ${dir}`, { path: dir });
    this.skills = [...this.skills.filter((s) => s.dir !== dir), skill];
    this.notify("skills/changed", {});
    return { skill: entry, target: dir };
  }

  skillsRemove(path: string) {
    const s = this.skillAt(path);
    if (!s.removable) throw new Failure(-32029, "not_removable", `${s.dir} is not one of your own skills`);
    this.skills = this.skills.filter((x) => x !== s);
    this.notify("skills/changed", {});
    return {};
  }

  skillsSetEnabled(path: string, enabled: boolean): SkillsSetEnabledResult {
    this.skillAt(path).on = enabled;
    this.versions.user++;
    this.notify("config/changed", { file: files.user });
    this.notify("skills/changed", {});
    return { file: files.user };
  }

  private skillOrCatalog(path: string) {
    const catalog = /\/catalog\/skills\/([^/]+)$/.exec(path.replace(/\/SKILL\.md$/i, ""));
    return catalog ? { name: catalog[1], description: `The ${catalog[1]} skill from her's catalog.`, files: skillFiles() } : this.skillAt(path);
  }

  private skillMd(s: { name: string; description: string }): string {
    return `---\nname: ${s.name}\ndescription: ${s.description}\n---\n\n# ${s.name}\n\nRead \`references/\` first, then work through the steps.\n\n## Steps\n\n1. Find out what the user wants done, and with which files.\n2. Run the script in \`scripts/\` that fits; do not rewrite it.\n3. Check the result, and say why when something is skipped.\n`;
  }

  skillsFiles(path: string): SkillsFilesResult {
    const s = this.skillOrCatalog(path);
    const skill_md = this.skillMd(s);
    const dirs = new Set(s.files.flatMap(([f]) => (f.includes("/") ? [f.split("/")[0]] : [])));
    return {
      skill_md,
      truncated: false,
      files: [...[...dirs].map((d) => ({ path: d, size: 0, dir: true })), ...s.files.map(([f, size]) => ({ path: f, size, dir: false }))].sort((a, b) => a.path.localeCompare(b.path)),
    };
  }

  skillsRead(path: string, file: string): SkillsReadResult {
    const s = this.skillOrCatalog(path);
    if (!s.files.some(([f]) => f === file)) throw new Failure(-32027, "skill_invalid", `${file} is not a file of the skill`);
    if (file === "SKILL.md") return { text: this.skillMd(s), truncated: false, binary: false };
    if (/\.png$/i.test(file)) return { text: "", truncated: false, binary: true, image: { mime_type: "image/png", data: previewPng } };
    if (/\.(jpe?g|gif|zip|pdf)$/i.test(file)) return { text: "", truncated: false, binary: true };
    const text = file.endsWith(".md")
      ? `# ${file.split("/").pop()!.replace(/\.md$/, "")}\n\nWhat ${s.name} needs to know, read only when a step asks for it.\n\n| Field | Meaning |\n|---|---|\n| \`title\` | One line, no period |\n| \`items\` | One per change, newest first |\n\n\`\`\`json\n{ "title": "Week 44", "items": [] }\n\`\`\`\n`
      : file.endsWith(".py")
        ? `#!/usr/bin/env python3\n"""${s.name}: ${file}."""\n\nimport sys\n\n\ndef main(path: str) -> int:\n    with open(path, "rb") as f:\n        print(len(f.read()), "bytes")\n    return 0\n\n\nif __name__ == "__main__":\n    sys.exit(main(sys.argv[1]))\n`
        : `#!/bin/sh\nset -eu\necho "${s.name}: ${file}"\n`;
    return { text, truncated: false, binary: false };
  }

  // ---- configuration ----

  private layers(): ConfigLayerInfo[] {
    return (["user", "project", "user_project"] as const).map((layer) => ({
      layer,
      file: files[layer],
      version: `v${this.versions[layer]}`,
      writable: layer !== "project",
      exists: true,
    }));
  }

  configRead(withLayers: boolean): ConfigReadResult {
    const mcp_servers: Record<string, unknown> = {};
    const origins: Record<string, ConfigLayer> = {};
    for (const s of this.servers) {
      const on = this.isEnabled(s);
      mcp_servers[s.name] = { ...s.config, ...(on ? {} : { enabled: false }) };
      for (const k of Object.keys(s.config)) origins[`mcp_servers.${s.name}.${k}`] = s.layer;
    }
    for (const [table, keys] of Object.entries(this.system)) for (const k of Object.keys(keys)) origins[`${table}.${k}`] = "user";
    return { config: { model: "deepseek/deepseek-chat", mcp_servers, ...structuredClone(this.system) }, origins, ...(withLayers ? { layers: this.layers() } : {}) };
  }

  /** Whether the person's own browser is on: the extension then answers, as if Chrome were open. */
  browserStatus() {
    return { embedded: false, extension: this.system.browser.extension === true ? { browser: "Google Chrome 141.0.7390.54", extension_version: "0.1.0" } : null };
  }

  private isEnabled(s: Server): boolean {
    return this.enabled.user_project.get(s.name) ?? this.enabled.user.get(s.name) ?? true;
  }

  configWrite(layer: ConfigLayer, edits: ConfigEdit[], expected?: string | null): ConfigWriteResult {
    if (layer !== "user" && layer !== "user_project") throw new Failure(-32024, "config_layer_readonly", `her does not write the ${layer} layer`);
    if (expected && expected !== `v${this.versions[layer]}`) throw new Failure(-32023, "config_version_conflict", "the configuration changed since it was read; read it again", { version: `v${this.versions[layer]}` });
    let dropped: string[] = [];
    for (const e of edits) {
      const path = segments(e.key_path);
      if (layer === "user" && path[0] in this.system && path.length > 1) {
        setPath(this.system[path[0]], path.slice(1), e.value);
        if (e.key_path === "browser.extension") this.notify("browser/changed", this.browserStatus());
        continue;
      }
      const m = /^mcp_servers\.("?)([^".]+)\1(?:\.(enabled))?$/.exec(e.key_path);
      if (!m) continue;
      const name = m[2];
      if (m[3]) {
        if (e.value === null) this.enabled[layer].delete(name);
        else this.enabled[layer].set(name, e.value === true);
        continue;
      }
      const old = this.servers.find((s) => s.name === name && s.layer === layer);
      if (e.value === null) {
        if (old) dropped = JSON.stringify(old.config).match(/credential:[^}]+/g)?.map((x) => x.slice("credential:".length)) ?? [];
        this.servers = this.servers.filter((s) => !(s.name === name && s.layer === layer));
        continue;
      }
      const value = e.value as Record<string, unknown>;
      const config = e.merge === "upsert" && old ? Object.fromEntries(Object.entries({ ...old.config, ...value }).filter(([, v]) => v !== null)) : value;
      if (!config.command && !config.url) throw new Failure(-32025, "config_validation_error", "give a command or a url", { errors: [{ key_path: `mcp_servers.${name}`, message: "give a command or a url" }] });
      if (old) old.config = config;
      else this.servers.push({ name, layer, config, status: { state: "unknown" }, tools: [] });
    }
    for (const name of dropped) this.saved.delete(name);
    this.versions[layer]++;
    this.notify("config/changed", { file: files[layer] });
    this.notify("mcp/changed", {});
    return { status: "ok", version: `v${this.versions[layer]}`, file: files[layer] };
  }

  credentialsSet(name: string) {
    if (!/^(mcp(-oauth)?|web-search)\.[A-Za-z0-9._-]{1,96}$/.test(name)) throw new Failure(-32602, "invalid_params", `'${name}' is not a credential name her manages`);
    this.saved.add(name);
    this.notify("mcp/changed", {});
    return {};
  }

  credentialsDelete(name: string) {
    this.saved.delete(name);
    this.notify("mcp/changed", {});
    return {};
  }

  // ---- web search ----

  private searchOrder(): (typeof searchServices)[number][] {
    const named = (this.system.web_search.order ?? []) as string[];
    const first = named.flatMap((id) => searchServices.filter((s) => s.id === id));
    return [...first, ...searchServices.filter((s) => !first.includes(s))];
  }

  webSearchStatus(): WebSearchStatus {
    const disabled = (this.system.web_search.disabled ?? []) as string[];
    const sources: WebSearchSource[] = this.searchOrder().map((s) => {
      const saved = this.saved.has(`web-search.${s.id}`);
      return {
        id: s.id,
        name: s.name,
        needs: s.needs,
        configured: s.needs !== "key" || saved,
        enabled: !disabled.includes(s.id),
        key: s.needs === "account" ? "account" : saved ? "saved" : null,
        key_url: s.key_url,
      };
    });
    return { enabled: this.system.web_search.enabled !== false, current: sources.find((s) => s.configured && s.enabled)?.id ?? null, sources };
  }

  webSearchTest(): WebSearchTestResult {
    return { ok: true, count: 10, elapsed_ms: 900 };
  }

  /** A key with "bad" in it is refused, as a real service would; one that works goes first and its switch goes on. */
  webSearchSetKey(source: string, key: string): WebSearchSetKeyResult {
    if (key.includes("bad")) return { saved: false, test: { ok: false, count: 0, elapsed_ms: 300, error: "auth_failed" } };
    const table = this.system.web_search;
    if (!this.saved.has(`web-search.${source}`)) table.order = [source, ...this.searchOrder().map((s) => s.id).filter((id) => id !== source)];
    table.disabled = ((table.disabled ?? []) as string[]).filter((id) => id !== source);
    this.saved.add(`web-search.${source}`);
    this.notify("config/changed", { file: files.user });
    return { saved: true, test: this.webSearchTest() };
  }

  // ---- servers ----

  /** The demo's logos, by host: what the kernel would have found (mcp-design §13.9). */
  private static logos: Record<string, string> = {
    "mcp.linear.app": svgLogo("#5e6ad2", "M6 18 18 6M6 12l6-6M12 18l6-6"),
    "api.githubcopilot.com": svgLogo("#1f2328", "M12 7a5 5 0 1 0 0 10a5 5 0 1 0 0-10"),
    "mcp.hermesh.io": svgLogo("#0f9f6e", "M7 6v12M17 6v12M7 12h10"),
  };

  private static logoOf(config: Record<string, unknown>): string | undefined {
    try {
      return typeof config.url === "string" ? CustomizeMock.logos[new URL(config.url).hostname] : undefined;
    } catch {
      return undefined;
    }
  }

  private view(s: Server): McpServerView {
    const credentials = JSON.stringify(s.config).match(/credential:[^}]+/g)?.map((x) => x.slice("credential:".length)) ?? [];
    const on = this.isEnabled(s);
    const disabledBy = on ? null : this.enabled.user_project.get(s.name) === false ? "user_project" : "user";
    const args = Array.isArray(s.config.args) ? (s.config.args as string[]) : [];
    return {
      name: s.name,
      layer: s.layer,
      file: files[s.layer],
      editable: s.layer !== "project",
      enabled: on,
      ...(disabledBy ? { disabled_by: disabledBy } : {}),
      transport: s.config.url ? (s.config.transport === "sse" || /\/sse\/?$/.test(String(s.config.url)) ? "sse" : "http") : "stdio",
      summary: typeof s.config.url === "string" ? s.config.url : [s.config.command, ...args].join(" "),
      ...(typeof s.config.description === "string" ? { description: s.config.description } : {}),
      auth: s.oauth || s.config.auth === "oauth" ? "oauth" : credentials.length ? "credential" : "none",
      ...(s.oauth || s.config.auth === "oauth" ? { auth_state: this.signedIn.has(s.name) ? "logged_in" : "needs_login" } : credentials.length ? { auth_state: credentials.every((c) => this.saved.has(c)) ? "saved" : "missing" } : {}),
      credentials,
      status: s.status,
      identity: identityOf(s.config),
      ...(CustomizeMock.logoOf(s.config) ? { icon: CustomizeMock.logoOf(s.config) } : {}),
    };
  }

  private server(name: string): Server {
    const s = this.servers.find((x) => x.name === name);
    if (!s) throw new Failure(-32602, "invalid_params", `no MCP server named ${name}`);
    return s;
  }

  mcpList(): McpListResult {
    return { servers: this.servers.map((s) => this.view(s)), warnings: [] };
  }

  mcpTools(name: string): McpToolsResult {
    const s = this.server(name);
    return s.status.state === "ok" ? { tools: s.tools, source: s.status.source === "session" ? "session" : "cache", checked_at: s.status.checked_at } : { tools: [], source: "none" };
  }

  private signedIn = new Set<string>();

  /**
   * Checks a server before it is added, step by step, as her does (mcp-design §13.6). The
   * address picks what the check finds, the prototype's cases (connector-add-demo.html).
   */
  async mcpProbe(p: McpProbeParams): Promise<McpProbeResult> {
    const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
    const steps: McpProbeStep[] = [];
    const set = (step: McpProbeStep) => {
      const i = steps.findIndex((s) => s.id === step.id);
      if (i >= 0) steps[i] = step;
      else steps.push(step);
      this.notify("mcp/probe_progress", { probe_id: p.probe_id, step });
    };
    const demoTools = tools(["search", "Search what the server offers"], ["read", "Read one item in full"], ["list", "List items"], ["create", "Create an item"]);
    const url = p.server.url ?? null;
    if (!url) {
      const command = [p.server.command, ...(p.server.args ?? [])].join(" ");
      set({ id: "start", state: "running" });
      await sleep(900);
      if (/nonexistent|does-not-exist/.test(command)) {
        set({ id: "start", state: "failed", detail: "npm error 404 Not Found - the package does not exist" });
        set({ id: "handshake", state: "skipped" });
        set({ id: "tools", state: "skipped" });
        return { kind: "local", steps, suggested: { auth: "none" }, error: "npm error 404 Not Found - the package does not exist" };
      }
      set({ id: "start", state: "ok" });
      set({ id: "handshake", state: "running" });
      await sleep(600);
      set({ id: "handshake", state: "ok" });
      set({ id: "tools", state: "running" });
      await sleep(400);
      set({ id: "tools", state: "ok", detail: `${demoTools.length} tools` });
      return { kind: "local", steps, suggested: { auth: "none" }, tools: demoTools };
    }
    const host = new URL(url).hostname;
    const token = Object.keys(p.server.headers ?? {}).length > 0;
    set({ id: "connect", state: "running" });
    await sleep(800);
    if (/unreachable|\.test$/.test(host)) {
      set({ id: "connect", state: "failed", detail: `error sending request: dns error: no host ${host}` });
      set({ id: "resource", state: "skipped" });
      set({ id: "auth_server", state: "skipped" });
      return { kind: "remote", steps, suggested: { auth: "none" }, error: `error sending request: dns error: no host ${host}` };
    }
    const sse = /\/sse\/?$/.test(url) ? "sse" : "streamable_http";
    if (token || /context7|localhost|127\.0\.0\.1|\/sse/.test(url)) {
      if (token && /wrong/.test(JSON.stringify(p.server.headers))) {
        set({ id: "connect", state: "failed", detail: "the server refused the token (HTTP 401)", http_status: 401 });
        set({ id: "resource", state: "skipped" });
        set({ id: "auth_server", state: "skipped" });
        return { kind: "remote", steps, suggested: { auth: "token" }, error: "the server refused the token (HTTP 401)" };
      }
      set({ id: "connect", state: "ok", detail: `connected; ${demoTools.length} tools`, http_status: 200 });
      set({ id: "resource", state: "skipped", detail: "no sign-in needed" });
      set({ id: "auth_server", state: "skipped", detail: "no sign-in needed" });
      return { kind: "remote", steps, transport: sse, auth: { required: token }, suggested: { auth: token ? "token" : "none" }, tools: demoTools };
    }
    set({ id: "connect", state: "ok", detail: "the server requires authorization", http_status: 401 });
    set({ id: "resource", state: "running" });
    await sleep(700);
    const issuer = /hermesh/.test(host) ? "https://hermesh.io" : /linear/.test(host) ? "https://mcp.linear.app" : /github/.test(host) ? "https://github.com/login/oauth" : null;
    if (!issuer) {
      set({ id: "resource", state: "skipped", detail: "no OAuth settings published; it probably takes an API key" });
      set({ id: "auth_server", state: "skipped" });
      return { kind: "remote", steps, transport: sse, auth: { required: true }, suggested: { auth: "token" } };
    }
    set({ id: "resource", state: "ok", detail: `authorization server ${issuer}` });
    set({ id: "auth_server", state: "running" });
    await sleep(700);
    const github = /github/.test(issuer);
    set({ id: "auth_server", state: "ok", detail: github ? "PKCE" : "PKCE, client metadata documents, dynamic registration" });
    return {
      kind: "remote",
      steps,
      transport: sse,
      auth: { required: true, oauth: { authorization_server: issuer, pkce: true, metadata_document: !github, registration: !github } },
      // her has not published its client metadata document yet, so registration comes first.
      suggested: { auth: "oauth", client: github ? "own" : "registration" },
      ...(CustomizeMock.logoOf(p.server as Record<string, unknown>) ? { icon: CustomizeMock.logoOf(p.server as Record<string, unknown>) } : {}),
    };
  }

  /** Signs in: the "browser" comes back after a moment. */
  mcpLogin(p: McpLoginParams): McpLoginResult {
    const login_id = `login-${Date.now()}`;
    const url = p.server?.url ?? (this.servers.find((s) => s.name === p.name)?.config.url as string | undefined) ?? "https://example.com";
    setTimeout(() => {
      this.signedIn.add(p.name);
      this.notify("mcp/changed", { name: p.name, login_id });
    }, 2500);
    return { authorize_url: `${new URL(url).origin}/authorize?client_id=her-demo&state=${login_id}`, login_id, client: p.client ?? "registration" };
  }

  mcpLogout(name: string) {
    this.signedIn.delete(name);
    this.notify("mcp/changed", { name });
    return {};
  }

  /** Connects once: a server that failed before fails once more, then works; a new one gets a few tools. */
  mcpCheck(name: string): McpCheckResult {
    const s = this.server(name);
    if (s.fails) {
      s.fails--;
      s.status = { ...s.status, state: "failed", source: "cache", checked_at: new Date().toISOString() };
    } else {
      if (s.tools.length === 0) s.tools = tools(["search", "Search what the server offers"], ["read", "Read one item in full"], ["list", "List items"]);
      s.status = { state: "ok", source: "cache", tools: s.tools.length, checked_at: new Date().toISOString(), elapsed_ms: 640 };
    }
    this.notify("mcp/changed", { name });
    return { server: this.view(s), tools: s.status.state === "ok" ? s.tools : [] };
  }
}

/** A logo as the kernel sends one: a `data:` URI. */
/** A key path's keys: `desktop.apps."com.apple.Safari"` is desktop, apps and com.apple.Safari. */
function segments(keyPath: string): string[] {
  return [...keyPath.matchAll(/"((?:[^"\\]|\\.)*)"|([^.]+)/g)].map((m) => (m[1] !== undefined ? m[1].replace(/\\(.)/g, "$1") : m[2]));
}

/** Sets a value under a table by its keys; null removes it. */
function setPath(table: Record<string, unknown>, keys: string[], value: unknown) {
  let at = table;
  for (const k of keys.slice(0, -1)) at = (at[k] ??= {}) as Record<string, unknown>;
  const last = keys[keys.length - 1];
  if (value === null) delete at[last];
  else at[last] = value;
}

function svgLogo(color: string, path: string): string {
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect width="24" height="24" rx="5" fill="${color}"/><path d="${path}" fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round"/></svg>`;
  return `data:image/svg+xml;base64,${btoa(svg)}`;
}

/** The service a configuration reaches, as the kernel computes it (mcp-design §13.10). */
function identityOf(config: Record<string, unknown>): string {
  if (typeof config.url === "string") {
    try {
      const u = new URL(config.url);
      return `${u.protocol}//${u.hostname}${u.port ? `:${u.port}` : ""}${u.pathname.replace(/\/+$/, "")}${u.search}`;
    } catch {
      return config.url;
    }
  }
  const args = Array.isArray(config.args) ? (config.args as string[]) : [];
  const runner = ["npx", "bunx", "pnpx", "uvx"].includes(String(config.command ?? "").split("/").pop() ?? "");
  return [String(config.command ?? ""), ...args.filter((a) => !(runner && (a === "-y" || a === "--yes")))].join(" ");
}
