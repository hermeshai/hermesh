import { useContext, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { sameImagePath } from "@hermesh/her-client";
import { FileRefKind } from "@hermesh/proto/session";
import { Icon } from "../../icons";
import { machineName } from "../../Sidebar";
import { useStore } from "../../store";
import { DraftContext } from "../scope";
import { FileTag } from "./FileTag";
import { baseName, FilesHomeContext, FilesRunContext, sizeText, useFileInfo, type FilesHome, type SessionFile } from "./files";
import { openFileInLightbox, openSessionLightbox, sessionImageMenu } from "./ImageLightbox";
import { ReplyImagesContext, requestSessionImage, useSessionImage, type ReplyImageItem } from "./images";

export type InlineImageSlotPayload = {
  items: ReplyImageItem[];
  fromLinks: boolean;
};

const MAX_STAGE_STATES = 120;
const stageIndexStore = new Map<string, number>();

function rememberStageIndex(key: string, idx: number) {
  if (stageIndexStore.has(key)) stageIndexStore.delete(key);
  stageIndexStore.set(key, idx);
  if (stageIndexStore.size > MAX_STAGE_STATES) {
    const oldest = stageIndexStore.keys().next().value;
    if (oldest !== undefined) stageIndexStore.delete(oldest);
  }
}

function hasDistinctCaption(caption: string, name: string): boolean {
  const trimmed = caption.trim();
  return Boolean(trimmed) && trimmed.toLowerCase() !== name.toLowerCase();
}

export function SessionImageSlot({ raw }: { raw: string }) {
  let parsed: InlineImageSlotPayload | null = null;
  try {
    parsed = JSON.parse(raw) as InlineImageSlotPayload;
  } catch {
    return null;
  }
  if (!parsed || !Array.isArray(parsed.items) || parsed.items.length === 0) return null;
  return <SessionImageGroup items={parsed.items} fromLinks={Boolean(parsed.fromLinks)} />;
}

function SessionImageGroup({ items, fromLinks }: { items: ReplyImageItem[]; fromLinks: boolean }) {
  const draft = useContext(DraftContext);
  const home = useContext(FilesHomeContext);
  const run = useContext(FilesRunContext);
  const replyImages = useContext(ReplyImagesContext);
  const boxRef = useRef<HTMLDivElement>(null);
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    if (draft || visible) return;
    const el = boxRef.current;
    if (!el) return;
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) {
          setVisible(true);
          io.disconnect();
        }
      },
      { rootMargin: "320px" },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [draft, visible]);

  if (!home) {
    if (fromLinks) return null;
    return <span>{items.map((it) => it.caption || baseName(it.path)).join(" ")}</span>;
  }

  if (draft) {
    if (fromLinks) return null;
    return <div className="simg-draft shimmer" aria-hidden="true" />;
  }

  const gallery = replyImages?.gallery && replyImages.gallery.length > 0 ? replyImages.gallery : items;
  const openInGallery = (target: ReplyImageItem, resolvedPath: string) => {
    const gIdx = gallery.findIndex((g) => sameImagePath(g.path, target.path));
    if (gIdx >= 0) openSessionLightbox(home, run, gallery, gIdx);
    else openFileInLightbox(home, { session: home.session, run, path: resolvedPath }, gallery);
  };

  if (items.length >= 3) {
    return (
      <SessionImageStage
        boxRef={boxRef}
        items={items}
        home={home}
        run={run}
        visible={visible}
        fromLinks={fromLinks}
        onOpen={openInGallery}
      />
    );
  }

  const multi = items.length > 1;
  return (
    <div ref={boxRef} className={multi ? "simg-row" : "simg-wrap"} data-testid={multi ? "session-image-row" : undefined}>
      {items.map((it, idx) => (
        <SessionImageCard
          key={`${idx}:${it.path}`}
          item={it}
          home={home}
          run={run}
          visible={visible}
          multi={multi}
          fromLinks={fromLinks}
          onOpen={(resolvedPath) => openInGallery(it, resolvedPath)}
        />
      ))}
    </div>
  );
}

