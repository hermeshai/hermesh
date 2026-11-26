// Pure helpers for terminal code blocks, command prompt stripping, secret masking,
// runnable command checks, and ANSI SGR / unified diff output parsing.

const SHELL_LANGS = new Set([
  "bash", "sh", "zsh", "shell", "shellscript", "console", "terminal",
  "fish", "ksh", "csh", "tcsh", "powershell", "pwsh", "ps1", "cmd", "bat", "batch",
]);

const CLI_HEADS = new Set([
  "git", "npm", "pnpm", "yarn", "bun", "npx", "bunx", "node", "deno",
  "python", "python3", "pip", "pip3", "uv", "poetry", "cargo", "rustc", "rustup",
  "go", "gofmt", "make", "cmake", "docker", "docker-compose", "kubectl", "helm",
  "terraform", "aws", "gcloud", "az", "gh", "brew", "apt", "apt-get", "dnf", "pacman",
  "systemctl", "journalctl", "launchctl", "curl", "wget", "ssh", "scp", "rsync",
  "tar", "unzip", "grep", "rg", "find", "fd", "jq", "sed", "awk", "chmod", "chown",
  "mkdir", "rm", "cp", "mv", "ls", "cd", "cat", "head", "tail", "echo", "printf",
  "export", "source", "which", "ps", "kill", "pkill", "lsof", "swift", "xcodebuild",
  "playwright", "vitest", "jest", "pytest", "hermesh", "hermeshd", "claude", "codex",
]);

const INTERACTIVE_TUIS = new Set([
  "vim", "nvim", "vi", "nano", "emacs", "htop", "top", "btop",
  "less", "more", "man", "tmux", "screen", "ssh", "sftp", "ftp", "telnet",
]);

const BARE_REPLS = new Set(["python", "python3", "node", "deno", "bun", "irb", "pry", "psql", "mysql", "sqlite3", "bash", "zsh", "sh", "fish"]);

export function shellLanguage(lang: string): boolean {
  return SHELL_LANGS.has(lang.trim().toLowerCase());
}

/**
 * Whether text holds characters that do not show but change what runs or reads:
 * controls (except tab/newline/CR), zero-width chars, and bidirectional overrides.
 */
export function hiddenChars(text: string): boolean {
  // eslint-disable-next-line no-control-regex
  return /[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F\u200B-\u200F\u202A-\u202E\u2060-\u2064\u2066-\u2069\uFEFF]/.test(text);
}

function firstCommandToken(line: string): string {
  let s = line.trim();
  s = s.replace(/^[$%]\s+/, "");
  // Strip leading env assignments (`FOO=bar BAZ=1 cmd`) and `sudo` / `env`.
  while (/^[A-Za-z_][A-Za-z0-9_]*=\S*\s+/.test(s)) {
    s = s.replace(/^[A-Za-z_][A-Za-z0-9_]*=\S*\s+/, "");
  }
  while (/^(?:sudo|env|command|time|nohup)\s+(?:-\S+\s+)*/.test(s)) {
    s = s.replace(/^(?:sudo|env|command|time|nohup)\s+(?:-\S+\s+)*/, "");
  }
  const tok = s.split(/[\s|;&()<>]/, 1)[0] ?? "";
  return tok.includes("/") ? (tok.split("/").pop() ?? tok) : tok;
}

