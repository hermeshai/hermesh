import { fileOfLink } from "./refs.ts";

// How a file a session's reply points at opens (docs/session-files.md §3.2, §4), by its
// name: the same rule on the Mac and the iPhone (HermeshSessions/FileRefs.swift), with the
// cases both run in testdata/file-refs.json. What the file turns out to be (binary, too
// large) is the machine's to say once it is read.

/** What shows a file. */
export type FileView = "markdown" | "svg" | "image" | "pdf" | "page" | "media" | "office" | "other" | "text" | "code";

/** Where a click takes a file on the Mac: a tab of the side panel (a result to look at, or code at its lines), or Quick Look. */
export type FileOpen = "preview" | "panel" | "quicklook";

const views: [RegExp, FileView][] = [
  [/\.(md|markdown)$/i, "markdown"],
  [/\.svg$/i, "svg"],
  [/\.(png|jpe?g|gif|webp|bmp|ico|heic|tiff?)$/i, "image"],
  [/\.pdf$/i, "pdf"],
  [/\.html?$/i, "page"],
  [/\.(mp3|m4a|wav|aac|flac|mp4|mov|m4v|avi|mkv|webm)$/i, "media"],
  [/\.(docx?|xlsx?|xlsm|pptx?|key|numbers|pages|rtf|odt|ods|odp|epub)$/i, "office"],
  [/\.(zip|rar|7z|tar|gz|tgz|dmg|pkg|app|ipa|apk|exe|bin)$/i, "other"],
  [/\.(txt|text|log|csv|tsv)$/i, "text"],
];

/** Names with no extension that are code all the same. */
const codeNames = new Set(["Makefile", "Dockerfile", "Gemfile", "Rakefile", "Procfile", "Justfile", "Brewfile", "Vagrantfile", "Containerfile", "Jenkinsfile"]);

const baseName = (path: string) => path.replace(/[\\/]+$/, "").split(/[\\/]/).pop() ?? "";
const htmlExt = /\.html?$/i;

/**
 * The label for an HTML page tab: the page's document `<title>` when set, otherwise its filename
 * without `.html`/`.htm` (since the globe glyph already marks it as a page).
 */
export function pageTabTitle(path: string, pageTitle?: string | null): string {
  const title = (pageTitle ?? "").replace(/\s+/g, " ").trim();
  if (title && !/^(?:hermesh-file|about):/i.test(title)) return title;
  const name = baseName(path);
  const stem = name.replace(htmlExt, "");
  return stem || name;
}

export function fileView(path: string): FileView {
  const name = baseName(path);
  for (const [re, view] of views) if (re.test(name)) return view;
  if (!/\.[^.]+$/.test(name.replace(/^\./, ""))) return codeNames.has(name) ? "code" : "text";
  return "code";
}

/**
 * A result to look at (a page, a picture, a PDF, Markdown), or code and text and anything a reply
 * points at a line of; what the app does not show itself goes to Quick Look (FinxWork's DD-161).
 * On the Mac both of the first open in a side panel tab (a picture in the Lightbox).
 */
export function fileOpens(path: string, line?: number): FileOpen {
  if (line !== undefined) return "panel";
  const view = fileView(path);
  if (view === "media" || view === "office" || view === "other") return "quicklook";
  return view === "text" || view === "code" ? "panel" : "preview";
}

const rasterImage = /\.(png|jpe?g|gif|webp)$/i;

/** Whether a session file path (without a line reference) is a raster image rendered inline and opened in the Lightbox. */
export function isSessionImage(path: string, line?: number): boolean {
  return line === undefined && rasterImage.test(baseName(path));
}

/**
 * Extracts a local/session raster image path from a Markdown image or link target (`![alt](href)` / `[label](href)`),
 * recognizing Windows drive paths regardless of the viewer's OS and rejecting `http(s):` / `data:` URLs.
 */
export function sessionImageRef(href: string): string | null {
  const ref = fileOfLink(href, true);
  return ref && isSessionImage(ref.path, ref.line) ? ref.path : null;
}

/** Normalizes a reply's image path so `![alt](/a/b.png)` and `[b.png](file:///a/b.png)` match. */
export function normalizeImagePath(path: string): string {
  let p = path.trim().replace(/\\/g, "/");
  while (p.startsWith("./")) p = p.slice(2);
  p = p.replace(/\/+/g, "/").replace(/(\/\.)+(?=\/|$)/g, "");
  return p.length > 1 ? p.replace(/\/$/, "") : p;
}

const isAbsImagePath = (p: string) => p.startsWith("/") || p.startsWith("~/") || /^[A-Za-z]:\//.test(p);

