/** The coding agents a grant can name; matches machine.AgentNames in hermeshd. */
export const codingAgentIds: readonly string[] = [
  "claude",
  "codex",
  "gemini",
  "antigravity",
  "cursor",
  "copilot",
  "opencode",
  "qwen",
  "goose",
  "auggie",
  "kimi",
  "kiro",
  "qoder",
  "deepseek",
  "codebuddy",
  "grok",
];

const titles = new Map([
  ["claude", "Claude Code"],
  ["codex", "Codex"],
  ["gemini", "Gemini CLI"],
  ["antigravity", "Antigravity"],
  ["cursor", "Cursor"],
  ["copilot", "Copilot CLI"],
  ["opencode", "OpenCode"],
  ["qwen", "Qwen Code"],
  ["goose", "Goose"],
  ["auggie", "Augment"],
  ["kimi", "Kimi"],
  ["kiro", "Kiro"],
  ["qoder", "Qoder"],
  ["deepseek", "DeepSeek Harness"],
  ["codebuddy", "CodeBuddy Code"],
  ["grok", "Grok"],
]);

const shortTitles = new Map([
  ["claude", "Claude"],
  ["copilot", "Copilot"],
  ["codebuddy", "CodeBuddy"],
  ["deepseek", "DeepSeek"],
  ["gemini", "Gemini"],
]);

/** A coding agent's name, by the id a grant names; an unknown id as it is. */
export function codingAgentTitle(name: string): string {
  return titles.get(name) ?? name;
}

/** The short name of a coding agent, for dense lines. */
export function codingAgentShort(name: string): string {
  return shortTitles.get(name) ?? codingAgentTitle(name);
}