function SessionImageStage({
  boxRef,
  items,
  home,
  run,
  visible,
  fromLinks,
  onOpen,
}: {
  boxRef: React.RefObject<HTMLDivElement | null>;
  items: ReplyImageItem[];
  home: FilesHome;
  run: string;
  visible: boolean;
  fromLinks: boolean;
  onOpen: (item: ReplyImageItem, resolvedPath: string) => void;
}) {
  const { t } = useTranslation("session");
  const st = useStore();
  const stripRef = useRef<HTMLDivElement>(null);
  const stateKey = `${home.session}\0${run}\0${items[0]?.path ?? ""}`;
  const [activeIdx, setActiveIdx] = useState(() => stageIndexStore.get(stateKey) ?? 0);
  const safeIdx = Math.max(0, Math.min(items.length - 1, activeIdx));
  const activeItem = items[safeIdx]!;

  const select = (next: number) => {
    const wrapped = ((next % items.length) + items.length) % items.length;
    rememberStageIndex(stateKey, wrapped);
    setActiveIdx(wrapped);
  };

  useEffect(() => {
    const strip = stripRef.current;
    if (!strip) return;
    const thumb = strip.children[safeIdx] as HTMLElement | undefined;
    if (!thumb) return;
    const targetLeft = thumb.offsetLeft - strip.clientWidth / 2 + thumb.offsetWidth / 2;
    const clampedLeft = Math.max(0, Math.min(strip.scrollWidth - strip.clientWidth, targetLeft));
    if (Math.abs(strip.scrollLeft - clampedLeft) > 8) {
      strip.scrollTo({ left: clampedLeft, behavior: "smooth" });
    }
  }, [safeIdx]);

  const info = useFileInfo(activeItem.path);
  const machine = st.machines.find((m) => m.machineId === home.machine);
  const online = home.local || (st.status?.relayConnected !== false && (machine?.online ?? false));
  const goneByInfo = info?.kind === FileRefKind.MISSING || info?.kind === FileRefKind.OUTSIDE;
  const effectivePath = info && !goneByInfo && info.path ? info.path : activeItem.path;
  const activeFile: SessionFile = { session: home.session, run, path: effectivePath };
  const activeState = useSessionImage(goneByInfo ? null : activeFile, visible && !goneByInfo, online, true);

  // Prioritize immediate neighbor slides right after the active slide so stepping feels instantaneous.
  useEffect(() => {
    if (!visible) return;
    for (const delta of [1, -1]) {
      const neighbor = items[((safeIdx + delta) % items.length + items.length) % items.length];
      if (neighbor) requestSessionImage({ session: home.session, run, path: neighbor.path }, false, true);
    }
  }, [visible, safeIdx, home.session, run, items]);

  const activeResolvedPath = activeState?.status === "ready" ? activeState.resolvedPath : effectivePath;
  const activeFolder = activeState?.status === "ready" ? activeState.folder : info?.folder ?? "";
  const activeName = baseName(activeResolvedPath);
  const activeDims =
    activeState?.status === "ready" && activeState.width > 0 && activeState.height > 0
      ? `${activeState.width} × ${activeState.height}`
      : "";
  const anyCaption = items.some((it) => hasDistinctCaption(it.caption, baseName(it.path)));
  const stageLoading = !goneByInfo && (!activeState || activeState.status === "loading");

  return (
    <div
      ref={boxRef}
      className="simg-stage"
      data-testid="session-image-row"
      data-simg-mode="stage"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "ArrowLeft") {
          e.preventDefault();
          select(safeIdx - 1);
        } else if (e.key === "ArrowRight") {
          e.preventDefault();
          select(safeIdx + 1);
        }
      }}
    >
      <div className={`simg-stage-main ${stageLoading ? "shimmer" : ""}`}>
        <span className="simg-stage-pill" data-testid="session-image-counter">
          {`${safeIdx + 1} / ${items.length}${activeDims ? ` · ${activeDims}` : ""}`}
        </span>
        <button
          type="button"
          className="simg-stage-nav prev"
          aria-label={t("files.prevImage")}
          data-testid="session-image-prev"
          onClick={(e) => {
            e.stopPropagation();
            select(safeIdx - 1);
          }}
        >
          <Icon name="chevL" />
        </button>

        {goneByInfo || activeState?.status === "missing" ? (
          <div className="simg-stage-empty">
            {!fromLinks ? (
              <FileTag target={{ path: activeItem.path }}>{activeItem.caption || baseName(activeItem.path)}</FileTag>
            ) : (
              <span>{t("files.notFound")}</span>
            )}
          </div>
        ) : activeState?.status === "offline" ? (
          <div className="simg-offline" data-testid="session-image-offline" data-path={effectivePath}>
            <Icon name="cloudOff" />
            <span>{t("files.imageOffline", { machine: machineName(st, home.machine) })}</span>
          </div>
        ) : activeState?.status === "tooLarge" ? (
          <div className="simg-offline" data-path={effectivePath}>
            <Icon name="doc" />
            <span>{t("files.tooLarge", { size: sizeText(activeState.size) })}</span>
          </div>
        ) : activeState?.status === "error" ? (
          <div className="simg-offline" data-path={effectivePath}>
            <Icon name="warn" />
            <span>{activeState.message}</span>
          </div>
        ) : (
          <button
            type="button"
            className="simg-btn simg-stage-view"
            aria-label={activeItem.caption || activeName}
            data-testid="session-image"
            data-path={activeResolvedPath}
            onClick={() => onOpen(activeItem, activeResolvedPath)}
            onContextMenu={(e) => {
              e.preventDefault();
              e.stopPropagation();
              void sessionImageMenu(st, home, { session: home.session, run, path: activeResolvedPath }, activeFolder, {
                onOpen: () => onOpen(activeItem, activeResolvedPath),
              });
            }}
          >
            {activeState?.status === "ready" && (
              <img src={activeState.url} alt={activeItem.caption || activeName} draggable={false} />
            )}
          </button>
        )}

        <button
          type="button"
          className="simg-stage-nav next"
          aria-label={t("files.nextImage")}
          data-testid="session-image-next"
          onClick={(e) => {
            e.stopPropagation();
            select(safeIdx + 1);
          }}
        >
          <Icon name="chevR" />
        </button>
      </div>

      {anyCaption && (
        <div className="simg-stage-cap" data-testid="session-image-caption">
          <span>{activeItem.caption || activeName}</span>
        </div>
      )}

      <div ref={stripRef} className="simg-filmstrip" role="tablist">
        {items.map((it, idx) => (
          <SessionStageThumb
            key={`${idx}:${it.path}`}
            item={it}
            index={idx}
            total={items.length}
            selected={idx === safeIdx}
            home={home}
            run={run}
            visible={visible}
            online={online}
            onSelect={() => select(idx)}
          />
        ))}
      </div>
    </div>
  );
}

