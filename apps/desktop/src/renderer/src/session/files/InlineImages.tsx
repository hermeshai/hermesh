import { useContext, useEffect, useRef, useState, type CSSProperties } from "react";
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
import {
  isTallStageImage,
  isUltraLongImage,
  ReplyImagesContext,
  requestSessionImage,
  shortImageTag,
  useAnyTallSessionImage,
  useSessionImage,
  type ReplyImageItem,
} from "./images";

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
  }, [draft, visible, Boolean(home)]);

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

const pad2 = (n: number) => String(n).padStart(2, "0");

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
  const barRef = useRef<HTMLDivElement>(null);
  const trackRef = useRef<HTMLDivElement>(null);
  const progScrollUntilRef = useRef(0);
  const stateKey = `${home.session}\0${run}\0${items[0]?.path ?? ""}`;
  const [activeIdx, setActiveIdx] = useState(() => stageIndexStore.get(stateKey) ?? 0);
  const [stageRatio, setStageRatio] = useState<number | null>(null);
  const [loupe, setLoupe] = useState<{ idx: number; left: number; bottom: number } | null>(null);
  const [canScroll, setCanScroll] = useState(false);
  const hasTall = useAnyTallSessionImage(home.session, run, items.map((it) => it.path));
  const [latchedStrip, setLatchedStrip] = useState(false);
  useEffect(() => {
    if (hasTall && !latchedStrip) setLatchedStrip(true);
  }, [hasTall, latchedStrip]);
  const isStrip = latchedStrip || hasTall;

  const safeIdx = Math.max(0, Math.min(items.length - 1, activeIdx));
  const activeItem = items[safeIdx]!;

  const scrollCardIntoView = (idx: number, smooth: boolean) => {
    const track = trackRef.current;
    if (!track) return;
    const card = track.querySelector<HTMLElement>(`[data-strip-idx="${idx}"]`);
    if (!card) return;
    const maxLeft = Math.max(0, track.scrollWidth - track.clientWidth);
    if (maxLeft <= 2) return;
    const cardLeft = card.offsetLeft;
    const cardRight = cardLeft + card.offsetWidth;
    const viewLeft = track.scrollLeft;
    const viewRight = viewLeft + track.clientWidth;
    let nextLeft = viewLeft;
    if (cardLeft < viewLeft + 4) nextLeft = Math.max(0, cardLeft - 4);
    else if (cardRight > viewRight - 4) nextLeft = Math.min(maxLeft, cardRight - track.clientWidth + 4);
    else return;
    progScrollUntilRef.current = performance.now() + (smooth ? 420 : 80);
    track.scrollTo({
      left: nextLeft,
      behavior: smooth && !matchMedia("(prefers-reduced-motion: reduce)").matches ? "smooth" : "instant",
    });
  };

  const select = (next: number) => {
    const wrapped = ((next % items.length) + items.length) % items.length;
    rememberStageIndex(stateKey, wrapped);
    setLoupe(null);
    setActiveIdx(wrapped);
    if (isStrip) scrollCardIntoView(wrapped, true);
  };

  const info = useFileInfo(activeItem.path);
  const machine = st.machines.find((m) => m.machineId === home.machine);
  const online = home.local || (st.status?.relayConnected !== false && (machine?.online ?? false));
  const goneByInfo = info?.kind === FileRefKind.MISSING || info?.kind === FileRefKind.OUTSIDE;
  const effectivePath = info && !goneByInfo && info.path ? info.path : activeItem.path;
  const activeFile: SessionFile = { session: home.session, run, path: effectivePath };
  const activeState = useSessionImage(goneByInfo ? null : activeFile, visible && !goneByInfo, online, true);

  // Lock stage aspect ratio to the first decoded landscape slide so stepping across wide slides never shifts vertical scroll,
  // while switching automatically to equal-height multi-up strip whenever any slide is portrait/tall (width / height <= 1.05).
  useEffect(() => {
    if (stageRatio !== null || isStrip) return;
    if (activeState?.status === "ready" && activeState.width > 0 && activeState.height > 0) {
      setStageRatio(Math.max(1.25, Math.min(2.2, activeState.width / activeState.height)));
    }
  }, [stageRatio, isStrip, activeState]);

  // Only prefetch immediate neighbors in single-slide stage mode (avoids flooding N thumbnail fetches at once).
  useEffect(() => {
    if (!visible || isStrip) return;
    for (const delta of [1, -1]) {
      const neighbor = items[((safeIdx + delta) % items.length + items.length) % items.length];
      if (neighbor) requestSessionImage({ session: home.session, run, path: neighbor.path }, false, true);
    }
  }, [visible, isStrip, safeIdx, home.session, run, items]);

  useEffect(() => {
    if (!isStrip) return;
    const track = trackRef.current;
    if (!track) return;
    const measure = () => setCanScroll(track.scrollWidth > track.clientWidth + 4);
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(track);
    Array.from(track.children).forEach((c) => ro.observe(c));
    return () => ro.disconnect();
  }, [isStrip, items.length]);

  const handleTrackScroll = () => {
    const track = trackRef.current;
    if (!track) return;
    setCanScroll(track.scrollWidth > track.clientWidth + 4);
    if (performance.now() < progScrollUntilRef.current) return;
    const maxLeft = Math.max(0, track.scrollWidth - track.clientWidth);
    if (maxLeft <= 2) return;
    if (track.scrollLeft >= maxLeft - 6) {
      const last = items.length - 1;
      rememberStageIndex(stateKey, last);
      setActiveIdx(last);
      return;
    }
    const cards = track.querySelectorAll<HTMLElement>("[data-strip-idx]");
    const anchor = track.scrollLeft + 24;
    let best = 0;
    let bestDist = Infinity;
    cards.forEach((el) => {
      const i = Number(el.dataset.stripIdx ?? 0);
      const dist = Math.abs(el.offsetLeft - anchor);
      if (dist < bestDist) {
        bestDist = dist;
        best = i;
      }
    });
    rememberStageIndex(stateKey, best);
    setActiveIdx(best);
  };

  const activeResolvedPath = activeState?.status === "ready" ? activeState.resolvedPath : effectivePath;
  const activeFolder = activeState?.status === "ready" ? activeState.folder : info?.folder ?? "";
  const activeName = baseName(activeResolvedPath);
  const anyCaption = items.some((it) => hasDistinctCaption(it.caption, baseName(it.path)));
  const stageLoading = !goneByInfo && (!activeState || activeState.status === "loading");

  const showLoupe = (idx: number, chipEl: HTMLButtonElement) => {
    if (idx === safeIdx) {
      setLoupe(null);
      return;
    }
    if (isStrip && trackRef.current) {
      const card = trackRef.current.querySelector<HTMLElement>(`[data-strip-idx="${idx}"]`);
      if (
        card &&
        card.offsetLeft >= trackRef.current.scrollLeft - 2 &&
        card.offsetLeft + card.offsetWidth <= trackRef.current.scrollLeft + trackRef.current.clientWidth + 2
      ) {
        setLoupe(null);
        return;
      }
    }
    const bar = barRef.current;
    if (!bar) return;
    const barRect = bar.getBoundingClientRect();
    const chipRect = chipEl.getBoundingClientRect();
    const center = chipRect.left - barRect.left + chipRect.width / 2;
    const clampedLeft = Math.max(70, Math.min(Math.max(70, barRect.width - 70), center));
    const bottom = barRect.bottom - chipRect.top + 7;
    setLoupe({ idx, left: clampedLeft, bottom });
  };

  return (
    <figure
      ref={boxRef}
      className="simg-stage"
      data-testid="session-image-row"
      data-simg-mode={isStrip ? "strip" : "stage"}
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
      {isStrip ? (
        <div className="simg-strip-main">
          {canScroll && (
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
          )}
          <div ref={trackRef} className="simg-strip-track" onScroll={handleTrackScroll}>
            {items.map((it, idx) => (
              <SessionStripCard
                key={`${idx}:${it.path}`}
                idx={idx}
                item={it}
                selected={idx === safeIdx}
                home={home}
                run={run}
                visible={visible}
                online={online}
                urgent={Math.abs(idx - safeIdx) <= 2}
                fromLinks={fromLinks}
                onOpen={(resolvedPath) => {
                  rememberStageIndex(stateKey, idx);
                  setActiveIdx(idx);
                  onOpen(it, resolvedPath);
                }}
              />
            ))}
          </div>
          {canScroll && (
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
          )}
        </div>
      ) : (
        <div
          className={`simg-stage-main ${stageLoading ? "shimmer" : ""}`}
          style={stageRatio ? ({ "--stage-ratio": String(stageRatio) } as CSSProperties) : undefined}
        >
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
      )}

      <div
        ref={barRef}
        className="simg-index-bar"
        role="tablist"
        onMouseLeave={() => setLoupe(null)}
      >
        {items.map((it, idx) => {
          const name = baseName(it.path);
          const tag = shortImageTag(it.caption, name);
          const selected = idx === safeIdx;
          const label = it.caption || name;
          return (
            <button
              key={`${idx}:${it.path}`}
              type="button"
              role="tab"
              aria-selected={selected}
              aria-label={`${idx + 1} / ${items.length}: ${label}`}
              title={label}
              data-testid="session-image-thumb"
              data-index={idx}
              data-path={it.path}
              className={`simg-index-chip ${selected ? "on" : ""}`}
              onMouseEnter={(e) => showLoupe(idx, e.currentTarget)}
              onFocus={(e) => showLoupe(idx, e.currentTarget)}
              onBlur={() => setLoupe(null)}
              onClick={() => select(idx)}
            >
              <i>{pad2(idx + 1)}</i>
              {tag && <span>{tag}</span>}
            </button>
          );
        })}
        <span className="simg-index-count" data-testid="session-image-counter">
          {`${safeIdx + 1} / ${items.length}`}
        </span>
        {loupe && loupe.idx !== safeIdx && items[loupe.idx] && (
          <SessionIndexLoupe
            item={items[loupe.idx]!}
            home={home}
            run={run}
            online={online}
            left={loupe.left}
            bottom={loupe.bottom}
          />
        )}
      </div>

      {anyCaption && (
        <figcaption className="simg-stage-cap" data-testid="session-image-caption">
          <span>{activeItem.caption || activeName}</span>
        </figcaption>
      )}
    </figure>
  );
}

