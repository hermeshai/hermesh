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