/** Whether two image references in the same reply point to the same file (for deduplicating `![alt](p)` + `[name](p)`). */
export function sameImagePath(a: string, b: string): boolean {
  const na = normalizeImagePath(a);
  const nb = normalizeImagePath(b);
  if (na === "" || nb === "") return false;
  if (na === nb) return true;
  const aa = isAbsImagePath(na);
  const ab = isAbsImagePath(nb);
  if (aa === ab) return false;
  return aa ? na.endsWith(`/${nb}`) : nb.endsWith(`/${na}`);
}

/** Resolves a relative path inside a Markdown document at `baseDir` ("" for chat replies). */
export function resolveDocPath(baseDir: string, path: string): string {
  const base = baseDir.trim().replace(/\\/g, "/").replace(/\/+$/, "");
  const p = path.trim().replace(/\\/g, "/");
  if (!base || !p || isAbsImagePath(p)) return path;
  const rootMatch = /^(\/|~\/|[A-Za-z]:\/)/.exec(base);
  const root = rootMatch ? rootMatch[1]! : "";
  const segs = [...base.slice(root.length).split("/"), ...p.split("/")];
  const out: string[] = [];
  for (const s of segs) {
    if (!s || s === ".") continue;
    if (s === "..") {
      if (out.length > 0 && out[out.length - 1] !== "..") out.pop();
      else if (!root) out.push("..");
      continue;
    }
    out.push(s);
  }
  return root + out.join("/");
}

// MARK: File tree & folder browser visual semantics (Scheme A: Seti + Linguist + Material role priority)

export type FileGlyph =
  | "folder"
  | "folderTest"
  | "go"
  | "test"
  | "swift"
  | "code"
  | "braces"
  | "sliders"
  | "markdown"
  | "page"
  | "terminal"
  | "cube"
  | "lock"
  | "git"
  | "image"
  | "pdf"
  | "doc";

export type FileTone =
  | "folder"
  | "folderTest"
  | "cyan"
  | "blue"
  | "orange"
  | "green"
  | "amber"
  | "purple"
  | "doc"
  | "red"
  | "slate";

export type FileAppearance = {
  glyph: FileGlyph;
  tone: FileTone;
  dimmed: boolean;
};

export type FileNameParts = {
  stem: string;
  sub: string;
  ext: string;
};

const lockFiles = new Set([
  "go.sum",
  "pnpm-lock.yaml",
  "package-lock.json",
  "yarn.lock",
  "cargo.lock",
  "podfile.lock",
  "package.resolved",
  "gemfile.lock",
  "composer.lock",
  "poetry.lock",
  "uv.lock",
  "bun.lock",
  "bun.lockb",
]);

const buildShellFiles = new Set([
  "makefile",
  "justfile",
  "rakefile",
  "procfile",
  "brewfile",
  "gemfile",
  "vagrantfile",
  "jenkinsfile",
]);

const gitIgnoreFiles = new Set([
  ".gitignore",
  ".gitattributes",
  ".gitmodules",
  ".dockerignore",
  ".npmignore",
  ".prettierignore",
  ".eslintignore",
]);

const licenseFiles = new Set(["license", "license.md", "license.txt", "copying"]);
const testDirPattern = /^(?:testdata|tests?|e2e|__tests__|specs?)$/i;
const testDirSuffix = /[a-z0-9](?:UITests|Tests)$/;
const testFilePattern = /(?:_(?:[a-z0-9]+_)?test\.[a-z0-9]+|\.(?:[a-z0-9]+\.)?(?:test|spec)\.[a-z0-9]+)$/i;
const pascalTestFilePattern = /(?:[a-z0-9](?:UITests?|Tests?)|^(?:UITests?|Tests?))\.(?:swift|kt|java)$/;
const pyTestPrefixPattern = /^test_[^.]+\.py$/i;
const splitTestPattern = /^(.+?)((?:\.[a-z0-9]+)?\.(?:test|spec)|_(?:[a-z0-9]+_)?test)(\.[A-Za-z0-9]+)$/i;
const splitPascalTestPattern = /^(.+[a-z0-9])(UITests?|Tests?)(\.(?:swift|kt|java))$/;

/**
 * Semantic icon glyph, OKLCH palette tone, and noise-dimming flag for a directory or file entry.
 * Priority: directory role -> exact filename / lockfile -> compound test role -> extension.
 */