function SessionStripCard({
  idx,
  item,
  selected,
  home,
  run,
  visible,
  online,
  urgent,
  fromLinks,
  onOpen,
}: {
  idx: number;
  item: ReplyImageItem;
  selected: boolean;
  home: FilesHome;
  run: string;
  visible: boolean;
  online: boolean;
  urgent: boolean;
  fromLinks: boolean;
  onOpen: (resolvedPath: string) => void;
}) {
  const { t } = useTranslation("session");
  const st = useStore();
  const info = useFileInfo(item.path);
  const goneByInfo = info?.kind === FileRefKind.MISSING || info?.kind === FileRefKind.OUTSIDE;
  const effectivePath = info && !goneByInfo && info.path ? info.path : item.path;
  const file: SessionFile = { session: home.session, run, path: effectivePath };
  const state = useSessionImage(goneByInfo ? null : file, visible && !goneByInfo, online, urgent);

  if (goneByInfo || state?.status === "missing") {
    if (fromLinks) return null;
    return (
      <div className="simg-fallback" data-strip-idx={idx}>
        <FileTag target={{ path: item.path }}>{item.caption || baseName(item.path)}</FileTag>
      </div>
    );
  }
  if (state?.status === "offline") {
    return (
      <div className="simg-offline" data-strip-idx={idx} data-testid="session-image-offline" data-path={effectivePath}>
        <Icon name="cloudOff" />
        <span>{t("files.imageOffline", { machine: machineName(st, home.machine) })}</span>
      </div>
    );
  }
  if (state?.status === "tooLarge") {
    return (
      <div className="simg-offline" data-strip-idx={idx} data-path={effectivePath}>
        <Icon name="doc" />
        <span>{t("files.tooLarge", { size: sizeText(state.size) })}</span>
      </div>
    );
  }
  if (state?.status === "error") {
    if (fromLinks) return null;
    return (
      <div className="simg-offline" data-strip-idx={idx} data-path={effectivePath}>
        <Icon name="warn" />
        <span>{state.message}</span>
      </div>
    );
  }

  const ready = state?.status === "ready" && state.width > 0 && state.height > 0;
  const ultraLong = Boolean(ready && state?.status === "ready" && isUltraLongImage(state.width, state.height));
  const ratio = ready && state?.status === "ready" ? state.width / state.height : 0.5;
  const resolvedPath = state?.status === "ready" ? state.resolvedPath : effectivePath;
  const folder = state?.status === "ready" ? state.folder : info?.folder ?? "";
  const name = baseName(resolvedPath);

  return (
    <div
      className={`simg-strip-card ${selected ? "on" : ""} ${ultraLong ? "ultra-long" : ""} ${state?.status === "ready" ? "ready" : "loading shimmer"}`}
      data-strip-idx={idx}
      style={!ultraLong && ready ? ({ "--card-ratio": String(ratio) } as CSSProperties) : undefined}
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
        {state?.status === "ready" && <img src={state.url} alt={item.caption || name} draggable={false} />}
        {ultraLong && (
          <span className="simg-long-veil" data-testid="session-image-long-badge">
            <span>{`↕ ${t("files.longImage")}`}</span>
          </span>
        )}
      </button>
    </div>
  );
}

