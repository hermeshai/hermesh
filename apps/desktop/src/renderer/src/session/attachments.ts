import { Code, ConnectError } from "@connectrpc/connect";
import { fileView } from "@hermesh/her-client";
import type { AttachmentRef } from "@hermesh/proto/session";

const IMAGE_EXT = /\.(png|jpe?g|gif|webp|bmp|ico|heic|tiff?|svg)$/i;
const MAX_KEPT = 100;

let snapshot: Record<string, string | null> = {};
const order: string[] = [];
const listeners = new Set<() => void>();
const inFlight = new Set<string>();
/** Why a picture could not be read: its machine away (it may come back), or not there. */
const failures = new Map<string, "offline" | "missing">();

export function attachmentFailure(sha256: string): "offline" | "missing" | undefined {
  return failures.get(sha256);
}

/** Forgets a picture that could not be read, so the next look asks for it again. */
export function retryAttachment(sha256: string) {
  if (snapshot[sha256] !== null) return;
  delete snapshot[sha256];
  failures.delete(sha256);
  notify();
}

function notify() {
  snapshot = { ...snapshot };
  for (const l of listeners) l();
}

/** A file sent with a prompt the app shows itself (a page, SVG, Markdown, code, text); the rest go to Quick Look. */
export function opensInApp(name: string): boolean {
  return ["page", "svg", "markdown", "code", "text"].includes(fileView(name));
}

/**
 * Whether an attached page refers to files beside it (a script, a style sheet, a picture by a relative address):
 * only the page was sent, so those are not there.
 */
export function refersToOwnFiles(html: string): boolean {
  const refs = [...html.matchAll(/\b(?:src|href)\s*=\s*["']([^"']*)["']/gi), ...html.matchAll(/url\(\s*["']?([^"')]+)["']?\s*\)/gi)].map((m) => m[1]!.trim());
  return refs.some((r) => r !== "" && !/^(?:[a-z][a-z0-9+.-]*:|\/\/|#)/i.test(r));
}

export function isImageAttachment(att: Pick<AttachmentRef, "name" | "mimeType">): boolean {
  return att.mimeType.toLowerCase().startsWith("image/") || IMAGE_EXT.test(att.name);
}

export function mimeForAttachment(att: Pick<AttachmentRef, "name" | "mimeType">): string {
  const m = att.mimeType.trim().toLowerCase();
  if (m.startsWith("image/")) return att.mimeType;
  if (/\.svg$/i.test(att.name)) return "image/svg+xml";
  if (/\.jpe?g$/i.test(att.name)) return "image/jpeg";
  if (/\.gif$/i.test(att.name)) return "image/gif";
  if (/\.webp$/i.test(att.name)) return "image/webp";
  return "image/png";
}

function storeUrl(sha256: string, url: string | null) {
  if (!sha256) return;
  const prev = snapshot[sha256];
  if (prev && prev !== url) URL.revokeObjectURL(prev);
  snapshot[sha256] = url;
  const idx = order.indexOf(sha256);
  if (idx >= 0) order.splice(idx, 1);
  order.push(sha256);
  while (order.length > MAX_KEPT) {
    const old = order.shift()!;
    const oldUrl = snapshot[old];
    if (oldUrl) URL.revokeObjectURL(oldUrl);
    delete snapshot[old];
  }
  notify();
}

export function keepAttachmentBlob(sha256: string, data: Uint8Array, mimeType: string, name = "") {
  if (!sha256 || !isImageAttachment({ name, mimeType })) return;
  const url = URL.createObjectURL(new Blob([data as Uint8Array<ArrayBuffer>], { type: mimeForAttachment({ name, mimeType }) }));
  storeUrl(sha256, url);
}

export function attachmentUrl(att: Pick<AttachmentRef, "blobSha256" | "name" | "mimeType">): string | undefined {
  if (!isImageAttachment(att)) return undefined;
  return snapshot[att.blobSha256] ?? undefined;
}

export function attachmentSnapshot(): Readonly<Record<string, string | null>> {
  return snapshot;
}

export function subscribeAttachments(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function loadAttachmentBlob(
  att: Pick<AttachmentRef, "blobSha256" | "name" | "mimeType">,
  download: (sha256: string) => Promise<{ data: Uint8Array; mimeType: string }>,
): Promise<void> {
  const sha = att.blobSha256;
  if (!sha || !isImageAttachment(att) || snapshot[sha] !== undefined || inFlight.has(sha)) {
    return Promise.resolve();
  }
  inFlight.add(sha);
  return download(sha)
    .then((res) => {
      if (res.data.length > 0) {
        const mime = mimeForAttachment({ name: att.name, mimeType: res.mimeType || att.mimeType });
        storeUrl(sha, URL.createObjectURL(new Blob([res.data as Uint8Array<ArrayBuffer>], { type: mime })));
      } else {
        storeUrl(sha, null);
      }
    })
    .catch((e: unknown) => {
      const code = ConnectError.from(e).code;
      failures.set(sha, code === Code.Unavailable || code === Code.DeadlineExceeded ? "offline" : "missing");
      storeUrl(sha, null);
    })
    .finally(() => {
      inFlight.delete(sha);
    });
}
