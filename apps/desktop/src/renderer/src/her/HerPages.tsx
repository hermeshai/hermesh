import { useCallback, useEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import type { BrowserStatus, ChatGptStatus, ModelsListResult, ProviderSummary, SkillsFilesResult, SkillsListResult, StorageUsage, WebSearchSource, WebSearchStatus } from "@hermesh/her-protocol";
import type { SessionView } from "@hermesh/her-client";
import { agoText } from "@hermesh/core";
import { Icon } from "../icons";
import { herAnswers } from "../skew";
import { errorText, useNow, useStore } from "../store";
import type { SendOp } from "./Approval";
import {
  allSitesEdits,
  brokenSkills,
  browsers,
  bySize,
  chromeName,
  controlEdits,
  defaultEdits,
  extensionEdits,
  providers as group,
  searchEnabledEdits,
  searchMoved,
  searchOrderEdits,
  searchSwitchEdits,
  siteEdits,
  siteLevels,
  siteOrigin,
  skillItems,
  type Browsers,
  type SiteLevel,
  type SkillItem,
} from "./abilities";
import { useHer } from "./context";
import { reachable } from "./environments";
import { ModelRows } from "./menus";

// The pages Her's page opens beside the conversation (docs/her-panel-demo.html), each with a way back to it: her
// model with the environment's model sign-ins and providers, a provider's models, her environment with its storage,
// her skills and one of them, web search, and the browsers. They read and write what her environment's console does
// (docs/her-server.md §10), from the same answers; adding a key, uploading a skill or connecting the owner's Chrome is
// done on the console's page, which opens at that page.

/** A page in the panel: back, its title, and close. */
function Shell({ title, back, close, testid, children }: { title: string; back: () => void; close: () => void; testid: string; children: ReactNode }) {
  const { t } = useTranslation("her");
  return (
    <aside className="her-panel rtn hpp" aria-label={title} data-testid={testid}>
      <div className="her-panel-top drag rtn-top rtn-sub">
        <button className="tb" title={t("routines.back")} aria-label={t("routines.back")} onClick={back}>
          <Icon name="chevL" />
        </button>
        <span className="rtn-tt">{title}</span>
        <button className="tb" title={t("panel.close")} aria-label={t("panel.close")} onClick={close}>
          <Icon name="x" />
        </button>
      </div>
      <div className="her-panel-body">{children}</div>
    </aside>
  );
}

/** What her says, read again when she says it changed (`events`) and after each change made here. */
function useRead<T>(read: (() => Promise<T>) | null, events: string[]): { value: T | null; error: string | null; again: () => Promise<void> } {
  const her = useHer();
  const rpc = her.env.rpc;
  const [value, setValue] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const again = useCallback(async () => {
    if (!read) return;
    try {
      setValue(await read());
      setError(null);
    } catch (e) {
      setError(errorText(e));
    }
  }, [read]);
  useEffect(() => {
    if (!rpc) return;
    void again();
    const offs = events.map((e) => rpc.on(e as "config/changed", () => void again()));
    return () => offs.forEach((off) => off());
  }, [rpc, again]);
  return { value, error, again };
}

/** A page's body while her is not read yet, has failed, or is too old to answer. */
function Waiting({ error, method }: { error: string | null; method: string }) {
  const { t: tc } = useTranslation("common");
  const her = useHer();
  if (herAnswers(her.machine, method) === false) {
    return (
      <div className="rtn-empty" data-testid="needs-update">
        <b>{tc("needsUpdate.title")}</b>
        {tc("needsUpdate.body", { machine: her.machine.machineName })}
      </div>
    );
  }
  return error ? <p className="set-note">{error}</p> : <div className="rtn-empty"><span className="mini-spin" /></div>;
}

function Switch({ on, label, disabled, testid, onChange }: { on: boolean; label: string; disabled?: boolean; testid?: string; onChange: (on: boolean) => void }) {
  return <button className={`sw ${on ? "on" : ""}`} role="switch" aria-checked={on} aria-label={label} disabled={disabled} data-testid={testid} onClick={() => onChange(!on)} />;
}

/** Writes her user file, then reads again: a failed write says why, and the page goes back. */
function useWrite(again: () => Promise<void>) {
  const her = useHer();
  const st = useStore();
  return async (edits: { key_path: string; value: unknown }[], done?: string) => {
    try {
      await her.call("config/write", { layer: "user", edits });
      if (done) st.show(done);
    } catch (e) {
      st.show(errorText(e));
    }
    await again();
  };
}

// ---- Model ----

/** A Token Plan by the name the app gives it, else the provider's own. */
function providerName(p: ProviderSummary, t: TFunction<"her">, zh: boolean): string {
  if (p.plan) return t(`plans.${p.id}.name`, { defaultValue: p.name });
  return (zh ? p.names?.zh : p.names?.en) ?? p.name;
}

/** The host of a service added by its address, for its row. */
function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

/** What a provider's row says: where its key comes from, how many of its models the menu shows, when its list was read. */
function providerLine(p: ProviderSummary, models: ModelsListResult, t: TFunction<"her">, now: Date): string {
  const source = p.custom ? hostOf(p.custom.base_url) : p.credential === "stored" ? t("hp.source.stored") : p.credential === "env" ? t("hp.source.env", { env: p.credential_source ?? "" }) : p.credential === "config" ? "models.toml" : p.credential === "command" ? t("hp.source.command") : "";
  const mine = models.models.filter((m) => m.provider === p.id);
  const shown = t("hp.shownOf", { shown: p.listing?.shown ?? mine.filter((m) => m.shown).length, count: p.listing?.models ?? mine.length });
  const updated = p.listing?.refreshing ? t("hp.refreshing") : p.listing?.fetched_at ? t("hp.updated", { when: agoText(new Date(p.listing.fetched_at), now) }) : "";
  return [source, shown, updated].filter(Boolean).join(" · ");
}

/**
 * Her's model and how hard she thinks, then where the environment's models come from: a ChatGPT subscription, Token
 * Plans and providers' keys, each provider opening its models. A new key or plan is added on the console's Models page.
 */
export function ModelPage({ view, op, onBack, onClose }: { view: SessionView | null; op: SendOp; onBack: () => void; onClose: () => void }) {
  const { t, i18n } = useTranslation("her");
  const her = useHer();
  const st = useStore();
  const now = useNow(60_000);
  const zh = i18n.language.startsWith("zh");
  const models = her.env.models;
  const read = useCallback(() => her.call("chatgpt/status", {}), [her.call]);
  const gpt = useRead<ChatGptStatus>(herAnswers(her.machine, "chatgpt/status") === false ? null : read, ["models/changed"]);
  const [signingIn, setSigningIn] = useState(false);
  const g = group(models.providers);
  const status = gpt.value;
  const ready = status?.connection?.state === "ready";
  const gptModels = models.models.filter((m) => m.provider === status?.connection?.provider_id);
  const signIn = () => {
    setSigningIn(true);
    const ended = window.hermesh ? window.hermesh.signInHerChatGpt(her.machine.machineId) : Promise.reject(new Error("no bridge"));
    ended.then(
      (r) => r.state === "connected" && st.show(t("hp.chatgptSignedIn")),
      (e: unknown) => st.show(errorText(e)),
    ).finally(() => {
      setSigningIn(false);
      void gpt.again();
    });
  };
  const forget = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("hp.forgetTitle"),
        message: t("hp.forgetMessage", { machine: her.machine.machineName }),
        button: t("hp.forget"),
        run: () => her.call("connections/remove", { connection_id: "chatgpt-api" }).then(() => gpt.again()),
      },
    });
  const row = (p: ProviderSummary) => (
    <button key={p.id} className="frow bpn-go mrow" data-testid={`her-provider-${p.id}`} onClick={() => her.setPanel({ kind: "provider", id: p.id })}>
      <span className="hp-mark">{providerName(p, t, zh).slice(0, 1).toUpperCase()}</span>
      <span className="ft">
        <span className="fn">{providerName(p, t, zh)}</span>
        <span className="fdesc">{providerLine(p, models, t, now)}</span>
        {p.listing?.error && <span className="fdesc warn">{t("hp.refreshFailed", { message: p.listing.error.message })}</span>}
      </span>
      <Icon name="chevR" />
    </button>
  );
  return (
    <Shell title={t("model.model")} back={onBack} close={onClose} testid="her-models-page">
      <div className="form">
        {view && (
          <section>
            <ModelRows below model={view.info.model} thinking={view.info.thinking} onModel={(ref) => void op({ type: "set_model", model: ref })} onThinking={(l) => void op({ type: "set_model", model: view.info.model, thinking: l })} />
          </section>
        )}
        {herAnswers(her.machine, "chatgpt/status") !== false && (
          <section>
            <h3 className="fhead">{t("hp.subscription")}</h3>
            <div className="fgroup">
              <div className="frow mrow" data-testid="her-chatgpt">
                <span className="hp-mark">C</span>
                <span className="ft">
                  <span className="fn">ChatGPT</span>
                  <span className={`fdesc ${status?.connection?.state === "needs_login" ? "warn" : ""}`}>
                    {signingIn || status?.state === "pending"
                      ? t("hp.chatgptWaiting")
                      : ready
                        ? t("hp.chatgptReady", { shown: gptModels.filter((m) => m.shown).length, count: gptModels.length })
                        : status?.connection?.state === "needs_login"
                          ? t("hp.chatgptExpired")
                          : t("hp.chatgptNote")}
                  </span>
                </span>
                {signingIn ? (
                  <button className="btn" onClick={() => window.hermesh?.cancelHerChatGpt(her.machine.machineId)}>
                    {t("sheet.cancel")}
                  </button>
                ) : ready ? (
                  <button className="btn" onClick={forget}>
                    {t("hp.forget")}
                  </button>
                ) : (
                  <button className="btn primary" disabled={!reachable(her.machine)} data-testid="her-chatgpt-sign-in" onClick={signIn}>
                    {t("hp.signIn")}
                  </button>
                )}
              </div>
            </div>
            <p className="set-note">{t("hp.subscriptionNote")}</p>
          </section>
        )}
        {(g.addedPlans.length > 0 || g.plansLeft.length > 0) && (
          <section>
            <h3 className="fhead">Token Plan</h3>
            <div className="fgroup">
              {g.addedPlans.map(row)}
              {g.plansLeft.length > 0 && (
                <button className="frow bpn-go" onClick={() => her.openConsole("models")}>
                  <span className="fl">{t("hp.planConnect")}</span>
                  <span className="fv">
                    <Icon name="chevR" />
                  </span>
                </button>
              )}
            </div>
          </section>
        )}
        <section>
          <h3 className="fhead">{t("hp.api")}</h3>
          <div className="fgroup">
            {g.keyed.map(row)}
            <button className="frow bpn-go hp-link" onClick={() => her.openConsole("models")}>
              <span className="fl">{t("hp.addApi")}</span>
            </button>
          </div>
          <p className="set-note">{t("hp.keysNote", { machine: her.machine.machineName })}</p>
        </section>
      </div>
    </Shell>
  );
}

