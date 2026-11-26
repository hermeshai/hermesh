import { useContext, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { sameImagePath } from "@hermesh/her-client";
import { FileRefKind } from "@hermesh/proto/session";
import { Icon } from "../../icons";
import { machineName } from "../../Sidebar";
import { useStore } from "../../store";
import { DraftContext } from "../scope";
import { FileTag } from "./FileTag";
import { baseName, FilesHomeContext, FilesRunContext, sizeText, useFileInfo, type SessionFile } from "./files";
import { openFileInLightbox, openSessionLightbox, sessionImageMenu } from "./ImageLightbox";
import { ReplyImagesContext, useSessionImage, type ReplyImageItem } from "./images";

export type InlineImageSlotPayload = {
  items: ReplyImageItem[];
  fromLinks: boolean;
};

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
          onOpen={(resolvedPath) => {
            const gIdx = gallery.findIndex((g) => sameImagePath(g.path, it.path));
            if (gIdx >= 0) openSessionLightbox(home, run, gallery, gIdx);
            else openFileInLightbox(home, { session: home.session, run, path: resolvedPath }, gallery);
          }}
        />
      ))}
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
  home: { session: string; machine: string; local: boolean };
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
  const showCaption = multi && item.caption && item.caption.toLowerCase() !== name.toLowerCase();

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
          ? { flexGrow: Math.max(0.45, Math.min(2.4, ratio)), flexBasis: 0 }
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
        {state?.status === "ready" && <img src={state.url} alt={item.caption || name} draggable={false} />}
        {showCaption && <span className="simg-cap">{item.caption}</span>}
      </button>
    </figure>
  );
}
