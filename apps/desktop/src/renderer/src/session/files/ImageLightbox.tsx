import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { sameImagePath } from "@hermesh/her-client";
import type { AttachmentRef } from "@hermesh/proto/session";
import type { MenuItem } from "../../../../shared/bridge";
import { Icon } from "../../icons";
import { machineName } from "../../Sidebar";
import { useStore, type Store } from "../../store";
import { sessions } from "../../api";
import { attachmentFailure, attachmentSnapshot, loadAttachmentBlob, retryAttachment, subscribeAttachments } from "../attachments";
import { baseName, sizeText, type FilesHome, type SessionFile } from "./files";
import { decodeDimensions, requestSessionImage, useSessionImage, type ImageState, type ReplyImageItem } from "./images";

/** A picture already in the app (Her's conversation): where it is, its name, and how to keep a copy if it can be. */
export type PictureItem = { src: string; name: string; save?: () => void };

/**
 * What the Lightbox shows, every picture of the app: a reply's pictures (session files, with their menu); the
 * pictures the owner sent with a prompt (attachments, kept by the machine as blobs; nothing to save or find, so no
 * menu); pictures already in the app (Her's conversation; a menu with Save when it can).
 */
export type ShownLightbox =
  | { kind: "files"; home: FilesHome; run: string; items: ReplyImageItem[]; index: number; seq: number }
  | { kind: "attachments"; session: string; machine: string; items: AttachmentRef[]; index: number; seq: number }
  | { kind: "pictures"; items: PictureItem[]; index: number; seq: number };

let current: ShownLightbox | null = null;
let seq = 0;
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());

export function useShownLightbox(): ShownLightbox | null {
  return useSyncExternalStore(
    (l) => (listeners.add(l), () => listeners.delete(l)),
    () => current,
  );
}

export function closeSessionLightbox() {
  if (!current) return;
  current = null;
  emit();
}

export function openSessionLightbox(home: FilesHome, run: string, items: ReplyImageItem[], index: number) {
  if (items.length === 0) return;
  const clamped = Math.max(0, Math.min(items.length - 1, index));
  current = { kind: "files", home, run, items, index: clamped, seq: ++seq };
  emit();
}

/** The pictures of a prompt (in its bubble or the composer), from the one clicked. */
export function openAttachmentLightbox(session: string, machine: string, items: AttachmentRef[], index: number) {
  if (items.length === 0) return;
  current = { kind: "attachments", session, machine, items, index: Math.max(0, Math.min(items.length - 1, index)), seq: ++seq };
  emit();
}

/** Pictures already in the app, from the one clicked. */
export function openPictureLightbox(items: PictureItem[], index: number) {
  if (items.length === 0) return;
  current = { kind: "pictures", items, index: Math.max(0, Math.min(items.length - 1, index)), seq: ++seq };
  emit();
}

/** A picture already in the app, as the Lightbox reads it: its size worked out. */
function usePicture(p: PictureItem | null): ImageState | undefined {
  const [dims, setDims] = useState<{ src: string; width: number; height: number } | null>(null);
  useEffect(() => {
    if (!p) return;
    let live = true;
    void decodeDimensions(p.src).then((d) => live && setDims({ src: p.src, ...d }));
    return () => {
      live = false;
    };
  }, [p?.src]);
  if (!p) return undefined;
  const d = dims?.src === p.src ? dims : { width: 0, height: 0 };
  return { status: "ready", url: p.src, width: d.width, height: d.height, size: 0n, turn: 0, folder: "", resolvedPath: p.name };
}

const download = (session: string, machine: string) => (sha256: string) =>
  sessions.downloadBlob({ sessionId: session, machineId: machine, blobSha256: sha256 });