/** A provider's models, each in the model menu or not; refreshing its list, and removing its key. */
export function ProviderPage({ id, onBack, onClose }: { id: string; onBack: () => void; onClose: () => void }) {
  const { t, i18n } = useTranslation("her");
  const her = useHer();
  const st = useStore();
  const now = useNow(60_000);
  const models = her.env.models;
  const p = models.providers.find((x) => x.id === id);
  const mine = models.models.filter((m) => m.provider === id);
  const [query, setQuery] = useState("");
  const found = query ? mine.filter((m) => `${m.name} ${m.id}`.toLowerCase().includes(query.toLowerCase())) : mine;
  const remove = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("hp.removeTitle", { name: p ? providerName(p, t, i18n.language.startsWith("zh")) : id }),
        message: p?.plan ? t("hp.removePlan") : p?.custom ? t("hp.removeCustom") : t("hp.removeKey", { machine: her.machine.machineName }),
        button: t("hp.remove"),
        // A service added by its address goes altogether, its saved key with it.
        run: () => (p?.custom ? her.call("providers/remove", { id }) : her.call("auth/delete", { provider: id })).then(onBack),
      },
    });
  return (
    <Shell title={p ? providerName(p, t, i18n.language.startsWith("zh")) : id} back={onBack} close={onClose} testid="her-provider-page">
      {!p ? (
        <div className="rtn-empty">
          <span className="mini-spin" />
        </div>
      ) : (
        <div className="form">
          <p className="set-note hp-lead">{providerLine(p, models, t, now)}</p>
          {p.credential === "env" && <p className="set-note">{t("hp.fromEnv", { machine: her.machine.machineName, env: p.credential_source ?? "" })}</p>}
          <input className="field hp-find" placeholder={t("hp.findModels")} value={query} spellCheck={false} onChange={(e) => setQuery(e.target.value)} />
          <section>
            <div className="fgroup">
              {found.map((m) => (
                <div key={m.id} className="frow">
                  <span className="ft">
                    <span className="fn">{m.name}</span>
                    <span className="fdesc mono">{m.id.split("/").slice(1).join("/")}</span>
                  </span>
                  <Switch on={m.shown} label={m.name} testid={`her-model-shown-${m.id}`} onChange={(v) => void her.run(() => her.call("models/show", { model: m.id, shown: v }))} />
                </div>
              ))}
              {found.length === 0 && <div className="frow fdesc">{t("model.noMatch")}</div>}
            </div>
            <p className="set-note">{t("hp.shownNote")}</p>
          </section>
          <section className="fgroup">
            {(!p.plan || p.plan.dynamic_models) && (
              <button className="frow bpn-go hp-link" disabled={!!p.listing?.refreshing} onClick={() => void her.run(() => her.call("models/refresh", { provider: id }))}>
                <span className="fl">{p.listing?.refreshing ? t("hp.refreshing") : t("hp.refresh")}</span>
              </button>
            )}
            {(p.credential === "stored" || p.custom) && (
              <>
                <button className="frow bpn-go hp-link" onClick={() => her.openConsole("models")}>
                  <span className="fl">{p.credential === "stored" ? t("hp.replaceKey") : t("hp.setKey")}</span>
                </button>
                <button className="frow bpn-end" data-testid="her-provider-remove" onClick={remove}>
                  {t("hp.remove")}
                </button>
              </>
            )}
          </section>
        </div>
      )}
    </Shell>
  );
}

