import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { CatalogModel, CustomEndpoint, ModelSummary } from "@hermesh/her-protocol";
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
  /** A service added by its address: its models can be told what they are like. */
  custom?: CustomEndpoint;
};

/** "2 of 3 models shown": a source's models as its row and the dialog say them. */
export function useModelCounts(provider: string | undefined) {
  const { models } = useHer();
  const mine = (models?.models ?? []).filter((m) => m.provider === provider);
  return { count: mine.length, shown: mine.filter((m) => m.shown).length };
}

/**
 * What a model of a custom endpoint is like, for one nothing describes (custom-endpoint-design
 * §7.3): a model of the public catalog it is like, or its window, images and thinking. Saved
 * with models/configure; "automatic" forgets it.
 */
function ModelConfig({ model, onDone }: { model: ModelSummary; onDone: () => void }) {
  const { t } = useTranslation("settings");
  const her = useHer();
  const bare = model.id.slice(model.provider.length + 1);
  const [query, setQuery] = useState(bare);
  const [found, setFound] = useState<CatalogModel[]>([]);
  const [context, setContext] = useState(model.known ? String(model.context_window) : "");
  const [images, setImages] = useState(model.images);
  const [reasoning, setReasoning] = useState(model.reasoning);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const call = her.call;
  useEffect(() => {
    const q = query.trim();
    if (!q) {
      setFound([]);
      return;
    }
    let live = true;
    const timer = setTimeout(() => {
      void call("models/catalog", { query: q, limit: 6 }).then(
        (r) => live && setFound(r.models),
        () => live && setFound([]),
      );
    }, 150);
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [query, call]);
  const save = async (params: Record<string, unknown>) => {
    setBusy(true);
    setError("");
    try {
      await her.call("models/configure", { model: model.id, ...params });
      await her.refreshModels();
      her.show(t("models.manage.saved"));
      onDone();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  const window = Number(context.replace(/[,_\s]/g, "").replace(/k$/i, "000"));
  return (
    <div className="mm-config">
      <label className="mm-config-label">
        {t("models.manage.likeLabel")}
        <input className="field" type="search" value={query} placeholder={t("models.manage.likePlaceholder")} onChange={(e) => setQuery(e.target.value)} />
      </label>
      {found.length > 0 && (
        <div className="mm-suggest" role="list">
          {found.map((c) => (
            <button key={c.id} type="button" className="mm-suggest-row" disabled={busy} onClick={() => void save({ canonical: c.id })}>
              <span>{c.name}</span>
              <span className="mm-meta">{[c.id, c.context_window ? t("models.manage.context", { size: tokensText(c.context_window) }) : ""].filter(Boolean).join(" · ")}</span>
            </button>
          ))}
        </div>
      )}
      <p className="mm-config-or">{t("models.manage.orFill")}</p>
      <div className="mm-config-fields">
        <label className="mm-config-label">
          {t("models.manage.contextLabel")}
          <input className="field" inputMode="numeric" value={context} placeholder={t("models.manage.contextPlaceholder")} onChange={(e) => setContext(e.target.value)} />
        </label>
        <label className="mm-config-switch">
          <button type="button" className={`sw ${images ? "on" : ""}`} role="switch" aria-checked={images} onClick={() => setImages(!images)} />
          {t("models.manage.imagesLabel")}
        </label>
        <label className="mm-config-switch">
          <button type="button" className={`sw ${reasoning ? "on" : ""}`} role="switch" aria-checked={reasoning} onClick={() => setReasoning(!reasoning)} />
          {t("models.manage.reasoningLabel")}
        </label>
      </div>
      {error && <p className="mrow-err">{error}</p>}
      <div className="mm-config-actions">
        <button type="button" className="btn" disabled={busy} onClick={() => void save({ clear: true })}>
          {t("models.manage.clear")}
        </button>
        <button type="button" className="btn" disabled={busy} onClick={onDone}>
          {t("models.cancel")}
        </button>
        <button
          type="button"
          className="btn primary"
          disabled={busy || (context.trim() !== "" && !(window > 0))}
          onClick={() => void save({ context_window: context.trim() ? window : null, images, reasoning })}
        >
          {t("models.manage.save")}
        </button>
      </div>
    </div>
  );
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
  const [configuring, setConfiguring] = useState<string | null>(null);
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
          <div key={m.id} className={`mm-row${m.shown ? "" : " off"}${configuring === m.id ? " configuring" : ""}`} role="listitem">
            <div className="mm-main">
              <div className="mm-name">
                {m.name}
                {!m.known && <span className="mm-tag">{t("models.manage.unknown")}</span>}
                {m.deprecated && <span className="mm-tag warn">{t("models.manage.deprecated")}</span>}
                {isNew(m, now) && <span className="mm-tag new">{t("models.manage.new")}</span>}
              </div>
              <div className="mm-meta">
                {[
                  m.id.slice(source.id.length + 1),
                  m.known ? t("models.manage.context", { size: tokensText(m.context_window) }) : "",
                  m.canonical ? t(m.canonical.guessed ? "models.manage.guessed" : "models.manage.like", { name: m.canonical.name }) : "",
                  m.cost ? t("models.manage.price", { price: priceText(m.cost) }) : "",
                ]
                  .filter(Boolean)
                  .join(" · ")}
              </div>
              {source.custom && configuring !== m.id && (
                <button type="button" className={`mm-config-open${!m.known || m.canonical?.guessed ? "" : " quiet"}`} onClick={() => setConfiguring(m.id)}>
                  {t("models.manage.configure")}
                </button>
              )}
              {configuring === m.id && <ModelConfig model={m} onDone={() => setConfiguring(null)} />}
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