/** A prompt's picture as the Lightbox reads it: the bubble's own blob URL, its size as sent. */
function useAttachmentImage(att: AttachmentRef | null, session: string, machine: string, online: boolean): ImageState | undefined {
  const urls = useSyncExternalStore(subscribeAttachments, attachmentSnapshot);
  const url = att ? urls[att.blobSha256] : undefined;
  const [dims, setDims] = useState<{ url: string; width: number; height: number } | null>(null);
  useEffect(() => {
    if (att && url === undefined) void loadAttachmentBlob(att, download(session, machine));
  }, [att?.blobSha256, url === undefined]);
  // Its machine back: asked again, as a reply's picture is.
  useEffect(() => {
    if (att && url === null && online && attachmentFailure(att.blobSha256) === "offline") retryAttachment(att.blobSha256);
  }, [att?.blobSha256, url === null, online]);
  useEffect(() => {
    if (!url) return;
    let live = true;
    void decodeDimensions(url).then((d) => live && setDims({ url, ...d }));
    return () => {
      live = false;
    };
  }, [url]);
  if (!att) return undefined;
  if (url === undefined) return { status: "loading" };
  if (url === null) return attachmentFailure(att.blobSha256) === "offline" ? { status: "offline" } : { status: "missing" };
  const d = dims?.url === url ? dims : { width: 0, height: 0 };
  return { status: "ready", url, width: d.width, height: d.height, size: att.sizeBytes, turn: 0, folder: "", resolvedPath: att.name };
}

export function openFileInLightbox(home: FilesHome, file: SessionFile, gallery?: ReplyImageItem[] | null) {
  if (gallery && gallery.length > 0) {
    const idx = gallery.findIndex((g) => sameImagePath(g.path, file.path));
    if (idx >= 0) {
      openSessionLightbox(home, file.run, gallery, idx);
      return;
    }
  }
  openSessionLightbox(home, file.run, [{ path: file.path, caption: "" }], 0);
}

const isAbsPath = (p: string) => /^([/\\]|~\/|[A-Za-z]:[/\\])/.test(p);

export async function sessionImageMenu(
  st: Store,
  home: FilesHome,
  file: SessionFile,
  folder: string,
  opts: { at?: { x: number; y: number }; onOpen?: () => void } = {},
) {
  const t = (await import("../../../../shared/i18n")).i18n.getFixedT(null, "session");
  const name = baseName(file.path);
  const full = folder && !isAbsPath(file.path) ? `${folder.replace(/\/+$/, "")}/${file.path}` : file.path;
  const items: MenuItem[] = [
    ...(opts.onOpen ? [{ id: "open", label: t("files.open") }, { separator: true } as const] : []),
    { id: "save", label: t("files.save") },
    ...(home.local ? [{ id: "reveal", label: t("files.revealFinder") }] : []),
    { separator: true },
    { id: "copyPath", label: t("files.copyPath") },
  ];
  const id = await window.hermesh?.popupMenu(items, opts.at);
  if (id === "open") opts.onOpen?.();
  else if (id === "save") {
    void window.hermesh?.saveSessionFile(file, name).then(
      () => st.show(t("files.saved")),
      (e: unknown) => st.show(t("files.failed", { message: e instanceof Error ? e.message : String(e) })),
    );
  } else if (id === "reveal") {
    void window.hermesh?.revealSessionFile(file).then((ok) => ok || st.show(t("files.notFound")));
  } else if (id === "copyPath") {
    void navigator.clipboard.writeText(full).then(() => st.show(t("content.copied")));
  }
}

export function ImageLightboxHost() {
  const shown = useShownLightbox();
  return shown ? <ImageLightbox key={shown.seq} shown={shown} onClose={closeSessionLightbox} /> : null;
}