// ---- Environment ----

/** Where Her runs: her environments, another machine, the storage of her data there, its console, and turning her off there. */
export function EnvironmentPage({ onBack, onClose }: { onBack: () => void; onClose: () => void }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const st = useStore();
  const m = her.machine;
  const read = useCallback(() => her.call("storage/usage", {}), [her.call]);
  const storage = useRead<StorageUsage>(herAnswers(m, "storage/usage") === false ? null : read, []);
  return (
    <Shell title={t("hp.environment")} back={onBack} close={onClose} testid="her-environment-page">
      <div className="form">
        <section>
          <h3 className="fhead">{t("hp.runsOn")}</h3>
          <div className="fgroup">
            {her.envs.map((e) => (
              <button key={e.machineId} className="frow bpn-go" aria-pressed={e.machineId === m.machineId} data-testid={`her-env-${e.machineName}`} onClick={() => her.choose(e.machineId)}>
                <span className="ft">
                  <span className="fn">{e.machineName}</span>
                  <span className="fdesc">
                    {reachable(e) ? t("env.online") : t("env.offlineShort")}
                    {(e.her?.unread ?? 0) > 0 ? ` · ${t("hp.unread", { count: e.her!.unread })}` : ""}
                  </span>
                </span>
                {e.machineId === m.machineId && <Icon name="check" className="hp-check" />}
              </button>
            ))}
            <button className="frow bpn-go hp-link" data-testid="her-env-add" onClick={() => her.setSheet({ kind: "add" })}>
              <span className="fl">{t("page.addEnvironment")}</span>
            </button>
          </div>
        </section>
        <section>
          <div className="fgroup">
            <button className="frow bpn-go" data-testid="her-storage" disabled={herAnswers(m, "storage/usage") === false} onClick={() => her.setPanel({ kind: "storage" })}>
              <span className="fl">{t("hp.storage")}</span>
              <span className="fv">
                <span className="hp-val">{storage.value ? bytes(storage.value.total) : ""}</span>
                <Icon name="chevR" />
              </span>
            </button>
            <button className="frow bpn-go" data-testid="her-console" onClick={() => her.openConsole()}>
              <span className="fl">{t("page.manage", { name: m.machineName })}</span>
              <span className="fv">
                <Icon name="ext" />
              </span>
            </button>
          </div>
          <p className="set-note">{t("hp.consoleNote", { machine: m.machineName })}</p>
        </section>
        <section className="fgroup">
          <button className="frow bpn-end" data-testid="her-turn-off" onClick={() => st.set({ sheet: { kind: "herOff", machineId: m.machineId, machineName: m.machineName } })}>
            {t("panel.turnOff", { machine: m.machineName })}
          </button>
        </section>
      </div>
    </Shell>
  );
}

