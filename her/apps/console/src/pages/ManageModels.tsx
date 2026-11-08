import { useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { ModelSummary } from "@hermesh/her-protocol";
import { Dialog } from "../components";
import { agoText, errorText, priceText, tokensText } from "../format";
import { useHer } from "../her";
import { Icon } from "../icons";

// Manage models: every model a provider offers, each
// with a switch for the model menu (models/show), and the list asked again on demand.

/** Where the models come from. */
export type ModelSource = {
  /** The provider the models are published under. */
  id: string;
  name: string;
  mark: ReactNode;
  /** Where the list came from, in words. */
  origin?: string;
  fetchedAt?: string | null;
  refreshing?: boolean;
  /** Why the last refresh failed. */
  failure?: string;
  /** Asks for the list again (models/refresh, connections/models). */
  refresh?: () => Promise<unknown>;
};

/** "2 of 3 models shown": a source's models as its row and the dialog say them. */
export function useModelCounts(provider: string | undefined) {
  const { models } = useHer();
  const mine = (models?.models ?? []).filter((m) => m.provider === provider);
  return { count: mine.length, shown: mine.filter((m) => m.shown).length };
}

/** Released within the last month. */
function isNew(m: ModelSummary, now: Date): boolean {
  return !!m.released && now.getTime() - new Date(m.released).getTime() < 30 * 86_400_000;
}

export function ManageModels({ source, footer, onClose }: { source: ModelSource; footer?: ReactNode; onClose: () => void }) {
  const { t } = useTranslation("settings");
  const her = useHer();
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState("");
  const { count, shown } = useModelCounts(source.id);
  const now = new Date();
  const all = her.models?.models;
  const models = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (all ?? [])
      .filter((m) => m.provider === source.id)
      .filter((m) => !q || m.name.toLowerCase().includes(q) || m.id.toLowerCase().includes(q))
      // Known before unknown, then newest; a switch never moves its row.
      .sort((a, b) => Number(b.known) - Number(a.known) || (b.released ?? "").localeCompare(a.released ?? "") || a.name.localeCompare(b.name));
  }, [all, source.id, query]);
  const run = async (what: string, work: () => Promise<unknown>) => {
    setBusy(what);
    setError("");
    try {
      await work();
      await her.refreshModels();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(null);
    }
  };
  const status = [t("models.manage.shownOf", { shown, count }), source.origin ?? "", source.fetchedAt ? t("models.manage.updated", { when: agoText(new Date(source.fetchedAt), now) }) : ""].filter(Boolean).join(" · ");
  const refreshing = busy === "refresh" || source.refreshing;
  return (
    <Dialog label={t("models.manage.title", { name: source.name })} width={620} className="models-manage" onClose={onClose}>
      <div className="provider-dialog-top">
        {source.mark}
        <span>{t("models.manage.title", { name: source.name })}</span>
        <button className="provider-icon-button" aria-label={t("models.close")} onClick={onClose}>
          <Icon name="x" />
        </button>
      </div>
      <div className="mm-bar">
        <input className="field" type="search" placeholder={t("models.manage.search")} value={query} onChange={(e) => setQuery(e.target.value)} />
        {source.refresh && (
          <button className="btn" disabled={busy !== null || source.refreshing} onClick={() => void run("refresh", source.refresh!)}>
            {refreshing ? t("models.manage.refreshing") : t("models.manage.refresh")}
          </button>
        )}
      </div>
      <p className="mm-status">{status}</p>
      {source.failure && <p className="mrow-err">{t("models.manage.failed", { message: source.failure })}</p>}
      {error && <p className="mrow-err">{error}</p>}
      <div className="mm-list" role="list">
        {models.map((m) => (
          <div key={m.id} className={`mm-row${m.shown ? "" : " off"}`} role="listitem">
            <div className="mm-main">
              <div className="mm-name">
                {m.name}
                {!m.known && <span className="mm-tag">{t("models.manage.unknown")}</span>}
                {m.deprecated && <span className="mm-tag warn">{t("models.manage.deprecated")}</span>}
                {isNew(m, now) && <span className="mm-tag new">{t("models.manage.new")}</span>}
              </div>
              <div className="mm-meta">
                {[m.id.slice(source.id.length + 1), m.known ? t("models.manage.context", { size: tokensText(m.context_window) }) : "", m.cost ? t("models.manage.price", { price: priceText(m.cost) }) : ""].filter(Boolean).join(" · ")}
              </div>
            </div>
            <button
              className={`sw ${m.shown ? "on" : ""}`}
              role="switch"
              aria-checked={m.shown}
              aria-label={t("models.manage.inMenu", { name: m.name })}
              disabled={busy === m.id}
              onClick={() => void run(m.id, () => her.call("models/show", { model: m.id, shown: !m.shown }))}
            />
          </div>
        ))}
        {models.length === 0 && <p className="mm-empty">{t("models.manage.none")}</p>}
      </div>
      {footer}
    </Dialog>
  );
}