function SessionIndexLoupe({
  item,
  home,
  run,
  online,
  left,
  bottom,
}: {
  item: ReplyImageItem;
  home: FilesHome;
  run: string;
  online: boolean;
  left: number;
  bottom: number;
}) {
  const info = useFileInfo(item.path);
  const goneByInfo = info?.kind === FileRefKind.MISSING || info?.kind === FileRefKind.OUTSIDE;
  const effectivePath = info && !goneByInfo && info.path ? info.path : item.path;
  const state = useSessionImage(goneByInfo ? null : { session: home.session, run, path: effectivePath }, !goneByInfo, online, true);
  if (goneByInfo || state?.status === "missing" || state?.status === "offline" || state?.status === "error" || state?.status === "tooLarge") {
    return null;
  }
  return (
    <div
      className={`simg-loupe ${state?.status === "ready" ? "ready" : "shimmer"}`}
      style={{ left, bottom }}
      aria-hidden="true"
    >
      {state?.status === "ready" && <img src={state.url} alt="" draggable={false} />}
    </div>
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

  const ready = state?.status === "ready" && state.width > 0 && state.height > 0;
  const ratio = ready && state?.status === "ready" ? state.width / state.height : undefined;
  const compact = multi && Boolean(ready && state?.status === "ready" && isTallStageImage(state.width, state.height));
  const ultraLong = Boolean(ready && state?.status === "ready" && isUltraLongImage(state.width, state.height));
  const resolvedPath = state?.status === "ready" ? state.resolvedPath : effectivePath;
  const folder = state?.status === "ready" ? state.folder : info?.folder ?? "";

  return (
    <figure
      className={`simg-card ${multi ? "in-row" : "single"} ${compact ? "compact" : ""} ${ultraLong ? "ultra-long" : ""} ${state?.status === "ready" ? "ready" : "loading shimmer"}`}
      style={
        multi && ratio && !compact && !ultraLong
          ? ({ flexGrow: Math.max(0.55, Math.min(2.0, ratio)), flexBasis: 0, "--img-ratio": String(ratio) } as CSSProperties)
          : !multi && ready && !ultraLong && state?.status === "ready"
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
          {ultraLong && (
            <span className="simg-long-veil" data-testid="session-image-long-badge">
              <span>{`↕ ${t("files.longImage")}`}</span>
            </span>
          )}
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