/** Bytes as people read them: "1.8 GB". */
function bytes(n: number): string {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${i === 0 ? v : v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

const kindColors: Record<string, string> = { conversations: "#5a68cf", sent: "#25938d", files: "#c4881f", browser: "#8e5bd6", cache: "#8a8d98", trash: "#e5484d", memory: "#e8508c", other: "#3a9651" };

/** Where Her's data is on her machine, that it is the only copy, and how much of each kind there is. Only to read. */
export function StoragePage({ onBack, onClose }: { onBack: () => void; onClose: () => void }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const st = useStore();
  const read = useCallback(() => her.call("storage/usage", {}), [her.call]);
  const { value: s, error } = useRead<StorageUsage>(read, []);
  return (
    <Shell title={t("hp.storage")} back={onBack} close={onClose} testid="her-storage-page">
      {!s ? (
        <Waiting error={error} method="storage/usage" />
      ) : (
        <div className="form">
          <div className="hp-bar" aria-hidden="true">
            {bySize(s.parts)
              .filter((p) => p.bytes > 0)
              .map((p) => (
                <i key={p.kind} style={{ flexGrow: p.bytes, background: kindColors[p.kind] ?? kindColors.other }} />
              ))}
          </div>
          <section>
            <h3 className="fhead">{t("hp.holds", { size: bytes(s.total) })}</h3>
            <div className="fgroup">
              {bySize(s.parts).map((p) => (
                <div key={p.kind} className="frow">
                  <i className="hp-kind" style={{ background: kindColors[p.kind] ?? kindColors.other }} />
                  <span className="ft">
                    <span className="fn">{t(`hp.kind.${p.kind}.name`)}</span>
                    <span className="fdesc">{t(`hp.kind.${p.kind}.desc`)}</span>
                  </span>
                  <span className="fv hp-val">{bytes(p.bytes)}</span>
                </div>
              ))}
            </div>
            <p className="set-note">{t("hp.counted")}</p>
          </section>
          <section>
            <h3 className="fhead">{t("hp.location")}</h3>
            <div className="fgroup">
              <div className="frow">
                <span className="ft">
                  <span className="fn mono sel-text">{s.home}</span>
                  <span className="fdesc">{t(s.custom ? "hp.custom" : "hp.default")}</span>
                </span>
                <button className="btn" onClick={() => void navigator.clipboard.writeText(s.home).then(() => st.show(t("hp.copied")))}>
                  {t("hp.copyPath")}
                </button>
              </div>
            </div>
            <p className="set-note">{t("hp.onlyCopy")}</p>
          </section>
        </div>
      )}
    </Shell>
  );
}

// ---- Skills ----

/** Her's skills by group, each turned on or off here and opening its page; uploading one is on the console's Skills page. */
export function SkillsPage({ onBack, onClose }: { onBack: () => void; onClose: () => void }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const st = useStore();
  const read = useCallback(() => her.call("skills/list", {}), [her.call]);
  const { value, error, again } = useRead<SkillsListResult>(read, ["skills/changed", "config/changed"]);
  const items = skillItems(value);
  const broken = brokenSkills(value);
  const toggle = async (i: SkillItem, on: boolean) => {
    try {
      await her.call("skills/set_enabled", { path: i.dir, enabled: on });
      st.show(t(on ? "hp.skillOn" : "hp.skillOff", { name: i.entry.name }));
    } catch (e) {
      st.show(errorText(e));
    }
    await again();
  };
  const groupOf = (g: SkillItem["group"], title: string, note?: string) => {
    const list = items.filter((i) => i.group === g);
    if (!list.length && g !== "user") return null;
    return (
      <section key={g}>
        <h3 className="fhead">{title}</h3>
        <div className="fgroup">
          {list.length === 0 && (
            <div className="frow">
              <span className="ft">
                <span className="fn">{t("hp.noSkills")}</span>
                <span className="fdesc">{t("hp.noSkillsHint")}</span>
              </span>
            </div>
          )}
          {list.map((i) => (
            <div key={i.dir} className="frow hp-skill" data-testid={`her-skill-${i.entry.name}`}>
              <button className="ft hp-open" onClick={() => her.setPanel({ kind: "skill", dir: i.dir })}>
                <span className="fn mono">{i.entry.name}</span>
                <span className="fdesc">{i.on ? i.entry.description : t("hp.skillOffShort")}</span>
              </button>
              <Switch on={i.on} label={t("hp.turnOn", { name: i.entry.name })} testid={`her-skill-switch-${i.entry.name}`} onChange={(v) => void toggle(i, v)} />
            </div>
          ))}
        </div>
        {note && <p className="set-note">{note}</p>}
      </section>
    );
  };
  return (
    <Shell title={t("hp.skills")} back={onBack} close={onClose} testid="her-skills-page">
      {!value ? (
        <Waiting error={error} method="skills/list" />
      ) : (
        <div className="form">
          {broken.length > 0 && <p className="set-note warn">{t("hp.broken", { count: broken.length })}</p>}
          {groupOf("user", t("hp.installed"))}
          {groupOf("extra", t("hp.paths"), t("hp.pathsNote"))}
          {groupOf("system", t("hp.builtIn"), t("hp.builtInNote"))}
          <section className="fgroup">
            <button className="frow bpn-go hp-link" data-testid="her-skill-upload" onClick={() => her.openConsole("skills")}>
              <span className="fl">{t("hp.upload")}</span>
              <span className="fv">
                <Icon name="ext" />
              </span>
            </button>
          </section>
          <p className="set-note">{t("hp.uploadNote", { machine: her.machine.machineName })}</p>
        </div>
      )}
    </Shell>
  );
}

/** A skill: on or off, where it came from, how it is called, where it is, what it does, its files, and deleting it. */
export function SkillPage({ dir, onBack, onClose }: { dir: string; onBack: () => void; onClose: () => void }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const st = useStore();
  const read = useCallback(() => her.call("skills/list", {}), [her.call]);
  const { value, again } = useRead<SkillsListResult>(read, ["skills/changed", "config/changed"]);
  const item = skillItems(value).find((i) => i.dir === dir);
  const [files, setFiles] = useState<SkillsFilesResult | null>(null);
  const [open, setOpen] = useState<{ path: string; text: string | null } | null>(null);
  useEffect(() => {
    let live = true;
    her.call("skills/files", { path: dir }).then((r) => live && setFiles(r), () => {});
    return () => {
      live = false;
    };
  }, [dir, her.call]);
  if (value && !item) {
    return (
      <Shell title="" back={onBack} close={onClose} testid="her-skill-page">
        <div className="rtn-empty">
          <b>{t("hp.skillGone")}</b>
        </div>
      </Shell>
    );
  }
  const command = item ? `/skill:${item.entry.name}` : "";
  const remove = () =>
    item &&
    st.set({
      sheet: {
        kind: "confirm",
        title: t("hp.deleteTitle", { name: item.entry.name }),
        message: t("hp.deleteMessage", { machine: her.machine.machineName }),
        button: t("hp.delete"),
        run: () => her.call("skills/remove", { path: dir }).then(onBack),
        done: t("hp.deleted", { name: item.entry.name }),
      },
    });
  const show = async (path: string) => {
    if (open?.path === path) return setOpen(null);
    try {
      const r = await her.call("skills/read", { path: dir, file: path });
      setOpen({ path, text: r.binary ? null : r.text });
    } catch (e) {
      st.show(errorText(e));
    }
  };
  return (
    <Shell title={item?.entry.name ?? ""} back={onBack} close={onClose} testid="her-skill-page">
      {!item ? (
        <div className="rtn-empty">
          <span className="mini-spin" />
        </div>
      ) : (
        <div className="form">
          <section>
            <div className="fgroup">
              <div className="frow">
                <span className="fl">{t("hp.enabled")}</span>
                <Switch
                  on={item.on}
                  label={t("hp.enabled")}
                  testid="her-skill-on"
                  onChange={(v) => void her.run(() => her.call("skills/set_enabled", { path: dir, enabled: v }).then(again), t(v ? "hp.skillOn" : "hp.skillOff", { name: item.entry.name }))}
                />
              </div>
              <div className="frow">
                <span className="fl">{t("hp.sourceLabel")}</span>
                <span className="fv hp-val">{t(`hp.source.${item.source}`)}</span>
              </div>
              <div className="frow">
                <span className="fl">{t("hp.invoke")}</span>
                <span className="fv">
                  <span className="mono hp-val">{command}</span>
                  <button className="link" onClick={() => void navigator.clipboard.writeText(command).then(() => st.show(t("hp.copied2", { command })))}>
                    {t("hp.copy")}
                  </button>
                </span>
              </div>
            </div>
            {!item.entry.model_invocable && <p className="set-note">{t("hp.userOnly")}</p>}
            {item.source === "extra" && <p className="set-note">{t("hp.pathsOnly")}</p>}
          </section>
          <section>
            <h3 className="fhead">{t("hp.path")}</h3>
            <div className="fgroup">
              <div className="frow mono sel-text hp-path">{dir}</div>
            </div>
          </section>
          {item.entry.description && (
            <section>
              <h3 className="fhead">{t("hp.about")}</h3>
              <div className="fgroup">
                <div className="frow sel-text hp-about">{item.entry.description}</div>
              </div>
            </section>
          )}
          <section>
            <h3 className="fhead">{t("hp.files")}</h3>
            <div className="fgroup">
              {(files?.files ?? [])
                .filter((f) => !f.dir)
                .map((f) => (
                  <div key={f.path}>
                    <button className="frow bpn-go" aria-expanded={open?.path === f.path} onClick={() => void show(f.path)}>
                      <span className="fl mono">{f.path}</span>
                      <span className="fv hp-val">{bytes(f.size)}</span>
                    </button>
                    {open?.path === f.path && <pre className="hp-file sel-text">{open.text ?? t("hp.binary")}</pre>}
                  </div>
                ))}
              {!files && (
                <div className="frow">
                  <span className="mini-spin" />
                </div>
              )}
            </div>
          </section>
          {item.entry.removable && (
            <section className="fgroup">
              <button className="frow bpn-end" data-testid="her-skill-delete" onClick={remove}>
                {t("hp.deleteSkill")}
              </button>
            </section>
          )}
        </div>
      )}
    </Shell>
  );
}

// ---- Web search ----

/** What a search service says of itself: its cost and what it needs, as the console words it. */
function sourceLine(s: WebSearchSource, t: TFunction<"her">, now: Date): { text: string; warn?: boolean } {
  if (s.needs === "account" && !s.configured) return { text: t("hp.needsAccount", { name: s.name }) };
  if (s.failure) return { text: `${t(`hp.why.${s.failure.kind}`)} · ${agoText(new Date(s.failure.at_ms), now)}`, warn: true };
  if (s.key === "env" && s.key_env) return { text: t("hp.keyFromEnv", { env: s.key_env }) };
  return { text: t(`hp.sources.${s.id}.${s.key === "saved" ? "keyed" : "desc"}`, { defaultValue: "" }) };
}

/** Whether the model may search the web, and the services tried from the top down: each on or off, dragged into place. */
export function SearchPage({ onBack, onClose }: { onBack: () => void; onClose: () => void }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const now = useNow(60_000);
  const read = useCallback(() => her.call("web_search/status", {}), [her.call]);
  const { value: s, error, again } = useRead<WebSearchStatus>(read, ["config/changed"]);
  const write = useWrite(again);
  const [dragging, setDragging] = useState<number | null>(null);
  return (
    <Shell title={t("hp.search")} back={onBack} close={onClose} testid="her-search-page">
      {!s ? (
        <Waiting error={error} method="web_search/status" />
      ) : (
        <div className="form">
          <section>
            <div className="fgroup">
              <div className="frow">
                <span className="fl">{t("hp.searchMaster")}</span>
                <Switch on={s.enabled} label={t("hp.searchMaster")} testid="her-search-on" onChange={(v) => void write(searchEnabledEdits(v), t(v ? "hp.searchOn" : "hp.searchOff"))} />
              </div>
            </div>
            <p className="set-note">{t("hp.searchNote")}</p>
          </section>
          <section className={s.enabled ? "" : "hp-off"}>
            <h3 className="fhead">{t("hp.services")}</h3>
            <div className="fgroup">
              {s.sources.map((src, i) => {
                const line = sourceLine(src, t, now);
                return (
                  <div
                    key={src.id}
                    className={`frow hp-src ${dragging === i ? "drag" : ""}`}
                    data-testid={`her-search-${src.id}`}
                    draggable={s.enabled}
                    onDragStart={() => setDragging(i)}
                    onDragOver={(e) => e.preventDefault()}
                    onDrop={() => {
                      if (dragging !== null && dragging !== i) void write(searchOrderEdits(searchMoved(s, dragging, i)));
                      setDragging(null);
                    }}
                    onDragEnd={() => setDragging(null)}
                  >
                    <span className="hp-grip" aria-hidden="true">
                      <Icon name="menu" />
                    </span>
                    <span className="ft">
                      <span className="fn">
                        {src.name}
                        {src.id === s.current && <span className="badge ok">{t("hp.inUse")}</span>}
                      </span>
                      {line.text && <span className={`fdesc ${line.warn ? "warn" : ""}`}>{line.text}</span>}
                    </span>
                    {src.needs === "key" && !src.configured ? (
                      <button className="btn" onClick={() => her.openConsole("search")}>
                        {t("hp.addKey")}
                      </button>
                    ) : src.needs === "account" && !src.configured ? (
                      <button className="btn" onClick={() => her.setPanel({ kind: "model" })}>
                        {t("hp.connect")}
                      </button>
                    ) : (
                      <Switch on={src.enabled} label={t("hp.use", { name: src.name })} disabled={!s.enabled} testid={`her-search-switch-${src.id}`} onChange={(v) => void write(searchSwitchEdits(s, src.id, v))} />
                    )}
                  </div>
                );
              })}
            </div>
            <p className={`set-note ${s.sources.every((x) => !x.enabled) ? "warn" : ""}`}>{s.sources.every((x) => !x.enabled) ? t("hp.noneLeft") : t("hp.searchOrder")}</p>
          </section>
        </div>
      )}
    </Shell>
  );
}

// ---- Browser ----

/**
 * Whether Her controls browsers and where new pages open; the owner's Chrome on her machine (connected there, on the
 * console's Browser page) and the built-in browser; and the sites she may open and act on without asking.
 */
export function BrowserPage({ onBack, onClose }: { onBack: () => void; onClose: () => void }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const st = useStore();
  const read = useCallback(async () => {
    const [status, config] = await Promise.all([her.call("browser/status", {}), her.call("config/read", {})]);
    return browsers(status as BrowserStatus, config.config);
  }, [her.call]);
  const { value: b, error, again } = useRead<Browsers>(read, ["config/changed", "browser/changed"]);
  const write = useWrite(again);
  const [site, setSite] = useState("");
  const [level, setLevel] = useState<SiteLevel>("act");
  if (!b) {
    return (
      <Shell title={t("hp.browser")} back={onBack} close={onClose} testid="her-browser-page">
        <Waiting error={error} method="browser/status" />
      </Shell>
    );
  }
  const computer = t(b.os === "linux" ? "hp.linux" : "hp.mac");
  const add = () => {
    const origin = siteOrigin(site);
    if (!origin) return st.show(t("hp.badSite"));
    void write(siteEdits(origin, level), `${origin.replace(/^https:\/\//, "")}: ${t(`hp.level.${level}`)}`).then(() => setSite(""));
  };
  const badge = b.yours === "on" ? "hp.badge.on" : b.yours === "ready" ? "hp.badge.ready" : b.yours === "outdated" ? "hp.badge.outdated" : "hp.badge.off";
  return (
    <Shell title={t("hp.browser")} back={onBack} close={onClose} testid="her-browser-page">
      <div className="form">
        <section>
          <div className="fgroup">
            <div className="frow">
              <span className="fl">{t("hp.control")}</span>
              <Switch on={b.controlling} label={t("hp.control")} testid="her-browser-on" onChange={(v) => void write(controlEdits(v))} />
            </div>
          </div>
          <p className="set-note">{t("hp.controlNote")}</p>
        </section>
        <div className={b.controlling ? "" : "hp-off"} inert={!b.controlling}>
          <section>
            <div className="fgroup">
              <div className="frow">
                <span className="fl">{t("hp.default")}</span>
                <span className="fv">
                  <select className="psel" value={b.opensInYours ? "yours" : "builtin"} disabled={b.yours !== "on"} onChange={(e) => void write(defaultEdits(e.target.value === "yours"))}>
                    <option value="builtin">{t("hp.builtin")}</option>
                    {b.yours === "on" && <option value="yours">{t("hp.yoursName", { name: b.chrome ? chromeName(b.chrome) : "Chrome" })}</option>}
                  </select>
                </span>
              </div>
            </div>
            <p className="set-note">{t(b.yours === "on" ? "hp.defaultNote" : "hp.defaultLocked")}</p>
          </section>
          <section>
            <h3 className="fhead">{t("hp.yours")}</h3>
            <div className="fgroup">
              <div className="frow">
                <span className="ft">
                  <span className="fn">
                    {t("hp.yours")}
                    <span className={`badge ${b.yours === "on" ? "ok" : ""}`}>{t(badge)}</span>
                  </span>
                  <span className="fdesc">{t(`hp.yoursNote.${b.yours}`, { computer })}</span>
                </span>
                {b.yours === "ready" ? (
                  <button className="btn primary" onClick={() => void write(extensionEdits(true, b.legacyBackend))}>
                    {t("hp.use2")}
                  </button>
                ) : b.yours === "off" || b.yours === "outdated" ? (
                  <button className="btn" onClick={() => her.openConsole("browser/connect")}>
                    {t("hp.connect")}
                  </button>
                ) : (
                  <button className="btn" onClick={() => void write(extensionEdits(false, b.legacyBackend))}>
                    {t("hp.stopUsing")}
                  </button>
                )}
              </div>
            </div>
          </section>
          <section>
            <h3 className="fhead">{t("hp.builtin")}</h3>
            <div className="fgroup">
              <div className="frow fdesc hp-about">
                {b.program ? t(b.window ? "hp.builtinWindow" : "hp.builtinNoWindow", { program: b.program, computer }) : t("hp.noChrome", { computer })}
              </div>
            </div>
          </section>
          <section>
            <h3 className="fhead">{t("hp.sites")}</h3>
            <div className="fgroup">
              <div className="frow">
                <span className="ft">
                  <span className="fn">{t("hp.allSites")}</span>
                  <span className="fdesc">{t(`hp.allSitesDesc.${b.allSites}`)}</span>
                </span>
                <span className="fv">
                  <select className="psel" data-testid="her-browser-all-sites" value={b.allSites} onChange={(e) => void write(allSitesEdits(e.target.value as SiteLevel))}>
                    {(["ask", "navigate", "act"] as SiteLevel[]).map((l) => (
                      <option key={l} value={l}>
                        {t(`hp.level.${l}`)}
                      </option>
                    ))}
                  </select>
                </span>
              </div>
            </div>
          </section>
          <section>
            <h3 className="fhead">{t("hp.exceptions")}</h3>
            <div className="fgroup">
              {b.exceptions.length === 0 && <div className="frow fdesc">{t("hp.noExceptions")}</div>}
              {b.exceptions.map(([origin, l]) => (
                <div key={origin} className="frow">
                  <span className="fl">{origin.replace(/^https:\/\//, "")}</span>
                  <span className="fv">
                    <select className="psel" value={l} onChange={(e) => void write(siteEdits(origin, e.target.value as SiteLevel))}>
                      {siteLevels.map((x) => (
                        <option key={x} value={x}>
                          {t(`hp.level.${x}`)}
                        </option>
                      ))}
                    </select>
                    <button className="tb" title={t("hp.removeSite", { site: origin })} aria-label={t("hp.removeSite", { site: origin })} onClick={() => void write(siteEdits(origin, null))}>
                      <Icon name="x" />
                    </button>
                  </span>
                </div>
              ))}
              <div className="frow hp-add-site">
                <input className="field" placeholder="example.com" value={site} spellCheck={false} data-testid="her-browser-site" onChange={(e) => setSite(e.target.value)} onKeyDown={(e) => e.key === "Enter" && add()} />
                <select className="psel" value={level} onChange={(e) => setLevel(e.target.value as SiteLevel)}>
                  {siteLevels.map((x) => (
                    <option key={x} value={x}>
                      {t(`hp.level.${x}`)}
                    </option>
                  ))}
                </select>
                <button className="btn" disabled={!site.trim()} data-testid="her-browser-site-add" onClick={add}>
                  {t("hp.add")}
                </button>
              </div>
            </div>
            <p className="set-note">{t("hp.sitesNote")}</p>
          </section>
        </div>
      </div>
    </Shell>
  );
}
