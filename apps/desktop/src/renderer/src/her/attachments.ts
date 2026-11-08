import { attachedBlock } from "@hermesh/her-client";
import type { UserContent } from "@hermesh/her-protocol";

// What the composer attaches to a message (docs/her-server.md §9.1, Files):
// pictures go in the message itself; other files are
// uploaded into the assistant's folder on its machine first (HerUpload), and
// the message names where they landed ("Attached files:"). Both are read here
// by their paths on this Mac, which the preload tells (webUtils).

export type Attachment =
  /** `local`: the picture's file on this Mac, when it came from one (a paste has none). */
  | { kind: "image"; path: string; name: string; mime_type: string; data: string; local?: string }
  /** `path`: the file on this Mac. */
  | { kind: "file"; path: string; name: string };

/** What a picture may be to go in a message: what models read, and not larger than this. */
const pictures = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);
const PICTURE_MAX = 10 * 1024 * 1024;

async function base64(f: Blob): Promise<string> {
  const bytes = new Uint8Array(await f.arrayBuffer());
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}

/** Files from a picker, a drop or a paste: pictures read, other files by their path on this Mac (one without a path is left out). */
export async function fromFiles(files: File[]): Promise<Attachment[]> {
  const out: Attachment[] = [];
  for (const f of files) {
    const local = window.hermesh?.pathForFile(f) || undefined;
    if (pictures.has(f.type) && f.size <= PICTURE_MAX) out.push({ kind: "image", path: local ?? `${f.name}:${f.size}:${f.lastModified}`, name: f.name, mime_type: f.type, data: await base64(f), local });
    else if (local) out.push({ kind: "file", path: local, name: f.name });
  }
  return out;
}

/** Files from the system's dialog. */
export function pickFiles(): Promise<Attachment[]> {
  return new Promise((resolve) => {
    const input = document.createElement("input");
    input.type = "file";
    input.multiple = true;
    input.onchange = () => void fromFiles([...(input.files ?? [])]).then(resolve);
    input.click();
  });
}

/** What a paste attaches, or null when it is only text (and pastes as text). */
export function pastedAttachments(data: DataTransfer): Promise<Attachment[]> | null {
  return data.files.length > 0 ? fromFiles([...data.files]) : null;
}

/** The list with more added; one already there (the same path) is not added twice. */
export function addAttachments(list: Attachment[], more: Attachment[]): Attachment[] {
  const have = new Set(list.map((a) => a.path));
  return [...list, ...more.filter((a) => !have.has(a.path))];
}

/** A message's content: its words, its pictures, then a block naming the files uploaded for it (their paths on the machine). */
export function toInput(text: string, atts: Attachment[], uploaded: string[]): UserContent[] {
  const out: UserContent[] = [];
  if (text.trim() !== "") out.push({ type: "text", text: text.trim() });
  for (const a of atts) if (a.kind === "image") out.push({ type: "image", mime_type: a.mime_type, data: a.data });
  if (uploaded.length) out.push(attachedBlock(uploaded));
  return out;
}
