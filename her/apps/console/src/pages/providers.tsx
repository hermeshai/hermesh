import { Fragment, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { RpcError } from "@hermesh/her-client";
import type { ProviderSummary, ProvidersAddResult } from "@hermesh/her-protocol";
import alibabacloud from "../assets/providers/alibabacloud.svg?raw";
import anthropic from "../assets/providers/anthropic.svg?raw";
import deepseek from "../assets/providers/deepseek.svg?raw";
import gemini from "../assets/providers/gemini.svg?raw";
import kimi from "../assets/providers/kimi.svg?raw";
import ollama from "../assets/providers/ollama.svg?raw";
import openai from "../assets/providers/openai.svg?raw";
import openrouter from "../assets/providers/openrouter.svg?raw";
import qwen from "../assets/providers/qwen.svg?raw";
import tencentcloud from "../assets/providers/tencentcloud.svg?raw";
import volcengine from "../assets/providers/volcengine.svg?raw";
import zai from "../assets/providers/zai.svg?raw";
import zhipu from "../assets/providers/zhipu.svg?raw";
import { Dialog } from "../components";
import { agoText, errorText } from "../format";
import { useHer, type Her } from "../her";
import { i18n } from "../i18n";
import { Icon } from "../icons";
import { ManageModels, useModelCounts } from "./ManageModels";

// The providers that take an API key: their
// marks, the tiles to pick one, and the form that gives her its key (auth/set). The key goes
// to her's credential store on the Her Server; the page keeps nothing.

/** The providers' own marks (assets/providers/README.md), by provider id. */
const marks: Record<string, string> = {
  anthropic,
  deepseek,
  google: gemini,
  kimi,
  ollama,
  openai,
  openrouter,
  qwen,
  glm: zhipu,
  "bailian-token-plan": alibabacloud,
  "tencent-token-plan": tencentcloud,
  "volcengine-agent-plan": volcengine,
  "volcengine-coding-plan": volcengine,
  "zai-coding-plan": zai,
  "zhipu-coding-plan": zhipu,
};

/** A provider's mark: its own for the known ones (a variant endpoint as its provider), a code glyph for a custom endpoint, else its initial. */
export function Brand({ id: given, name }: { id: string; name?: string }) {
  const id = given === "deepseek-anthropic" ? "deepseek" : given;
  // A mark with gradients names them; each use gets its own names.
  const uid = useId().replace(/[^\w-]/g, "");
  if (id === "custom") {
    return (
      <span className="provider-brand brand-custom" aria-hidden="true">
        <Icon name="code" />
      </span>
    );
  }
  const mark = marks[id];
  if (mark) return <span className={`provider-brand brand-${id}`} aria-hidden="true" dangerouslySetInnerHTML={{ __html: mark.replaceAll("__uid__", uid) }} />;
  return (
    <span className={`provider-brand brand-${id} brand-initial`} aria-hidden="true">
      {(name ?? id).charAt(0).toUpperCase()}
    </span>
  );
}

/** Shorter names than the catalog's, for the provider tiles. */
const tileNames: Record<string, { zh?: string; en?: string }> = {
  qwen: { zh: "阿里云百炼", en: "Alibaba Cloud" },
  glm: { zh: "智谱 GLM", en: "Zhipu GLM" },
  kimi: { zh: "Kimi", en: "Kimi" },
};

export function useTileName() {
  const { t, i18n: lang } = useTranslation("settings");
  const zh = lang.language.startsWith("zh");
  return (p: ProviderSummary) => (p.plan ? t(`models.plans.products.${p.id}.name` as "models.plans.title", { defaultValue: p.name }) : ((zh ? tileNames[p.id]?.zh : tileNames[p.id]?.en) ?? p.name));
}

/** Providers that take a key the person can type. */
export function keyProviders(providers: ProviderSummary[]): ProviderSummary[] {
  return providers.filter((p) => p.auth === "api_key");
}

/**
 * Saves a key with auth/set: her asks the provider first, and a refused key is not saved (its
 * error says so). A key her could not check is saved, and the page says it was not checked.
 * Returns whether it was.
 */
export async function saveKey(her: Her, provider: string, key: string, unverified?: string): Promise<boolean> {
  try {
    const result = await her.call("auth/set", { provider, api_key: key });
    if (!result.verified) her.show(unverified ?? i18n.t("settings:key.unverified"));
    return result.verified;
  } catch (e) {
    if (e instanceof RpcError && e.kind === "key_rejected") throw new Error(i18n.t("settings:key.rejected", { message: e.message }));
    throw e;
  }
}

/** The order the Token Plans are offered in; one not named here comes after them, by name. */
const planOrder = ["bailian-token-plan", "tencent-token-plan", "volcengine-agent-plan", "volcengine-coding-plan", "zai-coding-plan", "zhipu-coding-plan"];
const planRank = (p: ProviderSummary) => (planOrder.includes(p.id) ? planOrder.indexOf(p.id) : planOrder.length);

/** The Token Plans: a provider with product metadata, taking a key of its own. */
function tokenPlans(providers: ProviderSummary[]): ProviderSummary[] {
  return keyProviders(providers)
    .filter((p) => p.plan)
    .sort((a, b) => planRank(a) - planRank(b) || a.name.localeCompare(b.name));
}

/** The providers that take a key, or with `plans` the Token Plans, as tiles; `skip` leaves out ones already set up. `onCustom` adds the tile of a service of one's own. */
function ProviderTiles({ onPick, onCustom, skip = [], plans }: { onPick: (p: ProviderSummary) => void; onCustom?: () => void; skip?: string[]; plans?: boolean }) {
  const { t } = useTranslation("settings");
  const { models } = useHer();
  const label = useTileName();
  const all = models?.providers ?? [];
  // The Anthropic-compatible DeepSeek endpoint is the same account as DeepSeek.
  const providers = (plans ? tokenPlans(all) : keyProviders(all).filter((p) => !p.plan && !p.custom && p.id !== "deepseek-anthropic")).filter((p) => !skip.includes(p.id));
  // The catalog lists models for some providers only; the others are found once the key is in.
  const count = (p: ProviderSummary) => (models?.models ?? []).filter((m) => m.provider === p.id).length;
  return (
    <div className="provider-tiles">
      {providers.map((p) => {
        // A plan with a key stays in its place and says so; it is managed from its row.
        if (plans && p.credential !== "missing")
          return (
            <button key={p.id} className={`provider-choice choice-${p.id} added`} disabled>
              <span className="provider-wordmark">
                <Brand id={p.id} name={label(p)} />
                <strong>{label(p)}</strong>
              </span>
              <span className="provider-choice-meta">
                <span className="badge ok">{t("models.plans.added")}</span>
              </span>
            </button>
          );
        return (
          <button key={p.id} className={`provider-choice choice-${p.id}`} onClick={() => onPick(p)}>
            <span className="provider-wordmark">
              <Brand id={p.id} name={label(p)} />
              <strong>{label(p)}</strong>
            </span>
            <span className="provider-choice-meta">
              <span>{p.plan ? t(`models.plans.products.${p.id}.note` as "models.plans.title", { defaultValue: "API Key" }) : count(p) ? t("key.models", { count: count(p) }) : "API Key"}</span>
              <Icon name="chevR" />
            </span>
          </button>
        );
      })}
      {onCustom && !plans && (
        <button className="provider-choice choice-custom" onClick={onCustom}>
          <span className="provider-wordmark">
            <Brand id="custom" />
            <strong>{t("models.custom.tile")}</strong>
          </span>
          <span className="provider-choice-meta">
            <span>{t("models.custom.tileNote")}</span>
            <Icon name="chevR" />
          </span>
        </button>
      )}
    </div>
  );
}

/** The host of an address, for a custom endpoint's row. */
function hostOf(url: string | undefined): string {
  try {
    return url ? new URL(url).host : "";
  } catch {
    return url ?? "";
  }
}

const protocolNames: Record<string, string> = {
  "openai-chat": "OpenAI Chat Completions",
  "anthropic-messages": "Anthropic Messages",
  "openai-responses": "OpenAI Responses",
};

/** What a failed probe says, in the person's words (custom-endpoint-design §3.3). */
function probeErrorText(t: TFunction<"settings">, e: unknown): { text: string; reason?: string } {
  if (e instanceof RpcError) {
    if (e.kind === "key_rejected") return { text: t("models.custom.error.key_rejected", { message: e.message }), reason: "key_rejected" };
    const reason = typeof (e.data as { reason?: unknown } | null)?.reason === "string" ? (e.data as { reason: string }).reason : undefined;
    if (reason) return { text: t(`models.custom.error.${reason}` as "models.custom.error.unreachable", { message: e.message, defaultValue: e.message }), reason };
  }
  return { text: errorText(e) };
}

/** What the probe found and what to know about it. */
function ProbeNotes({ result }: { result: ProvidersAddResult }) {
  const { t } = useTranslation("settings");
  const { models } = useHer();
  const probe = result.probe;
  const builtin = probe.builtin ? (models?.providers.find((p) => p.id === probe.builtin)?.name ?? probe.builtin) : "";
  const both = probe.protocols.some((p) => p.api === "anthropic-messages") && probe.protocols.some((p) => p.api === "openai-chat");
  return (
    <div className="custom-result">
      <p className="custom-found">{probe.no_list ? t("models.custom.foundManual") : t("models.custom.found", { count: probe.models, known: probe.known })}</p>
      <p>{t("models.custom.protocols", { list: probe.protocols.map((p) => protocolNames[p.api] ?? p.api).join(t("models.custom.listSep")) })}</p>
      {both && <p>{t("models.custom.claudeMessages")}</p>}
      {probe.insecure && <p className="warn">{t("models.custom.insecure")}</p>}
      {builtin && <p className="warn">{t("models.custom.builtin", { name: builtin })}</p>}
      {probe.unsure?.includes("anthropic-messages") && <p className="warn">{t("models.custom.unsure")}</p>}
      {probe.note && <p className="warn">{t(`models.custom.note.${probe.note}` as "models.custom.note.bedrock_models", { defaultValue: "" })}</p>}
    </div>
  );
}

/**
 * A service of one's own by its address (custom-endpoint-design §3.1): her finds out what it
 * speaks and saves it only when it can be used (providers/add). The page keeps no key.
 */
function CustomEndpointEntry({ onBack, onDone }: { onBack: () => void; onDone?: () => void }) {
  const { t } = useTranslation("settings");
  const her = useHer();
  const [url, setUrl] = useState("");
  const [key, setKey] = useState("");
  const [name, setName] = useState("");
  const [api, setApi] = useState("");
  const [models, setModels] = useState("");
  const [needModels, setNeedModels] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [added, setAdded] = useState<ProvidersAddResult | null>(null);
  const [managing, setManaging] = useState(false);
  const card = useRef<HTMLFormElement>(null);
  useEffect(() => {
    void card.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, []);
  const connect = async () => {
    if (busy || !url.trim()) return;
    setBusy(true);
    setError("");
    try {
      const named = needModels ? models.split(/[\s,，]+/).map((m) => m.trim()).filter(Boolean) : [];
      const result = await her.call("providers/add", {
        base_url: url.trim(),
        api_key: key.trim() || undefined,
        name: name.trim() || undefined,
        api: api || undefined,
        models: named.length ? named : undefined,
      });
      setKey("");
      setAdded(result);
      await her.refreshModels();
    } catch (e) {
      const failed = probeErrorText(t, e);
      if (failed.reason === "no_models") setNeedModels(true);
      setError(failed.text);
    } finally {
      setBusy(false);
    }
  };
  if (added) {
    const p = added.provider;
    return (
      <div ref={card as never} className="connection-card key-entry" data-testid="custom-added">
        <div className="connection-provider">
          <Brand id="custom" />
          <div className="connection-provider-name">
            <h4>{p.name}</h4>
            <p>{hostOf(added.probe.base_url)}</p>
          </div>
        </div>
        <div className="provider-form">
          <ProbeNotes result={added} />
        </div>
        <div className="connection-card-footer">
          <span />
          <div className="key-actions">
            <button type="button" className="btn" onClick={() => setManaging(true)}>
              {t("models.custom.manage")}
            </button>
            <button type="button" className="btn primary" onClick={onDone ?? onBack}>
              {t("models.custom.done")}
            </button>
          </div>
        </div>
        {managing && (
          <ManageModels
            source={{ id: p.id, name: p.name, mark: <Brand id="custom" />, custom: p.custom ?? undefined }}
            footer={
              <div className="mm-foot">
                <ConnectionDetails provider={p} />
              </div>
            }
            onClose={() => setManaging(false)}
          />
        )}
      </div>
    );
  }
  return (
    <form
      ref={card}
      className="connection-card key-entry"
      onSubmit={(e) => {
        e.preventDefault();
        void connect();
      }}
    >
      <div className="connection-provider">
        <Brand id="custom" />
        <div className="connection-provider-name">
          <h4>{t("models.custom.title")}</h4>
          <p>{t("models.custom.subtitle")}</p>
        </div>
      </div>
      <div className="provider-form">
        <label>
          {t("models.custom.url")}
          <input className="field" type="url" autoComplete="off" spellCheck={false} autoFocus value={url} placeholder={t("models.custom.urlPlaceholder")} onChange={(e) => setUrl(e.target.value)} />
        </label>
        <label>
          {t("models.custom.key")}
          <input className="field" type="password" autoComplete="off" spellCheck={false} value={key} placeholder={t("models.custom.keyOptional")} onChange={(e) => setKey(e.target.value)} />
        </label>
        <label>
          {t("models.custom.name")}
          <input className="field" type="text" autoComplete="off" spellCheck={false} value={name} placeholder={t("models.custom.namePlaceholder")} onChange={(e) => setName(e.target.value)} />
        </label>
        {needModels && (
          <label>
            {t("models.custom.models")}
            <input className="field" type="text" autoComplete="off" spellCheck={false} autoFocus value={models} placeholder="model-a, model-b" onChange={(e) => setModels(e.target.value)} />
            <span className="provider-form-note">{t("models.custom.modelsNote")}</span>
          </label>
        )}
        <details className="plan-details">
          <summary>
            <Icon name="chevR" />
            {t("models.custom.advanced")}
          </summary>
          <label className="custom-protocol">
            {t("models.custom.protocol")}
            <select className="field" value={api} onChange={(e) => setApi(e.target.value)}>
              <option value="">{t("models.custom.protocolAuto")}</option>
              <option value="openai-chat">OpenAI Chat Completions</option>
              <option value="anthropic-messages">Anthropic Messages</option>
            </select>
          </label>
        </details>
        {error && (
          <p className="err" role="alert">
            {error}
          </p>
        )}
      </div>
      <div className="connection-card-footer">
        <span>
          <Icon name="lock" />
          {t("key.where")}
        </span>
        <div className="key-actions">
          <button type="button" className="btn" disabled={busy} onClick={onBack}>
            {t("key.back")}
          </button>
          <button type="submit" className="btn primary" disabled={busy || !url.trim() || (needModels && !models.trim())}>
            {busy ? t("models.custom.detecting") : t("models.custom.connect")}
          </button>
        </div>
      </div>
    </form>
  );
}

/** The header a key goes in, as a request carries it (models.toml's `auth_header` values). */
const headerNames: Record<string, string> = { bearer: "Authorization: Bearer", x_api_key: "x-api-key", api_key: "api-key" };

/** A custom endpoint's address and protocols, and asking it again (custom-endpoint-design §11). */
function ConnectionDetails({ provider: p }: { provider: ProviderSummary }) {
  const { t } = useTranslation("settings");
  const her = useHer();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const recheck = async () => {
    setBusy(true);
    setError("");
    try {
      await her.call("providers/update", { id: p.id, base_url: p.custom?.base_url });
      await her.refreshModels();
      her.show(t("models.custom.rechecked"));
    } catch (e) {
      setError(probeErrorText(t, e).text);
    } finally {
      setBusy(false);
    }
  };
  return (
    <details className="plan-details">
      <summary>
        <Icon name="chevR" />
        {t("models.custom.details")}
      </summary>
      <dl>
        <dt>{t("models.custom.address")}</dt>
        <dd>{p.custom?.base_url}</dd>
        {p.custom?.protocols.map((proto) => (
          <Fragment key={proto.api}>
            <dt>{protocolNames[proto.api] ?? proto.api}</dt>
            <dd>{proto.auth_header ? `${proto.base_url} · ${headerNames[proto.auth_header] ?? proto.auth_header}` : proto.base_url}</dd>
          </Fragment>
        ))}
      </dl>
      <button type="button" className="btn" disabled={busy} onClick={() => void recheck()}>
        {busy ? t("models.custom.detecting") : t("models.custom.recheck")}
      </button>
      {error && <p className="mrow-err">{error}</p>}
    </details>
  );
}

/** A provider's key, saved by her in its credential store (auth/set). */
function KeyEntry({ provider, onBack, onSaved }: { provider: ProviderSummary; onBack: () => void; onSaved?: (verified: boolean) => void }) {
  const { t } = useTranslation("settings");
  const her = useHer();
  const label = useTileName();
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const card = useRef<HTMLFormElement>(null);
  useEffect(() => {
    void card.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, []);
  const plan = provider.plan;
  const save = async () => {
    if (busy || !key.trim()) return;
    setBusy(true);
    setError("");
    try {
      // A service added by its address is probed again with the key: its list may be open to anyone.
      const verified = provider.custom
        ? (await her.call("providers/update", { id: provider.id, api_key: key.trim() }), true)
        : await saveKey(her, provider.id, key.trim(), plan ? t("models.plans.unverified") : undefined);
      setKey("");
      await her.refreshModels();
      onSaved?.(verified);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form
      ref={card}
      className="connection-card key-entry"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <div className="connection-provider">
        <Brand id={provider.custom ? "custom" : provider.id} name={label(provider)} />
        <div className="connection-provider-name">
          <h4>{label(provider)}</h4>
          <p>{plan ? t(`models.plans.products.${provider.id}.note` as "models.plans.title", { defaultValue: t("key.subtitle") }) : t("key.subtitle")}</p>
        </div>
      </div>
      <div className="provider-form">
        <label>
          API Key
          {plan && (
            <a href={plan.key_url} target="_blank" rel="noreferrer">
              {t("models.plans.getKey")}
              <Icon name="ext" />
            </a>
          )}
          <input
            className="field"
            type="password"
            autoComplete="off"
            spellCheck={false}
            autoFocus
            value={key}
            placeholder={plan ? t("models.plans.keyPlaceholder") : provider.key_env?.[0] ? t("key.placeholder", { env: provider.key_env[0] }) : ""}
            onChange={(e) => setKey(e.target.value)}
          />
        </label>
        {provider.credential === "env" && <p className="provider-form-note">{t("key.fromEnv", { env: provider.credential_source })}</p>}
        {plan && <PlanDetails provider={provider} />}
        {error && (
          <p className="err" role="alert">
            {error}
          </p>
        )}
      </div>
      <div className="connection-card-footer">
        <span>
          <Icon name="lock" />
          {t("key.where")}
        </span>
        <div className="key-actions">
          <button type="button" className="btn" disabled={busy} onClick={onBack}>
            {t("key.back")}
          </button>
          <button type="submit" className="btn primary" disabled={busy || !key.trim()}>
            {busy ? t("key.saving") : t("key.save")}
          </button>
        </div>
      </div>
    </form>
  );
}

/** A Token Plan's connection: where its requests go, by which protocol, and where its model list comes from. */
function PlanDetails({ provider: p }: { provider: ProviderSummary }) {
  const { t } = useTranslation("settings");
  return (
    <details className="plan-details">
      <summary>
        <Icon name="chevR" />
        {t("models.plans.details")}
      </summary>
      <dl>
        <dt>{t("models.plans.endpoint")}</dt>
        <dd>{p.plan?.base_url}</dd>
        <dt>{t("models.plans.protocol")}</dt>
        <dd>{p.plan?.api === "openai-chat" ? "OpenAI Chat Completions" : p.plan?.api}</dd>
        <dt>{t("models.plans.catalog")}</dt>
        <dd>{t(p.plan?.dynamic_models ? "models.plans.dynamicCatalog" : "models.plans.seedCatalog")}</dd>
      </dl>
      {!p.plan?.dynamic_models && <p>{t("models.plans.catalogNote")}</p>}
    </details>
  );
}

/** A sheet over Models: its title, the close button, and what it asks. */
function ConnectSheet({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  const { t } = useTranslation("settings");
  return (
    <Dialog label={title} width={560} className="models-add" onClose={onClose}>
      <div className="provider-dialog-top">
        <span>{title}</span>
        <button className="provider-icon-button" aria-label={t("models.close")} onClick={onClose}>
          <Icon name="x" />
        </button>
      </div>
      <div className="models-add-body">{children}</div>
    </Dialog>
  );
}

/** Adding a provider's key, or with `plans` a Token Plan's: pick one, paste its key. */
export function AddProvider({ skip, plans, onClose }: { skip?: string[]; plans?: boolean; onClose: () => void }) {
  const { t } = useTranslation("settings");
  const [provider, setProvider] = useState<ProviderSummary | null>(null);
  const [custom, setCustom] = useState(false);
  return (
    <ConnectSheet title={t(plans ? "models.plans.connect" : "models.add")} onClose={onClose}>
      {custom ? (
        <CustomEndpointEntry onBack={() => setCustom(false)} onDone={onClose} />
      ) : provider ? (
        <KeyEntry provider={provider} onBack={() => setProvider(null)} onSaved={onClose} />
      ) : (
        <>
          {plans && <p className="sub">{t("models.plans.chooseNote")}</p>}
          <ProviderTiles onPick={setProvider} onCustom={plans ? undefined : () => setCustom(true)} skip={skip} plans={plans} />
        </>
      )}
    </ConnectSheet>
  );
}

/** A provider her has a key for: where the key comes from, how many of its models the menu shows; its models can be managed, and a key saved here removed (auth/delete). */
export function KeyRow({ p }: { p: ProviderSummary }) {
  const { t } = useTranslation("settings");
  const her = useHer();
  const label = useTileName();
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [sheet, setSheet] = useState<"models" | "key" | null>(null);
  const listing = p.listing;
  const known = useModelCounts(p.id);
  const source = p.custom ? hostOf(p.custom.base_url) : t(`models.source.${p.credential}` as "models.source.stored", { env: p.credential_source ?? "", defaultValue: "" });
  const models = t("models.shownOf", { shown: listing?.shown ?? known.shown, count: listing?.models ?? known.count });
  const updated = listing?.refreshing ? t("models.manage.refreshing") : listing?.fetched_at ? t("models.manage.updated", { when: agoText(new Date(listing.fetched_at), new Date()) }) : "";
  const remove = async () => {
    setBusy(true);
    setError("");
    try {
      if (p.custom) await her.call("providers/remove", { id: p.id });
      else await her.call("auth/delete", { provider: p.id });
      await her.refreshModels();
    } catch (e) {
      setError(errorText(e));
      setBusy(false);
      setConfirming(false);
    }
  };
  return (
    <>
      <div className="frow mrow">
        <Brand id={p.custom ? "custom" : p.id} name={label(p)} />
        <div className="ft">
          <div className="fn">
            {label(p)}
            {p.plan && <span className="badge ok">{t("models.plans.added")}</span>}
          </div>
          <div className="fdesc">
            {confirming && p.plan ? t("models.plans.removeNote") : confirming && p.custom ? t("models.custom.removeNote") : [source, models, updated].filter(Boolean).join(" · ")}
          </div>
          {listing?.error && <div className="fdesc warn">{t("models.manage.failed", { message: listing.error.message })}</div>}
        </div>
        {!confirming && (
          <button className="btn" onClick={() => setSheet("models")}>
            {t("models.manage.open")}
          </button>
        )}
        {p.credential === "stored" || p.custom ? (
          confirming ? (
            <>
              <button className="btn" disabled={busy} onClick={() => setConfirming(false)}>
                {t("models.cancel")}
              </button>
              <button className="btn primary danger" disabled={busy} onClick={() => void remove()}>
                {t("models.confirmRemove")}
              </button>
            </>
          ) : (
            <button className="btn" onClick={() => setConfirming(true)}>
              {t("models.remove")}
            </button>
          )
        ) : (
          // Set outside the console: removed where it is set.
          <span className="fv-side" title={t(`models.elsewhere.${p.credential === "env" ? "env" : "config"}`, { env: p.credential_source ?? "" })}>
            <Icon name="lock" />
          </span>
        )}
      </div>
      {error && <p className="mrow-err">{error}</p>}
      {sheet === "models" && (
        <ManageModels
          source={{
            id: p.id,
            name: label(p),
            mark: <Brand id={p.custom ? "custom" : p.id} name={label(p)} />,
            custom: p.custom ?? undefined,
            origin: p.plan ? t(p.plan.dynamic_models ? "models.plans.dynamicCatalog" : "models.plans.seedCatalog") : listing ? t(`models.manage.source.${listing.source}`) : undefined,
            fetchedAt: listing?.fetched_at,
            refreshing: listing?.refreshing,
            failure: listing?.error?.message,
            // A plan with presets only has no list to ask for.
            refresh: !p.plan || p.plan.dynamic_models ? () => her.call("models/refresh", { provider: p.id }) : undefined,
          }}
          footer={
            p.custom ? (
              <div className="mm-foot">
                <ConnectionDetails provider={p} />
                {/* Keys change, and a service that checks the body first takes none at first. */}
                <button className="btn" onClick={() => setSheet("key")}>
                  {p.credential === "stored" ? t("models.replaceKey") : t("models.setKey")}
                </button>
              </div>
            ) : p.plan && (
              <div className="mm-foot">
                <PlanDetails provider={p} />
                {p.credential === "stored" && (
                  <button className="btn" onClick={() => setSheet("key")}>
                    {t("models.replaceKey")}
                  </button>
                )}
              </div>
            )
          }
          onClose={() => setSheet(null)}
        />
      )}
      {sheet === "key" && (
        <ConnectSheet title={p.custom && p.credential !== "stored" ? t("models.custom.setKeyTitle") : t("models.plans.replaceTitle")} onClose={() => setSheet(null)}>
          <KeyEntry
            provider={p}
            onBack={() => setSheet("models")}
            onSaved={(verified) => {
              setSheet(null);
              if (verified) her.show(p.custom && p.credential !== "stored" ? t("models.custom.keySaved") : t("models.plans.replaced"));
            }}
          />
        </ConnectSheet>
      )}
    </>
  );
}

/**
 * The Token Plans, a section of their own: the ones with a key as rows, then the entry that adds
 * another, its marks the services still on offer (two products of one service show one mark).
 */
export function TokenPlanSection() {
  const { t } = useTranslation("settings");
  const { models } = useHer();
  const name = useTileName();
  const [adding, setAdding] = useState(false);
  const plans = tokenPlans(models?.providers ?? []);
  if (!plans.length) return null;
  const added = plans.filter((p) => p.credential !== "missing");
  const left = plans.filter((p) => p.credential === "missing");
  const offered = left.filter((p, i) => left.findIndex((o) => marks[o.id] === marks[p.id]) === i);
  return (
    <section aria-label={t("models.plans.title")}>
      <h3 className="fhead">{t("models.plans.title")}</h3>
      {added.length > 0 && (
        <div className="fgroup">
          {added.map((p) => (
            <KeyRow key={p.id} p={p} />
          ))}
        </div>
      )}
      {left.length > 0 && (
        <div className="fgroup plan-entry-group">
          <button className="frow plan-entry" onClick={() => setAdding(true)}>
            <span className="plan-marks" aria-hidden="true">
              {offered.map((p) => (
                <span key={p.id} className="plan-mark">
                  <Brand id={p.id} name={name(p)} />
                </span>
              ))}
            </span>
            <span className="ft">
              <span className="fn">{t("models.plans.connect")}</span>
              <span className="fdesc">{t("models.plans.subtitle")}</span>
            </span>
            <Icon name="chevR" />
          </button>
        </div>
      )}
      {adding && <AddProvider plans onClose={() => setAdding(false)} />}
    </section>
  );
}
