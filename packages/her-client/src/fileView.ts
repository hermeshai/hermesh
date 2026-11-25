// How a file a session's reply points at opens (docs/session-files.md §3.2, §4), by its
// name: the same rule on the Mac and the iPhone (HermeshSessions/FileRefs.swift), with the
// cases both run in testdata/file-refs.json. What the file turns out to be (binary, too
// large) is the machine's to say once it is read.

/** What shows a file. */
export type FileView = "markdown" | "svg" | "image" | "pdf" | "page" | "media" | "office" | "other" | "text" | "code";

/** Where a click takes a file on the Mac: over the window, the file panel, or Quick Look. */
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

export function fileView(path: string): FileView {
  const name = baseName(path);
  for (const [re, view] of views) if (re.test(name)) return view;
  if (!/\.[^.]+$/.test(name.replace(/^\./, ""))) return codeNames.has(name) ? "code" : "text";
  return "code";
}

/**
 * A result to look at (a page, a picture, a PDF, Markdown) opens over the window; code and
 * text, and anything a reply points at a line of, in the file panel; what the app does not
 * show itself, in Quick Look (FinxWork's DD-161).
 */
export function fileOpens(path: string, line?: number): FileOpen {
  if (line !== undefined) return "panel";
  const view = fileView(path);
  if (view === "media" || view === "office" || view === "other") return "quicklook";
  return view === "text" || view === "code" ? "panel" : "preview";
}
