// What a reply points at, read from its Markdown (content-actions-design §3): files, by a
// link to their path or by a path in inline code, and web pages. Only what is written
// plainly as one is taken: missing one is better than taking a branch name for a file.

/** A file or folder as a reply names it: the path as written, and the lines it points to. */
export type FileRef = { path: string; line?: number; endLine?: number };

/** Lines at a path's end: `:42`, `:42:7` (the column is not used), `:42-60`, `#L42`, `#L42-L60`. */
const lines = /(?::(\d+)(?::\d+|-(\d+))?|#L(\d+)(?:-L?(\d+))?)$/;
/** A scheme before a colon: `mailto:`, `javascript:`; a path's own colons come after a slash. */
const scheme = /^[A-Za-z][A-Za-z0-9+.-]*:/;
const drive = /^[A-Za-z]:[\\/]/;

function withLines(text: string): FileRef {
  const m = lines.exec(text);
  if (!m) return { path: text };
  const line = Number(m[1] ?? m[3]);
  const end = Number(m[2] ?? m[4]);
  if (!(line >= 1)) return { path: text };
  return { path: text.slice(0, m.index), line, ...(end > line && { endLine: end }) };
}

function decoded(path: string): string {
  try {
    return decodeURIComponent(path);
  } catch {
    return path;
  }
}

/**
 * A link's target as a file: a path (relative, absolute, `~/`, a `file://` address), with the
 * lines it points to. Null for a web address, another scheme (`mailto:`), an anchor, nothing.
 * `windows`: a drive's path (`C:\`) is a path, not a scheme.
 */
export function fileOfLink(href: string, windows = false): FileRef | null {
  const target = href.trim();
  if (target === "" || target.startsWith("#") || webOf(target)) return null;
  if (/^file:/i.test(target)) {
    try {
      const u = new URL(target);
      if (u.host !== "" && u.host !== "localhost") return null;
      const path = decoded(u.pathname);
      const ref = withLines(path + u.hash);
      return { ...ref, path: windows && /^\/[A-Za-z]:\//.test(ref.path) ? ref.path.slice(1) : ref.path };
    } catch {
      return null;
    }
  }
  const ref = withLines(target);
  // "tel:123" has a scheme; "README.md:12" and "Makefile:3" are files with a line.
  if (scheme.test(target) && !(windows && drive.test(target))) {
    const before = target.slice(0, target.indexOf(":"));
    if (!(ref.line !== undefined && ref.path === before && fileName(before))) return null;
  }
  // A fragment that is not lines (`#usage`) is a place in the file, which opens at its start.
  const path = ref.line === undefined ? ref.path.replace(/#.*$/, "") : ref.path;
  if (path === "") return null;
  return { ...ref, path: decoded(path) };
}

/** Files that have no extension and are still plainly files. */
const bareNames = new Set(["Makefile", "Dockerfile", "LICENSE", "Gemfile", "Rakefile", "Procfile", "Justfile", "Brewfile", "Vagrantfile", "CODEOWNERS", "Containerfile", "Jenkinsfile"]);

/** A name with an extension, or a well-known one without. */
const fileName = (name: string) => /\.[A-Za-z0-9]{1,8}$/.test(name) || bareNames.has(name);

/**
 * Inline code that names a file or folder plainly enough to look it up (§3.3): one line, no
 * shell or glob characters, a slash or an extension (or a well-known name like `Makefile`),
 * spaces only in an explicit path. Whether it exists is for the caller to find out; only then
 * is it a file.
 */
export function fileOfCode(text: string, windows = false): FileRef | null {
  const code = text.trim();
  if (code === "" || code.length > 300 || code.includes("\n") || code.includes("://")) return null;
  if (/[*?<>|"`]/.test(code) || /^[-$@#!]/.test(code)) return null;
  const ref = withLines(code);
  const path = ref.path;
  if (!/[\p{L}\p{N}]/u.test(path)) return null;
  if (/\s/.test(path) && !/^(\/|~\/|\.\.?\/)/.test(path) && !(windows && drive.test(path))) return null;
  const slash = path.includes("/") || (windows && path.includes("\\"));
  if (!slash && !fileName(path)) return null;
  if (scheme.test(path) && !(windows && drive.test(path))) return null;
  return ref;
}

/** An http or https address, or null for anything else. */
export function webOf(href: string): URL | null {
  if (!/^https?:\/\//i.test(href.trim())) return null;
  try {
    return new URL(href.trim());
  } catch {
    return null;
  }
}

/** Inline code that is a web address and nothing else, with no placeholders (`{id}`, `<host>`) in it. */
export function webOfCode(text: string): URL | null {
  const code = text.trim();
  return /[\s{}<>]/.test(code) ? null : webOf(code);
}

/** Whether a host is this machine: `localhost`, a `.localhost` name, a loopback address. */
export function loopback(host: string): boolean {
  const h = host.toLowerCase().replace(/^\[|\]$/g, "");
  return h === "localhost" || h.endsWith(".localhost") || h === "::1" || h === "0.0.0.0" || /^127\.\d+\.\d+\.\d+$/.test(h);
}

/**
 * A bare address as a reply shows it: without the scheme, `www.` and a last `/`, cut in the
 * middle past `max` characters. The host is the one the address really goes to.
 */
export function shortURL(url: URL, max = 60): string {
  let s = url.host.replace(/^www\./, "") + (url.pathname === "/" ? "" : url.pathname) + url.search + url.hash;
  s = s.replace(/\/$/, "");
  if (s.length <= max) return s;
  const head = Math.ceil((max - 1) / 2);
  return `${s.slice(0, head)}…${s.slice(s.length - (max - 1 - head))}`;
}

const shells = new Set(["bash", "sh", "zsh", "shell", "shellscript", "console", "terminal", "fish", "ksh", "csh", "tcsh", "powershell", "pwsh", "ps1", "cmd", "bat", "batch"]);

/** Whether a fence's language is a shell's: a block of commands to run, not code to read. */
export function shellLanguage(lang: string): boolean {
  return shells.has(lang.toLowerCase());
}

/**
 * Whether text holds characters that do not show but change what runs or reads: controls
 * (but tab and newline), zero-width ones and bidirectional overrides.
 */
export function hiddenChars(text: string): boolean {
  return /[\u0000-\u0008\u000B-\u001F\u007F\u200B-\u200F\u202A-\u202E\u2060-\u2064\u2066-\u2069\uFEFF]/.test(text);
}