export function fileAppearance(pathOrName: string, dir = false): FileAppearance {
  const name = baseName(pathOrName) || pathOrName;
  const lower = name.toLowerCase();

  if (dir) {
    if (lower.startsWith(".")) return { glyph: "folder", tone: "slate", dimmed: true };
    if (testDirPattern.test(lower) || testDirSuffix.test(name)) {
      return { glyph: "folderTest", tone: "folderTest", dimmed: false };
    }
    return { glyph: "folder", tone: "folder", dimmed: false };
  }

  if (lockFiles.has(lower)) return { glyph: "lock", tone: "slate", dimmed: true };
  if (lower === "go.mod" || lower === "go.work") return { glyph: "cube", tone: "cyan", dimmed: false };
  if (lower === "cargo.toml") return { glyph: "cube", tone: "orange", dimmed: false };
  if (lower === "dockerfile" || lower === "containerfile" || lower.startsWith("dockerfile.") || lower.endsWith(".dockerfile")) {
    return { glyph: "cube", tone: "cyan", dimmed: false };
  }
  if (buildShellFiles.has(lower)) return { glyph: "terminal", tone: "green", dimmed: false };
  if (gitIgnoreFiles.has(lower)) return { glyph: "git", tone: "red", dimmed: true };
  if (licenseFiles.has(lower)) return { glyph: "doc", tone: "slate", dimmed: true };
  if (lower === ".env" || lower.startsWith(".env.")) return { glyph: "sliders", tone: "amber", dimmed: true };

  if (testFilePattern.test(name) || pascalTestFilePattern.test(name) || pyTestPrefixPattern.test(name)) {
    return { glyph: "test", tone: "green", dimmed: false };
  }

  const dot = lower.lastIndexOf(".");
  const ext = dot > 0 ? lower.slice(dot + 1) : "";
  const dimmed = lower.startsWith(".");

  switch (ext) {
    case "go":
      return { glyph: "go", tone: "cyan", dimmed };
    case "proto":
    case "sql":
    case "graphql":
    case "gql":
      return { glyph: "cube", tone: "cyan", dimmed };
    case "swift":
      return { glyph: "swift", tone: "orange", dimmed };
    case "rs":
    case "c":
    case "h":
    case "cpp":
    case "cc":
    case "cxx":
    case "hpp":
    case "zig":
      return { glyph: "code", tone: "orange", dimmed };
    case "ts":
    case "tsx":
    case "mts":
    case "cts":
    case "py":
    case "pyi":
    case "java":
    case "kt":
    case "kts":
    case "scala":
    case "cs":
    case "dart":
    case "vue":
    case "svelte":
      return { glyph: "code", tone: "blue", dimmed };
    case "css":
    case "scss":
    case "sass":
    case "less":
      return { glyph: "braces", tone: "blue", dimmed };
    case "js":
    case "mjs":
    case "cjs":
    case "jsx":
    case "json":
    case "jsonc":
    case "json5":
      return { glyph: "braces", tone: "amber", dimmed };
    case "yaml":
    case "yml":
    case "toml":
    case "ini":
    case "conf":
    case "cfg":
    case "plist":
    case "xcconfig":
    case "entitlements":
    case "xml":
      return { glyph: "sliders", tone: "purple", dimmed };
    case "md":
    case "markdown":
    case "mdx":
    case "rst":
      return { glyph: "markdown", tone: "doc", dimmed };
    case "html":
    case "htm":
      return { glyph: "page", tone: "orange", dimmed };
    case "sh":
    case "bash":
    case "zsh":
    case "fish":
    case "ps1":
    case "bat":
    case "cmd":
      return { glyph: "terminal", tone: "green", dimmed };
    case "svg":
    case "png":
    case "jpg":
    case "jpeg":
    case "gif":
    case "webp":
    case "ico":
    case "heic":
    case "bmp":
    case "tiff":
    case "tif":
    case "avif":
      return { glyph: "image", tone: "purple", dimmed };
    case "pdf":
      return { glyph: "pdf", tone: "red", dimmed };
    case "rb":
    case "erb":
      return { glyph: "code", tone: "red", dimmed };
    default:
      return { glyph: dimmed ? "sliders" : "doc", tone: "slate", dimmed };
  }
}

/**
 * Splits a filename into primary stem, secondary test/role suffix, and muted extension
 * so `stem + sub + ext === name` always holds.
 */
export function splitFileName(name: string, dir = false): FileNameParts {
  if (dir) return { stem: name, sub: "", ext: "" };
  const m = splitTestPattern.exec(name) ?? splitPascalTestPattern.exec(name);
  if (m && m[1]) return { stem: m[1], sub: m[2]!, ext: m[3]! };
  const dot = name.lastIndexOf(".");
  if (dot > 0 && dot < name.length - 1) {
    return { stem: name.slice(0, dot), sub: "", ext: name.slice(dot) };
  }
  return { stem: name, sub: "", ext: "" };
}
