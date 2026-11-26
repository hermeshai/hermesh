import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { sameImagePath } from "@hermesh/her-client";
import type { MenuItem } from "../../../../shared/bridge";
import { Icon } from "../../icons";
import { machineName } from "../../Sidebar";
import { useStore, type Store } from "../../store";
import { baseName, sizeText, type FilesHome, type SessionFile } from "./files";
import { requestSessionImage, useSessionImage, type ReplyImageItem } from "./images";

export type ShownLightbox = {
  home: FilesHome;
  run: string;
  items: ReplyImageItem[];
  index: number;
  seq: number;
};

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
  current = { home, run, items, index: clamped, seq: ++seq };
  emit();
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

  const item = shown.items[index] ?? shown.items[0]!;
  const file: SessionFile = { session: shown.home.session, run: shown.run, path: item.path };
  const machine = st.machines.find((m) => m.machineId === shown.home.machine);
  const online = shown.home.local || (st.status?.relayConnected !== false && (machine?.online ?? false));
  const state = useSessionImage(file, true, online);

  // Prefetch adjacent images in the same reply for instant stepping.
  useEffect(() => {
    for (const adj of [index - 1, index + 1]) {
      const neighbor = shown.items[adj];
      if (neighbor) requestSessionImage({ session: shown.home.session, run: shown.run, path: neighbor.path });
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

  const resolvedPath = state?.status === "ready" ? state.resolvedPath : item.path;
  const folder = state?.status === "ready" ? state.folder : "";
  const name = baseName(resolvedPath);
  const metaParts: string[] = [];
  if (shown.items.length > 1) metaParts.push(`${index + 1} / ${shown.items.length}`);
  if (state?.status === "ready" && state.width > 0 && state.height > 0) metaParts.push(`${state.width} × ${state.height}`);
  if (state?.status === "ready" && state.size > 0n) metaParts.push(sizeText(state.size));

  const clampZoom = (next: number) => Math.max(1, Math.min(4, next));

  const openMore = () => {
    const rect = moreRef.current?.getBoundingClientRect();
    const at = rect ? { x: Math.round(rect.left), y: Math.round(rect.bottom + 4) } : undefined;
    void sessionImageMenu(st, shown.home, { ...file, path: resolvedPath }, folder, { at });
  };

  return createPortal(
    <div className="slb" role="dialog" aria-modal="true" aria-label={name} data-testid="image-lightbox" data-path={resolvedPath}>
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
          <button
            ref={moreRef}
            className="slb-btn"
            aria-label={t("files.more")}
            title={t("files.more")}
            data-testid="lightbox-more"
            onClick={openMore}
          >
            <Icon name="more" />
          </button>
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
            alt={item.caption || name}
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
              void sessionImageMenu(st, shown.home, { ...file, path: resolvedPath }, folder);
            }}
          />
        ) : (
          <div className="slb-err" onClick={(e) => e.stopPropagation()}>
            <Icon name={state.status === "offline" ? "cloudOff" : "doc"} />
            <b>{name}</b>
            <span>
              {state.status === "offline"
                ? t("files.imageOffline", { machine: machineName(st, shown.home.machine) })
                : state.status === "tooLarge"
                  ? t("files.tooLarge", { size: sizeText(state.size) })
                  : state.status === "missing"
                    ? t("files.notFound")
                    : state.message}
            </span>
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