function SessionStageThumb({
  item,
  index,
  total,
  selected,
  home,
  run,
  visible,
  online,
  onSelect,
}: {
  item: ReplyImageItem;
  index: number;
  total: number;
  selected: boolean;
  home: FilesHome;
  run: string;
  visible: boolean;
  online: boolean;
  onSelect: () => void;
}) {
  const info = useFileInfo(item.path);
  const goneByInfo = info?.kind === FileRefKind.MISSING || info?.kind === FileRefKind.OUTSIDE;
  const effectivePath = info && !goneByInfo && info.path ? info.path : item.path;
  const state = useSessionImage(goneByInfo ? null : { session: home.session, run, path: effectivePath }, visible && !goneByInfo, online, selected);
  const name = baseName(effectivePath);
  const label = item.caption || name;
  const loading = !goneByInfo && (!state || state.status === "loading");

  return (
    <button
      type="button"
      role="tab"
      aria-selected={selected}
      aria-label={`${index + 1} / ${total}: ${label}`}
      title={label}
      data-testid="session-image-thumb"
      data-index={index}
      data-path={effectivePath}
      className={`simg-thumb ${selected ? "on" : ""} ${loading ? "shimmer" : ""}`}
      onClick={onSelect}
    >
      {state?.status === "ready" ? (
        <img src={state.url} alt="" draggable={false} />
      ) : goneByInfo || state?.status === "missing" || state?.status === "offline" || state?.status === "error" || state?.status === "tooLarge" ? (
        <Icon name={state?.status === "offline" ? "cloudOff" : "doc"} />
      ) : null}
    </button>
  );
}

