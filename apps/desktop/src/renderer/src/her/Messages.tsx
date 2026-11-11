import { useEffect, useState, type CSSProperties } from "react";
import { useTranslation } from "react-i18next";
import { imageRatio, imageRows, type BotFile, type RpcClient } from "@hermesh/her-client";
import { errorText, useStore } from "../store";
import { Markdown } from "../session/Markdown";
import { Lightbox, Tile } from "./Composer";
import { useHer } from "./context";
import { sizeText } from "./format";
import { FilePreview, previews } from "./preview/FilePreview";

// What an assistant sends besides words.
// Its files are on its machine: pictures are read through the tunnel
// to show (files/read), a PDF or Markdown opens over the window
// (preview/FilePreview), other files are saved to Downloads on this Mac
// (HerDownload) and shown there.

/** Where a conversation's files are read: its connection and session. */
export type FileScope = { rpc: RpcClient | null; session: string; bot: string };

/** The pictures of a message, as data URLs; null for one that can't be read as a picture. */
function useImages(scope: FileScope, files: BotFile[]) {
  const [pictures, setPictures] = useState<Record<string, string | null>>({});
  const paths = files.filter((f) => f.image).map((f) => f.path);
  useEffect(() => {
    const rpc = scope.rpc;
    if (!rpc) return;
    let live = true;
    const keep = (path: string, picture: string | null) => live && setPictures((p) => ({ ...p, [path]: picture }));
    for (const path of paths) {
      if (pictures[path] !== undefined) continue;
      rpc.request("files/read", { session_id: scope.session, path })
        .then((read) => keep(path, read.image ? `data:${read.image.mime_type};base64,${read.image.data}` : null))
        .catch(() => keep(path, null));
    }
    return () => {
      live = false;
    };
  }, [scope.rpc, scope.session, paths.join("\0")]);
  return pictures;
}

/** Saves one of the assistant's files to Downloads and shows it there. */
export function useSaveFile(bot: string) {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  return (path: string, name: string) =>
    window.hermesh?.saveHerFile(her.machine.machineId, bot, path).then(
      () => st.show(t("files.saved", { name })),
      (e) => st.show(errorText(e)),
    );
}

/** A message of files: its words if it has any, its pictures together, then its other files together. */
export function SentFiles({ scope, files, text }: { scope: FileScope; files: BotFile[]; text?: string }) {
  const pictures = useImages(scope, files);
  const images = files.filter((f) => f.image && pictures[f.path] !== null);
  const others = files.filter((f) => !images.includes(f));
  const save = useSaveFile(scope.bot);
  const [shown, setShown] = useState<BotFile | null>(null);
  return (
    <div className="batt">
      {text !== undefined && (
        <div className="bbub">
          <Markdown text={text} />
        </div>
      )}
      {images.length > 0 && <ImageGroup files={images} pictures={pictures} />}
      {others.length > 0 && (
        <div className="bfiles">
          {others.map((f, i) => (
            <Tile key={i} name={f.name} meta={sizeText(f.size)} views={previews(f.name)} onOpen={() => (previews(f.name) ? setShown(f) : void save(f.path, f.name))} />
          ))}
        </div>
      )}
      {shown && <FilePreview scope={scope} path={shown.path} name={shown.name} onSave={() => void save(shown.path, shown.name)} onClose={() => setShown(null)} />}
    </div>
  );
}

/**
 * One picture with its own proportions, or several in rows of two or three: one height in a row,
 * so a chart keeps its axes. Its size is known before the picture is read, so nothing moves.
 */
function ImageGroup({ files, pictures }: { files: BotFile[]; pictures: Record<string, string | null> }) {
  const { t } = useTranslation("her");
  const [large, setLarge] = useState<number | null>(null);
  const alone = files.length === 1;
  let at = 0;
  const rows = imageRows(files.length).map((count) => {
    const row = { from: at, files: files.slice(at, at + count) };
    at += count;
    return row;
  });
  const shown = large === null ? undefined : files[large];
  const picture = shown && pictures[shown.path];
  return (
    <div className={`bims ${alone ? "one" : ""}`} style={alone ? ({ "--r": imageRatio(files[0], true), "--w": files[0].width ? `${files[0].width}px` : "480px" } as CSSProperties) : undefined}>
      {rows.map((row) => (
        <div key={row.from} className="bims-r">
          {row.files.map((f, i) => (
            <button key={i} className="bim" style={{ "--r": imageRatio(f, alone) } as CSSProperties} title={f.name} aria-label={t("composer.view", { name: f.name })} onClick={() => setLarge(row.from + i)}>
              {pictures[f.path] && <img src={pictures[f.path]!} alt="" draggable={false} />}
            </button>
          ))}
        </div>
      ))}
      {shown && picture && (
        <Lightbox
          src={picture}
          caption={alone ? shown.name : `${large! + 1} / ${files.length}  ·  ${shown.name}`}
          onStep={alone ? undefined : (d) => setLarge(((large ?? 0) + d + files.length) % files.length)}
          onClose={() => setLarge(null)}
        />
      )}
    </div>
  );
}

/**
 * A question, with answers to tap if they can be listed. A tapped answer is the owner's message like any
 * other; once they said anything, the options fold away and it reads as an ordinary exchange.
 */
export function Question({ text, options, open, onAnswer }: { text: string; options: string[]; open: boolean; onAnswer: (label: string) => Promise<boolean> }) {
  const [shown, setShown] = useState(open);
  const [sending, setSending] = useState(false);
  useEffect(() => {
    if (open) return setShown(true);
    const fold = setTimeout(() => setShown(false), 260);
    return () => clearTimeout(fold);
  }, [open]);
  const answer = async (label: string) => {
    setSending(true);
    if (!(await onAnswer(label))) setSending(false);
  };
  return (
    <div className={`bbub bq ${open && options.length > 0 ? "" : "done"}`}>
      <div className="bq-q">
        <Markdown text={text} />
      </div>
      {shown && options.length > 0 && (
        <div className={`bq-fold ${open ? "" : "shut"}`}>
          <div className="bq-opts">
            {options.map((label) => (
              <button key={label} className="bq-opt" disabled={!open || sending} onClick={() => void answer(label)}>
                {label}
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
