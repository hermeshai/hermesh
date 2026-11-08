import { useEffect, useLayoutEffect, useRef, useState, type MouseEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { Icon } from "../icons";
import { addAttachments, fromFiles, pastedAttachments, pickFiles, type Attachment } from "./attachments";

/**
 * Where the owner writes to an assistant: one line while empty, "+", the text and Send, growing with what is
 * written. ↩ sends, ⇧↩ breaks the line, nothing is sent while an input method
 * composes. What is attached shows as tiles above the text; files dragged
 * anywhere over the window come here. An approval, or a card that says what
 * waits, sits above it.
 */
export function Composer({ above, onHeight, value, onChange, onSend, placeholder, canSend = true, attachments, onAttachments }: {
  above?: ReactNode;
  onHeight: (h: number) => void;
  value: string;
  onChange: (v: string) => void;
  onSend: () => void;
  placeholder: string;
  canSend?: boolean;
  attachments: Attachment[];
  onAttachments: (a: Attachment[]) => void;
}) {
  const { t } = useTranslation("her");
  const area = useRef<HTMLTextAreaElement>(null);
  const col = useRef<HTMLDivElement>(null);
  // What is attached now, for a paste or a drop whose files are read a moment later.
  const latest = useRef({ attachments, onAttachments });
  latest.current = { attachments, onAttachments };
  const attach = (more: Attachment[]) => {
    if (!more.length) return;
    latest.current.onAttachments(addAttachments(latest.current.attachments, more));
    area.current?.focus();
  };
  const dropping = useFileDrop((files) => void fromFiles(files).then(attach));
  const ready = canSend && (value.trim() !== "" || attachments.length > 0);
  useEffect(() => {
    if (!(document.activeElement instanceof HTMLInputElement)) area.current?.focus();
  }, []);
  useEffect(() => {
    const el = col.current;
    if (!el) return;
    const ro = new ResizeObserver(() => onHeight(el.offsetHeight));
    ro.observe(el);
    return () => {
      ro.disconnect();
      onHeight(0);
    };
  }, [onHeight]);
  // The text grows with what is written, up to ten lines, then scrolls.
  useLayoutEffect(() => {
    const el = area.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  }, [value]);
  return (
    <div className="cmp-zone">
      <div className="cmp-col" ref={col}>
        {above}
        <div
          className={`cmp ${dropping ? "dropping" : ""}`}
          onMouseDown={(e) => e.target instanceof HTMLElement && e.target.matches(".cmp,.cmp-row,.cmp-att") && (e.preventDefault(), area.current?.focus())}
          onPasteCapture={(e) => {
            const got = pastedAttachments(e.clipboardData);
            if (!got) return;
            e.preventDefault();
            e.stopPropagation();
            void got.then(attach);
          }}
        >
          {dropping && (
            <div className="cmp-drop" aria-hidden="true">
              <b>{t("composer.dropTitle")}</b>
              <span className="cmp-drop-hint">{t("composer.dropHint")}</span>
            </div>
          )}
          {attachments.length > 0 && (
            <div className="cmp-att">
              {attachments.map((a, i) => (
                <Tile
                  key={a.path}
                  name={a.name}
                  src={a.kind === "image" ? `data:${a.mime_type};base64,${a.data}` : undefined}
                  onRemove={() => (onAttachments(attachments.filter((_, j) => j !== i)), area.current?.focus())}
                />
              ))}
            </div>
          )}
          <div className="cmp-row">
            <button className="cmp-plus" title={t("composer.addFiles")} aria-label={t("composer.addFiles")} onClick={() => void pickFiles().then(attach)}>
              <Icon name="plus" />
            </button>
            <textarea
              ref={area}
              className="cmp-in"
              rows={1}
              value={value}
              placeholder={placeholder}
              aria-label={t("composer.label")}
              spellCheck={false}
              onChange={(e) => onChange(e.target.value)}
              onKeyDown={(e) => {
                // ⌘↩ is an approval's (Approval.tsx), not Send.
                if (e.key !== "Enter" || e.shiftKey || e.altKey || e.metaKey || e.ctrlKey || e.nativeEvent.isComposing) return;
                e.preventDefault();
                if (ready) onSend();
              }}
            />
            <button className="send" disabled={!ready} onClick={onSend} title={t("composer.send")} aria-label={t("composer.send")}>
              <Icon name="arrowUp" />
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

/**
 * Files dragged over the window: true while they are, so the composer shows it takes them; dropped
 * anywhere in the window, they go to `drop` (the window would otherwise open the file in itself).
 */
function useFileDrop(drop: (files: File[]) => void): boolean {
  const [over, setOver] = useState(false);
  const latest = useRef(drop);
  latest.current = drop;
  useEffect(() => {
    let depth = 0;
    const files = (e: DragEvent) => !!e.dataTransfer?.types.includes("Files");
    const enter = (e: DragEvent) => {
      if (!files(e)) return;
      depth += 1;
      setOver(true);
    };
    const move = (e: DragEvent) => {
      if (!files(e)) return;
      e.preventDefault();
      e.dataTransfer!.dropEffect = "copy";
    };
    const leave = (e: DragEvent) => {
      if (!files(e)) return;
      depth = Math.max(0, depth - 1);
      if (depth === 0) setOver(false);
    };
    const put = (e: DragEvent) => {
      if (!files(e)) return;
      e.preventDefault();
      depth = 0;
      setOver(false);
      latest.current([...e.dataTransfer!.files]);
    };
    window.addEventListener("dragenter", enter);
    window.addEventListener("dragover", move);
    window.addEventListener("dragleave", leave);
    window.addEventListener("drop", put);
    return () => {
      window.removeEventListener("dragenter", enter);
      window.removeEventListener("dragover", move);
      window.removeEventListener("dragleave", leave);
      window.removeEventListener("drop", put);
    };
  }, []);
  return over;
}

/** A file type's family, for its badge's colour. */
const families: [string, RegExp][] = [
  ["pdf", /^pdf$/],
  ["word", /^(docx?|rtf|odt|pages)$/],
  ["sheet", /^(xlsx?|xlsm|csv|tsv|ods|numbers)$/],
  ["slide", /^(pptx?|odp|key)$/],
  ["image", /^(png|jpe?g|gif|webp|svg|heic|bmp|ico|tiff?)$/],
  ["media", /^(mp3|m4a|wav|aac|flac|mp4|mov|m4v|avi|mkv|webm)$/],
  ["archive", /^(zip|rar|7z|tar|gz|dmg|pkg)$/],
  ["text", /^(md|markdown|txt|log|epub)$/],
  ["code", /^(js|jsx|ts|tsx|py|rs|go|java|kt|swift|c|cc|cpp|h|hpp|cs|rb|php|sh|zsh|json|ya?ml|toml|xml|html?|css|scss|sql|lua)$/],
];

/** A small page in the file type's colour with its extension on it. */
function FileBadge({ name }: { name: string }) {
  const ext = (/\.([A-Za-z0-9]{1,6})$/.exec(name)?.[1] ?? "").toLowerCase();
  const family = families.find(([, re]) => re.test(ext))?.[0] ?? "other";
  return (
    <span className={`tile-badge f-${family}`} aria-hidden>
      <Icon name="doc" />
      <b>{ext.slice(0, 4) || "FILE"}</b>
    </span>
  );
}

/**
 * One attachment, the same in the composer and in the conversation: a picture shows itself and opens
 * large over the window; a file shows its name and type (or `meta`), and opens as `onOpen` says.
 */
export function Tile({ name, src, meta, onOpen, onRemove, onContextMenu }: {
  name: string;
  /** A picture's data URL. */
  src?: string;
  /** A file's second line instead of its type. */
  meta?: string;
  onOpen?: () => void;
  onRemove?: () => void;
  onContextMenu?: (e: MouseEvent) => void;
}) {
  const { t } = useTranslation("her");
  const [large, setLarge] = useState(false);
  const dot = name.lastIndexOf(".");
  const ext = /\.([A-Za-z0-9]{1,6})$/.exec(name)?.[1]?.toUpperCase() ?? "";
  return (
    <div className={`tile ${src ? "img" : "file"}`} title={name} onContextMenu={onContextMenu}>
      <button className="tile-open" onClick={() => (src ? setLarge(true) : onOpen?.())} aria-label={src ? t("composer.view", { name }) : (onOpen ? t("files.save", { name }) : name)}>
        {src ? (
          <img src={src} alt="" />
        ) : (
          <>
            <FileBadge name={name} />
            <span className="tile-text">
              <span className="tile-name">
                <span className="tile-stem">{dot > 0 ? name.slice(0, dot) : name}</span>
                {dot > 0 && <span className="tile-ext">{name.slice(dot)}</span>}
              </span>
              {(meta ?? ext) && <span className="tile-kind">{meta ?? ext}</span>}
            </span>
          </>
        )}
      </button>
      {onRemove && (
        <button className="tile-x" aria-label={t("composer.remove", { name })} onClick={onRemove}>
          <Icon name="x" />
        </button>
      )}
      {large && src && <Lightbox src={src} onClose={() => setLarge(false)} />}
    </div>
  );
}

/**
 * A picture over the whole window; a click anywhere or Esc closes it. With onStep, one of several:
 * arrows and the ← → keys go through them. caption: what it is, under it.
 */
export function Lightbox({ src, caption, onStep, onClose }: { src: string; caption?: string; onStep?: (by: 1 | -1) => void; onClose: () => void }) {
  const { t } = useTranslation("her");
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
      else if (onStep && e.key === "ArrowLeft") onStep(-1);
      else if (onStep && e.key === "ArrowRight") onStep(1);
      else return;
      e.preventDefault();
      e.stopPropagation();
    };
    window.addEventListener("keydown", key, true);
    return () => window.removeEventListener("keydown", key, true);
  }, [onClose, onStep]);
  const step = (by: 1 | -1) => (e: MouseEvent) => {
    e.stopPropagation();
    onStep?.(by);
  };
  return createPortal(
    <div className={`lightbox ${onStep ? "many" : ""}`} role="dialog" aria-modal="true" onClick={onClose}>
      <img src={src} alt="" />
      {caption && <div className="lb-cap">{caption}</div>}
      {onStep && (
        <>
          <button className="lb-go prev" aria-label={t("composer.prevImage")} title={t("composer.prevImage")} onClick={step(-1)}>
            <Icon name="chevL" />
          </button>
          <button className="lb-go next" aria-label={t("composer.nextImage")} title={t("composer.nextImage")} onClick={step(1)}>
            <Icon name="chevR" />
          </button>
        </>
      )}
    </div>,
    document.body,
  );
}