function SessionImageCard({
  item,
  home,
  run,
  visible,
  multi,
  fromLinks,
  onOpen,
}: {
  item: ReplyImageItem;
  home: FilesHome;
  run: string;
  visible: boolean;
  multi: boolean;
  fromLinks: boolean;
  onOpen: (resolvedPath: string) => void;
}) {
  const { t } = useTranslation("session");
  const st = useStore();
  const info = useFileInfo(item.path);
  const machine = st.machines.find((m) => m.machineId === home.machine);
  const online = home.local || (st.status?.relayConnected !== false && (machine?.online ?? false));
  const goneByInfo = info?.kind === FileRefKind.MISSING || info?.kind === FileRefKind.OUTSIDE;
  const effectivePath = info && !goneByInfo && info.path ? info.path : item.path;
  const file: SessionFile = { session: home.session, run, path: effectivePath };
  const state = useSessionImage(goneByInfo ? null : file, visible && !goneByInfo, online);

  if (goneByInfo || state?.status === "missing") {
    if (fromLinks) return null;
    return (
      <div className="simg-fallback">
        <FileTag target={{ path: item.path }}>{item.caption || baseName(item.path)}</FileTag>
      </div>
    );
  }

  const name = baseName(effectivePath);
  const showCaption = multi && hasDistinctCaption(item.caption, name);

  if (state?.status === "offline") {
    return (
      <div className="simg-offline" data-testid="session-image-offline" data-path={effectivePath}>
        <Icon name="cloudOff" />
        <span>{t("files.imageOffline", { machine: machineName(st, home.machine) })}</span>
      </div>
    );
  }

  if (state?.status === "tooLarge") {
    return (
      <div className="simg-offline" data-path={effectivePath}>
        <Icon name="doc" />
        <span>{t("files.tooLarge", { size: sizeText(state.size) })}</span>
      </div>
    );
  }

  if (state?.status === "error") {
    if (fromLinks) return null;
    return (
      <div className="simg-offline" data-path={effectivePath}>
        <Icon name="warn" />
        <span>{state.message}</span>
      </div>
    );
  }

  const ratio = state?.status === "ready" && state.width > 0 && state.height > 0 ? state.width / state.height : undefined;
  const resolvedPath = state?.status === "ready" ? state.resolvedPath : effectivePath;
  const folder = state?.status === "ready" ? state.folder : info?.folder ?? "";

  return (
    <figure
      className={`simg-card ${multi ? "in-row" : "single"} ${state?.status === "ready" ? "ready" : "loading shimmer"}`}
      style={
        multi && ratio
          ? { flexGrow: Math.max(0.55, Math.min(2.0, ratio)), flexBasis: 0 }
          : !multi && state?.status === "ready" && state.width > 0 && state.height > 0
            ? { aspectRatio: `${state.width} / ${state.height}` }
            : undefined
      }
    >
      <button
        type="button"
        className="simg-btn"
        aria-label={item.caption || name}
        data-testid="session-image"
        data-path={resolvedPath}
        onClick={() => onOpen(resolvedPath)}
        onContextMenu={(e) => {
          e.preventDefault();
          e.stopPropagation();
          void sessionImageMenu(st, home, { session: home.session, run, path: resolvedPath }, folder, {
            onOpen: () => onOpen(resolvedPath),
          });
        }}
      >
        <span className="simg-pic">
          {state?.status === "ready" && <img src={state.url} alt={item.caption || name} draggable={false} />}
        </span>
        {showCaption && (
          <span className="simg-pair-cap">
            <span>{item.caption}</span>
          </span>
        )}
      </button>
    </figure>
  );
}
