import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { BotMemoryPage, BotMemoryResult, BotSummary } from "@hermesh/her-protocol";
import { Icon } from "../icons";
import { Markdown } from "../session/Markdown";
import { errorText } from "../store";
import { Tile } from "./Composer";
import { useHer } from "./context";
import { useSaveFile, type FileScope } from "./Messages";
import { FilePreview, previews } from "./preview/FilePreview";

// What an assistant keeps in its memory (docs/her-memory.md), in the place its settings take. MEMORY.md
// is shown as written, as Markdown, whatever form the memory takes: the assistant writes it, and how is
// its prompt's business, not this page's. Changing it goes through the chat.

/**
 * The memory as bots/memory gives it, read again whenever the assistant's last message changes and when
 * `reload` is called (after it was cleared); null until then.
 */
export function useMemory(bot: BotSummary): { memory: BotMemoryResult | null; error: string | null; reload: () => void } {
  const her = useHer();
  const [memory, setMemory] = useState<BotMemoryResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [round, setRound] = useState(0);
  const last = JSON.stringify(bot.last ?? null);
  useEffect(() => {
    let live = true;
    her.call("bots/memory", { bot_id: bot.id }).then(
      (m) => live && (setMemory(m), setError(null)),
      (e) => live && setError(errorText(e)),
    );
    return () => {
      live = false;
    };
  }, [bot.id, last, round]);
  return { memory, error, reload: () => setRound((n) => n + 1) };
}

export const isEmpty = (m: BotMemoryResult) => m.text.trim() === "" && (m.pages ?? []).length === 0;

export function MemoryPage({ bot, files, onBack, onClose }: { bot: BotSummary; files: FileScope; onBack: () => void; onClose: () => void }) {
  const { t } = useTranslation("her");
  const { memory, error } = useMemory(bot);
  return (
    <aside className="her-panel rtn mem" aria-label={t("memory.title")}>
      <div className="her-panel-top drag rtn-top rtn-sub">
        <button className="tb" title={t("memory.back")} aria-label={t("memory.back")} onClick={onBack}>
          <Icon name="chevL" />
        </button>
        <span className="rtn-tt">{t("memory.title")}</span>
        <button className="tb" title={t("panel.close")} aria-label={t("panel.close")} onClick={onClose}>
          <Icon name="x" />
        </button>
      </div>
      <div className="her-panel-body">
        {error && !memory && <p className="set-note">{t("memory.failed", { error })}</p>}
        {memory && isEmpty(memory) && (
          <div className="rtn-empty">
            <b>{t("memory.emptyTitle")}</b>
            {t("memory.emptyBody")}
          </div>
        )}
        {memory && !isEmpty(memory) && (
          <div className="form">
            {memory.text.trim() !== "" && (
              <div className="fgroup mem-doc sel-text">
                <Markdown text={memory.text} />
              </div>
            )}
            {(memory.pages ?? []).length > 0 && <Pages bot={bot} files={files} pages={memory.pages ?? []} />}
            <p className="set-note">{t("memory.note")}</p>
          </div>
        )}
      </div>
    </aside>
  );
}

/** The other files of the memory folder: Markdown and PDF open over the window, as in the conversation; the rest go to Downloads. */
function Pages({ bot, files, pages }: { bot: BotSummary; files: FileScope; pages: BotMemoryPage[] }) {
  const { t, i18n } = useTranslation("her");
  const save = useSaveFile(bot.id);
  const [shown, setShown] = useState<BotMemoryPage | null>(null);
  const day = (at?: string | null) => (at ? new Date(at).toLocaleDateString(i18n.language, { month: "short", day: "numeric" }) : undefined);
  return (
    <section>
      <h3 className="fhead">{t("memory.pages")}</h3>
      <div className="bfiles mem-pages">
        {pages.map((p) => (
          <Tile key={p.path} name={p.name} meta={day(p.modified_at)} views={previews(p.name)} onOpen={() => (previews(p.name) ? setShown(p) : void save(p.path, p.name))} />
        ))}
      </div>
      {shown && <FilePreview scope={files} path={shown.path} name={shown.name} onSave={() => void save(shown.path, shown.name)} onClose={() => setShown(null)} />}
    </section>
  );
}