/** Sniffs untagged or `text` fences that clearly contain CLI commands. */
export function sniffShellCommand(text: string): boolean {
  const trimmed = text.trim();
  if (!trimmed || /^[{\[<]/.test(trimmed)) return false;
  const lines = trimmed.split(/\r?\n/);
  if (lines.length > 12) return false;
  const nonEmpty = lines.map((l) => l.trim()).filter((l) => l !== "" && !l.startsWith("#"));
  if (nonEmpty.length === 0) return false;
  const first = nonEmpty[0]!;
  const hasPrompt = /^[$%]\s+[A-Za-z0-9_./~-]/.test(first);
  const head = firstCommandToken(first);
  if (!hasPrompt && !CLI_HEADS.has(head)) return false;
  // Reject multi-line English prose that happens to start with words like "make sure..." or "go to...".
  if (!hasPrompt && (head === "make" || head === "go" || head === "find" || head === "time" || head === "more")) {
    const rest = first.slice(head.length).trim();
    if (!rest || (/^(?:sure|to|the|a|an|it|this|that|your|our)\b/i.test(rest) && !/[-|>&;$=]/.test(first))) {
      return false;
    }
  }
  return true;
}

export function effectiveCodeLang(lang: string, text: string): { lang: string; shell: boolean; sniffed: boolean } {
  const norm = lang.trim();
  if (shellLanguage(norm)) return { lang: norm, shell: true, sniffed: false };
  if ((norm === "" || norm.toLowerCase() === "text" || norm.toLowerCase() === "plaintext") && sniffShellCommand(text)) {
    return { lang: "bash", shell: true, sniffed: true };
  }
  return { lang: norm, shell: false, sniffed: false };
}

/**
 * Strips leading `$ ` / `% ` shell prompts when copying or running a shell block.
 * If prompt lines are mixed with sample output lines (as in `console` blocks), keeps only the commands.
 */
export function cleanShellCommand(text: string): string {
  const rawLines = text.replace(/\r\n/g, "\n").split("\n");
  const promptRe = /^\s*[$%]\s+(.*)$/;
  const hasAnyPrompt = rawLines.some((l) => promptRe.test(l));
  if (!hasAnyPrompt) return text.trim();

  const out: string[] = [];
  let continuing = false;
  for (const line of rawLines) {
    if (continuing) {
      const trimmed = line.trimEnd();
      out.push(trimmed);
      continuing = trimmed.endsWith("\\");
      continue;
    }
    const m = promptRe.exec(line);
    if (m) {
      const cmd = m[1]!.trimEnd();
      out.push(cmd);
      continuing = cmd.endsWith("\\");
    }
  }
  return out.join("\n").trim();
}

const SECRET_PATTERNS: RegExp[] = [
  /\bsk-(?:ant-|proj-|live-|test-)?[A-Za-z0-9_-]{16,}\b/g,
  /\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}\b/g,
  /\bgithub_pat_[A-Za-z0-9_]{22,}\b/g,
  /\bAKIA[0-9A-Z]{16}\b/g,
  /\bxox[baprs]-[A-Za-z0-9-]{16,}\b/g,
  /\bAIza[0-9A-Za-z_-]{32,}\b/g,
  /(?<=Bearer\s+)[A-Za-z0-9._~+/=-]{20,}/g,
  /(?<=\b[A-Z0-9_]*(?:API_KEY|SECRET_KEY|ACCESS_TOKEN|AUTH_TOKEN|PRIVATE_KEY|CLIENT_SECRET)\s*=\s*["']?)[A-Za-z0-9._~+/=-]{12,}/g,
];

export type SecretSpan = { start: number; end: number };

export function findSecretSpans(text: string): SecretSpan[] {
  const spans: SecretSpan[] = [];
  for (const base of SECRET_PATTERNS) {
    const re = new RegExp(base.source, base.flags);
    for (const m of text.matchAll(re)) {
      if (m.index === undefined || m[0].length === 0) continue;
      spans.push({ start: m.index, end: m.index + m[0].length });
    }
  }
  if (spans.length <= 1) return spans;
  spans.sort((a, b) => a.start - b.start || b.end - a.end);
  const merged: SecretSpan[] = [spans[0]!];
  for (let i = 1; i < spans.length; i++) {
    const prev = merged[merged.length - 1]!;
    const cur = spans[i]!;
    if (cur.start <= prev.end) {
      prev.end = Math.max(prev.end, cur.end);
    } else {
      merged.push(cur);
    }
  }
  return merged;
}

export function maskSecrets(text: string): string {
  const spans = findSecretSpans(text);
  if (spans.length === 0) return text;
  let out = "";
  let pos = 0;
  for (const s of spans) {
    out += text.slice(pos, s.start);
    const raw = text.slice(s.start, s.end);
    const keep = Math.min(4, Math.max(2, Math.floor(raw.length / 5)));
    out += raw.slice(0, keep) + "••••••••";
    pos = s.end;
  }
  out += text.slice(pos);
  return out;
}

const DESTRUCTIVE_PATTERNS: RegExp[] = [
  /\brm\s+(?:-[A-Za-z]*[rf][A-Za-z]*\b|--recursive\b|--force\b)/,
  /(?:^|[;&|\n]\s*)sudo\s+/,
  /\bgit\s+(?:reset\s+--hard\b|clean\s+-[A-Za-z]*f|push\s+(?:--force|-f)\b|checkout\s+--\s*\.)/,
  /\b(?:mkfs(?:\.\w+)?|fdisk|dd\s+if=|diskutil\s+erase\w*)\b/,
  /\b(?:curl|wget)\b[^\n|]*\|\s*(?:sudo\s+)?(?:ba|z|k)?sh\b/,
];

export type RunnableShellPlan = {
  command: string;
  destructive: boolean;
};

export function isDestructiveShell(command: string): boolean {
  const activeLines = command
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "" && !l.startsWith("#"))
    .join("\n");
  return DESTRUCTIVE_PATTERNS.some((re) => re.test(activeLines));
}

/**
 * Evaluates whether a shell block (single-line or multi-line script, including shebangs/comments)
 * can run non-interactively in the session's workspace, and whether it warrants an inline confirmation.
 */
export function inspectRunnableShell(lang: string, text: string): RunnableShellPlan | null {
  const eff = effectiveCodeLang(lang, text);
  if (!eff.shell) return null;
  if (hiddenChars(text)) return null;
  const clean = cleanShellCommand(text);
  if (!clean || clean.length > 4000) return null;

  const execLines = clean
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "" && !l.startsWith("#"));
  if (execLines.length === 0) return null;

  for (const line of execLines) {
    const head = firstCommandToken(line);
    if (!head) continue;
    if (INTERACTIVE_TUIS.has(head)) return null;
    const rest = line.replace(/^[$%]\s+/, "").trim().slice(head.length).trim();
    if (BARE_REPLS.has(head) && rest === "") return null;
  }

  return {
    command: clean,
    destructive: isDestructiveShell(clean),
  };
}

export function runnableShellCommand(lang: string, text: string): string | null {
  return inspectRunnableShell(lang, text)?.command ?? null;
}

// --- Lightweight ANSI SGR + Unified Diff Output Parser ---

export type AnsiSpanStyle = {
  fgClass?: string;
  fgColor?: string;
  bold?: boolean;
  dim?: boolean;
  italic?: boolean;
  underline?: boolean;
};

export type TerminalSpan = {
  text: string;
  style?: AnsiSpanStyle;
};

export type TerminalLine = {
  diff?: "add" | "del" | "hunk" | "meta";
  spans: TerminalSpan[];
};

const FG_CLASSES: Record<number, string> = {
  30: "ansi-gray",
  31: "ansi-red",
  32: "ansi-green",
  33: "ansi-yellow",
  34: "ansi-blue",
  35: "ansi-magenta",
  36: "ansi-cyan",
  37: "ansi-fg",
  90: "ansi-gray",
  91: "ansi-red",
  92: "ansi-green",
  93: "ansi-yellow",
  94: "ansi-blue",
  95: "ansi-magenta",
  96: "ansi-cyan",
  97: "ansi-fg",
};

function xterm256ToCss(n: number): string | undefined {
  if (n < 0 || n > 255) return undefined;
  if (n < 8) return FG_CLASSES[30 + n];
  if (n < 16) return FG_CLASSES[90 + (n - 8)];
  if (n >= 232) {
    const v = 8 + (n - 232) * 10;
    return `rgb(${v},${v},${v})`;
  }
  const idx = n - 16;
  const levels = [0, 95, 135, 175, 215, 255];
  const r = levels[Math.floor(idx / 36)]!;
  const g = levels[Math.floor((idx % 36) / 6)]!;
  const b = levels[idx % 6]!;
  return `rgb(${r},${g},${b})`;
}

function hasStyle(s: AnsiSpanStyle): boolean {
  return Boolean(s.fgClass || s.fgColor || s.bold || s.dim || s.italic || s.underline);
}

function visibleLen(s: string): number {
  // eslint-disable-next-line no-control-regex
  return s.replace(/\x1b\[[0-9;]*m/g, "").length;
}

function sliceAfterVisible(s: string, skip: number): string {
  if (skip <= 0) return s;
  let seen = 0;
  let i = 0;
  while (i < s.length && seen < skip) {
    if (s[i] === "\x1b" && s[i + 1] === "[") {
      const m = s.indexOf("m", i + 2);
      if (m !== -1) {
        i = m + 1;
        continue;
      }
    }
    seen++;
    i++;
  }
  return s.slice(i);
}

function applyCarriageReturns(line: string): string {
  if (!line.includes("\r")) return line;
  const parts = line.split("\r");
  let acc = "";
  for (const p of parts) {
    if (p === "") continue;
    const pVis = visibleLen(p);
    acc = pVis >= visibleLen(acc) ? p : p + sliceAfterVisible(acc, pVis);
  }
  return acc;
}

function applySgr(codes: number[], cur: AnsiSpanStyle): AnsiSpanStyle {
  let next = { ...cur };
  if (codes.length === 0) return {};
  for (let i = 0; i < codes.length; i++) {
    const c = codes[i]!;
    if (c === 0) next = {};
    else if (c === 1) next.bold = true;
    else if (c === 2) next.dim = true;
    else if (c === 3) next.italic = true;
    else if (c === 4) next.underline = true;
    else if (c === 22) {
      delete next.bold;
      delete next.dim;
    } else if (c === 23) delete next.italic;
    else if (c === 24) delete next.underline;
    else if (c === 39) {
      delete next.fgClass;
      delete next.fgColor;
    } else if (FG_CLASSES[c]) {
      next.fgClass = FG_CLASSES[c];
      delete next.fgColor;
    } else if (c === 38) {
      const mode = codes[i + 1];
      if (mode === 5 && codes[i + 2] !== undefined) {
        const mapped = xterm256ToCss(codes[i + 2]!);
        if (mapped?.startsWith("ansi-")) {
          next.fgClass = mapped;
          delete next.fgColor;
        } else if (mapped) {
          next.fgColor = mapped;
          delete next.fgClass;
        }
        i += 2;
      } else if (mode === 2 && codes[i + 4] !== undefined) {
        const r = Math.min(255, Math.max(0, codes[i + 2]!));
        const g = Math.min(255, Math.max(0, codes[i + 3]!));
        const b = Math.min(255, Math.max(0, codes[i + 4]!));
        next.fgColor = `rgb(${r},${g},${b})`;
        delete next.fgClass;
        i += 4;
      }
    }
  }
  return next;
}

function looksLikeUnifiedDiff(lines: string[]): boolean {
  let markers = 0;
  let addOrDel = 0;
  for (let i = 0; i < Math.min(lines.length, 80); i++) {
    const l = lines[i]!;
    if (l.startsWith("@@ ") || l.startsWith("diff --git ") || l.startsWith("--- ") || l.startsWith("+++ ")) markers++;
    else if ((l.startsWith("+") && !l.startsWith("+++")) || (l.startsWith("-") && !l.startsWith("---"))) addOrDel++;
  }
  return markers > 0 || (addOrDel >= 2 && lines.every((l) => l === "" || /^[ +\-@\\]/.test(l)));
}

/** Strips all terminal escape sequences (for clipboard copy). */
export function stripTerminalEscapes(raw: string): string {
  // eslint-disable-next-line no-control-regex
  const cleaned = raw.replace(/\r\n/g, "\n").replace(/\x1b\][\s\S]*?(?:\x07|\x1b\\)|\x1b\[[0-?]*[ -/]*[@-~]|\x1b[@-_]/g, "");
  return cleaned
    .split("\n")
    .map(applyCarriageReturns)
    .join("\n");
}

/** Parses raw command/diff output into styled lines (ANSI SGR + unified diff fallback). */
export function parseTerminalLines(raw: string): TerminalLine[] {
  const normalized = raw.replace(/\r\n/g, "\n");
  // Remove OSC / non-SGR CSI sequences while preserving CSI SGR `\x1b[...m`.
  // eslint-disable-next-line no-control-regex
  const csiOnly = normalized.replace(/\x1b\][\s\S]*?(?:\x07|\x1b\\)|\x1b\[[0-?]*[ -/]*[@-ln-~]/g, "");
  const rawLines = csiOnly.split("\n").map(applyCarriageReturns);
  const hasAnsi = csiOnly.includes("\x1b[");
  const isDiff = !hasAnsi && looksLikeUnifiedDiff(rawLines);

  let style: AnsiSpanStyle = {};
  // eslint-disable-next-line no-control-regex
  const sgrRe = /\x1b\[([0-9;]*)m/g;

  return rawLines.map((line) => {
    if (!hasAnsi) {
      let diff: TerminalLine["diff"];
      if (isDiff) {
        if (line.startsWith("@@ ")) diff = "hunk";
        else if (line.startsWith("+++ ") || line.startsWith("--- ") || line.startsWith("diff --git ")) diff = "meta";
        else if (line.startsWith("+")) diff = "add";
        else if (line.startsWith("-")) diff = "del";
      }
      return { diff, spans: [{ text: line }] };
    }
    const spans: TerminalSpan[] = [];
    let last = 0;
    sgrRe.lastIndex = 0;
    for (const m of line.matchAll(sgrRe)) {
      const idx = m.index ?? 0;
      if (idx > last) {
        const chunk = line.slice(last, idx);
        spans.push(hasStyle(style) ? { text: chunk, style: { ...style } } : { text: chunk });
      }
      const codes = m[1] === "" ? [0] : m[1]!.split(";").map((s) => (s === "" ? 0 : Number.parseInt(s, 10) || 0));
      style = applySgr(codes, style);
      last = idx + m[0].length;
    }
    if (last < line.length || spans.length === 0) {
      const chunk = line.slice(last);
      spans.push(hasStyle(style) ? { text: chunk, style: { ...style } } : { text: chunk });
    }
    return { spans };
  });
}