function ImageLightbox({ shown, onClose }: { shown: ShownLightbox; onClose: () => void }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const [index, setIndex] = useState(shown.index);
  const [scale, setScale] = useState(1);
  const [pan, setPan] = useState({ x: 0, y: 0 });
  const [dragging, setDragging] = useState(false);
  const dragRef = useRef<{ startX: number; startY: number; baseX: number; baseY: number; moved: boolean } | null>(null);
  const moreRef = useRef<HTMLButtonElement>(null);

  const files = shown.kind === "files" ? shown : null;
  const atts = shown.kind === "attachments" ? shown : null;
  const pics = shown.kind === "pictures" ? shown : null;
  const pic = pics ? (pics.items[index] ?? pics.items[0]!) : null;
  const item = files ? (files.items[index] ?? files.items[0]!) : null;
  const att = atts ? (atts.items[index] ?? atts.items[0]!) : null;
  const file: SessionFile | null = files && item ? { session: files.home.session, run: files.run, path: item.path } : null;
  const machineId = files ? files.home.machine : (atts?.machine ?? "");
  const machine = st.machines.find((m) => m.machineId === machineId);
  const online = (files?.home.local ?? false) || (st.status?.relayConnected !== false && (machine?.online ?? false));
  const fileState = useSessionImage(file, true, online);
  const attState = useAttachmentImage(att, atts?.session ?? "", machineId, online);
  const picState = usePicture(pic);
  const state = files ? fileState : atts ? attState : picState;
  const caption = item?.caption ?? "";
  const askedPath = item?.path ?? att?.name ?? pic?.name ?? "";

  // Prefetch adjacent images for instant stepping.
  useEffect(() => {
    for (const adj of [index - 1, index + 1]) {
      if (files) {
        const neighbor = files.items[adj];
        if (neighbor) requestSessionImage({ session: files.home.session, run: files.run, path: neighbor.path });
      } else if (atts) {
        const neighbor = atts.items[adj];
        if (neighbor) void loadAttachmentBlob(neighbor, download(atts.session, atts.machine));
      }
    }
  }, [index, shown]);

  const stepTo = (next: number) => {
    const clamped = Math.max(0, Math.min(shown.items.length - 1, next));
    if (clamped === index) return;
    setIndex(clamped);
    setScale(1);
    setPan({ x: 0, y: 0 });
  };

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        onClose();
      } else if (e.key === "ArrowLeft" && shown.items.length > 1) {
        e.preventDefault();
        stepTo(index - 1);
      } else if (e.key === "ArrowRight" && shown.items.length > 1) {
        e.preventDefault();
        stepTo(index + 1);
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [index, shown.items.length, onClose]);

  const resolvedPath = state?.status === "ready" ? state.resolvedPath : askedPath;
  const folder = state?.status === "ready" ? state.folder : "";
  const name = baseName(resolvedPath);
  const metaParts: string[] = [];
  if (shown.items.length > 1) metaParts.push(`${index + 1} / ${shown.items.length}`);
  if (state?.status === "ready" && state.width > 0 && state.height > 0) metaParts.push(`${state.width} × ${state.height}`);
  if (state?.status === "ready" && state.size > 0n) metaParts.push(sizeText(state.size));

  const clampZoom = (next: number) => Math.max(1, Math.min(4, next));

  const openMore = (from?: HTMLElement | null) => {
    const rect = from?.getBoundingClientRect();
    const at = rect ? { x: Math.round(rect.left), y: Math.round(rect.bottom + 4) } : undefined;
    if (files && file) void sessionImageMenu(st, files.home, { ...file, path: resolvedPath }, folder, { at });
    else if (pic?.save) void window.hermesh?.popupMenu([{ id: "save", label: t("files.save") }], at).then((id) => id === "save" && pic.save?.());
  };

  return createPortal(
    <div className="slb" role="dialog" aria-modal="true" aria-label={name} data-testid="image-lightbox" data-path={resolvedPath} data-source={shown.kind}>
      <div className="slb-top">
        <div className="slb-spacer" aria-hidden="true" />
        <div className="slb-center">
          <div className="slb-title" title={resolvedPath} data-testid="lightbox-title">
            {name}
          </div>
          {metaParts.length > 0 && (
            <div className="slb-meta" data-testid="lightbox-meta">
              {metaParts.join(" · ")}
            </div>
          )}
        </div>
        <div className="slb-actions">
          {(files || pic?.save) && (
            <button
              ref={moreRef}
              className="slb-btn"
              aria-label={t("files.more")}
              title={t("files.more")}
              data-testid="lightbox-more"
              onClick={() => openMore(moreRef.current)}
            >
              <Icon name="more" />
            </button>
          )}
          <button
            className="slb-btn"
            aria-label={t("markdown.close")}
            title={t("markdown.close")}
            data-testid="lightbox-close"
            onClick={onClose}
          >
            <Icon name="x" />
          </button>
        </div>
      </div>

      <div
        className="slb-stage"
        onClick={(e) => {
          if (dragRef.current?.moved) {
            dragRef.current = null;
            return;
          }
          if (e.target === e.currentTarget) onClose();
        }}
        onWheel={(e) => {
          e.preventDefault();
          const factor = e.deltaY < 0 ? 1.14 : 1 / 1.14;
          const next = clampZoom(scale * factor);
          setScale(next);
          if (next === 1) setPan({ x: 0, y: 0 });
        }}
      >
        {shown.items.length > 1 && (
          <button
            className="slb-nav prev"
            disabled={index === 0}
            aria-label="Previous"
            data-testid="lightbox-prev"
            onClick={(e) => (e.stopPropagation(), stepTo(index - 1))}
          >
            <Icon name="chevL" />
          </button>
        )}

        {!state || state.status === "loading" ? (
          <span className="mini-spin" />
        ) : state.status === "ready" ? (
          <img
            src={state.url}
            alt={caption || name}
            draggable={false}
            data-testid="lightbox-image"
            className={`slb-img ${scale > 1 ? (dragging ? "grabbing" : "grab") : ""}`}
            style={{
              transform: `translate3d(${pan.x}px, ${pan.y}px, 0) scale(${scale})`,
              transition: dragging ? "none" : "transform .18s cubic-bezier(.2, 0, 0, 1)",
            }}
            onClick={(e) => e.stopPropagation()}
            onDoubleClick={(e) => {
              e.stopPropagation();
              if (scale > 1.05) {
                setScale(1);
                setPan({ x: 0, y: 0 });
              } else {
                setScale(2.4);
              }
            }}
            onPointerDown={(e) => {
              if (scale <= 1 || e.button !== 0) return;
              e.currentTarget.setPointerCapture(e.pointerId);
              setDragging(true);
              dragRef.current = { startX: e.clientX, startY: e.clientY, baseX: pan.x, baseY: pan.y, moved: false };
            }}
            onPointerMove={(e) => {
              const d = dragRef.current;
              if (!d || !dragging) return;
              const dx = e.clientX - d.startX;
              const dy = e.clientY - d.startY;
              if (Math.abs(dx) > 3 || Math.abs(dy) > 3) d.moved = true;
              const maxX = Math.max(0, ((scale - 1) * window.innerWidth) / 2);
              const maxY = Math.max(0, ((scale - 1) * window.innerHeight) / 2);
              setPan({
                x: Math.max(-maxX, Math.min(maxX, d.baseX + dx)),
                y: Math.max(-maxY, Math.min(maxY, d.baseY + dy)),
              });
            }}
            onPointerUp={() => setDragging(false)}
            onPointerCancel={() => setDragging(false)}
            onContextMenu={(e) => {
              e.preventDefault();
              e.stopPropagation();
              openMore();
            }}
          />
        ) : (
          <div className="slb-err" onClick={(e) => e.stopPropagation()}>
            <Icon name={state.status === "offline" ? "cloudOff" : "doc"} />
            <b>{name}</b>
            <span>
              {state.status === "offline"
                ? t("files.imageOffline", { machine: machineName(st, machineId) })
                : state.status === "tooLarge"
                  ? t("files.tooLarge", { size: sizeText(state.size) })
                  : state.status === "missing"
                    ? t("files.notFound")
                    : state.message}
            </span>
            {att && state.status !== "tooLarge" && (
              <button className="slb-retry" data-testid="lightbox-retry" onClick={() => retryAttachment(att.blobSha256)}>
                {t("files.retry")}
              </button>
            )}
          </div>
        )}

        {shown.items.length > 1 && (
          <button
            className="slb-nav next"
            disabled={index === shown.items.length - 1}
            aria-label="Next"
            data-testid="lightbox-next"
            onClick={(e) => (e.stopPropagation(), stepTo(index + 1))}
          >
            <Icon name="chevR" />
          </button>
        )}
      </div>
    </div>,
    document.body,
  );
}
