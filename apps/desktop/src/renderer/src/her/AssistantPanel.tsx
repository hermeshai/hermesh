import { useEffect, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { routineDot, type SessionView } from "@hermesh/her-client";
import type { AvatarHue, AvatarInput, BotSummary, McpServerView, ModelsListResult, PermissionMode } from "@hermesh/her-protocol";
import { local } from "../api";
import { Icon } from "../icons";
import { herAnswers } from "../skew";
import { useStore } from "../store";
import type { SendOp } from "./Approval";
import { BotAvatar } from "./Avatar";
import { useHer } from "./context";
import { modelTitle, permissionDesc, permissionTitle, thinkingTitle } from "./format";
import { GrantLine } from "./GrantLine";
import { isEmpty, MemoryPage, useMemory } from "./Memory";
import { MenuButton, MenuItem } from "./menus";
import type { FileScope } from "./Messages";
import { AvatarChoices, NAME_MAX, pickPicture } from "./sheets";

const modes: PermissionMode[] = ["manual", "accept-edits", "bypass-permissions"];

/** The three tabs of Her's page. */
export type HerTab = "work" | "abilities" | "settings";
const tabs: { id: HerTab; icon: string }[] = [
  { id: "work", icon: "checklist" },
  { id: "abilities", icon: "puzzle" },
  { id: "settings", icon: "gear" },
];

/** The tab chosen last: her page opens on it. */
function rememberedTab(): HerTab {
  try {
    const t = localStorage.getItem("her-tab");
    return t === "abilities" || t === "settings" ? t : "work";
  } catch {
    return "work";
  }
}

/**
 * Her's page, beside the conversation (docs/her-panel-demo.html): her avatar and name, changed where they are, and
 * three tabs, one kind of thing in each. Work: what she is doing (Progress), the files she sent, her scheduled tasks.
 * Abilities: what she remembers, her skills, the apps and services she is connected to, web search and the browsers.
 * Settings: her model, what she may do without asking (full access is signed, docs/her-server.md §8), whether she
 * alerts, her environment, and clearing her conversation. Each card is only a way in, with at most a word of how it
 * stands; what it holds is on the page it opens here, beside the conversation, with a way back.
 */
export function AssistantPanel({ bot, view, op, files, onClose }: { bot: BotSummary; view: SessionView | null; op: SendOp; files: FileScope; onClose: () => void }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const [tab, setTabState] = useState<HerTab>(rememberedTab);
  const setTab = (next: HerTab) => {
    setTabState(next);
    try {
      localStorage.setItem("her-tab", next);
    } catch {
      // A convenience: without storage Work shows first.
    }
  };
  const memoryShown = her.panel?.kind === "settings" && her.panel.page === "memory";
  if (memoryShown) return <MemoryPage bot={bot} files={files} onBack={() => her.setPanel({ kind: "settings" })} onClose={onClose} />;
  // Clearing the chat is asked first, and what stays is said (bot-mode-design §6.5.1).
  const clear = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("clear.title", { name: bot.name }),
        message: t("clear.message"),
        button: t("clear.button"),
        run: () => her.call("bots/clear", { bot_id: bot.id }),
        done: t("clear.done"),
      },
    });
  return (
    <aside className="her-panel bpn" aria-label={t("head.settings")} data-testid="her-panel">
      <div className="her-panel-top drag">
        <button className="tb" title={t("panel.close")} aria-label={t("panel.close")} onClick={onClose}>
          <Icon name="x" />
        </button>
      </div>
      <div className="her-panel-body">
        <BotHero key={bot.id} bot={bot} />
        <div className="hp-tabs" role="tablist" aria-label={t("hp.tabs")}>
          {tabs.map((x) => (
            <button key={x.id} role="tab" aria-selected={x.id === tab} className={x.id === tab ? "on" : ""} data-testid={`her-tab-${x.id}`} onClick={() => setTab(x.id)}>
              <Icon name={x.icon} />
              {t(`hp.tab.${x.id}`)}
            </button>
          ))}
        </div>
        <div className="hp-page" key={tab} role="tabpanel">
          {tab === "work" && <WorkCards bot={bot} />}
          {tab === "abilities" && <AbilityCards bot={bot} />}
          {tab === "settings" && (
            <div className="form">
              <section>
                <div className="fgroup">
                  <button className="frow bpn-go" data-testid="her-model-page" onClick={() => her.setPanel({ kind: "model" })}>
                    <span className="fl">{t("model.model")}</span>
                    <span className="fv">
                      <span className="hp-val">{view ? modelLine(view, her.env.models) : ""}</span>
                      <Icon name="chevR" />
                    </span>
                  </button>
                  {view && <PermissionRow bot={bot} mode={view.info.permission_mode} />}
                  <div className="frow">
                    <span className="fl">{t("panel.muted")}</span>
                    <div className="fv">
                      <button
                        className={`sw ${bot.muted ? "on" : ""}`}
                        role="switch"
                        aria-checked={!!bot.muted}
                        aria-label={t("panel.muted")}
                        onClick={() => void her.run(() => her.call("bots/update", { bot_id: bot.id, muted: !bot.muted }))}
                      />
                    </div>
                  </div>
                </div>
                <p className="set-note">{view ? t("panel.permissionNote", { desc: permissionDesc(view.info.permission_mode) }) : t("panel.mutedNote")}</p>
                <GrantLine m={her.machine} />
              </section>
              <section className="fgroup">
                <button className="frow bpn-go" data-testid="her-environment" onClick={() => her.setPanel({ kind: "environment" })}>
                  <span className="fl">{t("hp.environment")}</span>
                  <span className="fv">
                    <span className="hp-val">{her.machine.machineName}</span>
                    <Icon name="chevR" />
                  </span>
                </button>
              </section>
              <section className="fgroup">
                <button className="frow bpn-end" data-testid="her-clear" onClick={clear}>
                  {t("panel.clear")}
                </button>
              </section>
            </div>
          )}
        </div>
      </div>
    </aside>
  );
}

/** The model and, when it offers levels, how hard it thinks: "GPT-5.5 · High". */
function modelLine(view: SessionView, models: ModelsListResult): string {
  const { model, thinking } = view.info;
  if (!model) return "";
  const entry = models.models.find((m) => m.id === model);
  const title = modelTitle(model, models);
  return entry && entry.thinking_levels.length > 0 ? `${title} · ${thinkingTitle(thinking)}` : title;
}

/** What a card says of how its page stands, when it says anything. */
type Status = { text: string; tone?: "warn" | "run" } | null;

/** A way in: its name, at most a word of how it stands, and its glyph, on a pane of glass. */
function Card({ id, title, icon, status, wide, onOpen }: { id: string; title: string; icon: string; status?: Status; wide?: boolean; onOpen: () => void }) {
  const { t } = useTranslation("her");
  return (
    <button className={`hp-card ${wide ? "wide" : ""}`} data-testid={id} onClick={onOpen} aria-label={status ? t("hp.cardLabel", { title, status: status.text }) : title}>
      <b>{title}</b>
      {status && (
        <span className={`hp-st ${status.tone ?? ""}`}>
          {status.tone === "warn" && <i className="hp-dot" />}
          {status.text}
        </span>
      )}
      <Icon name={icon} className="hp-glyph" />
    </button>
  );
}

/** The cards of a tab: panes of glass over a field of Her's own colours. */
function Cards({ children }: { children: ReactNode }) {
  return (
    <div className="hp-cards">
      <div className="hp-field" aria-hidden="true" />
      {children}
    </div>
  );
}

/** Work: Progress (a page of its own), the files she sent and her scheduled tasks. */
function WorkCards({ bot }: { bot: BotSummary }) {
  const { t } = useTranslation("her");
  const { t: tc } = useTranslation("common");
  const her = useHer();
  const st = useStore();
  const { waits, running } = st.herAgents ?? { waits: 0, running: 0 };
  const older = herAnswers(her.machine, "deliveries/list") === false;
  const fresh = bot.new_deliveries ?? 0;
  const dot = routineDot(bot.routines);
  return (
    <Cards>
      <Card
        id="her-progress"
        wide
        title={t("progress.title")}
        icon="pulse"
        status={waits > 0 ? { text: t("progress.waiting", { count: waits }), tone: "warn" } : running > 0 ? { text: t("progress.atWork", { count: running }) } : null}
        onOpen={() => st.set({ selection: { kind: "progress", machine: her.machine.machineId } })}
      />
      <Card
        id="her-files"
        title={t("files.title")}
        icon="tray"
        status={older ? { text: tc("needsUpdate.mark"), tone: "warn" } : fresh > 0 ? { text: t("hp.newFiles", { count: fresh }) } : null}
        onOpen={() => her.setPanel({ kind: "files" })}
      />
      <Card
        id="her-routines"
        title={t("routines.title")}
        icon="clock"
        status={dot === "failed" ? { text: t("hp.lastFailed"), tone: "warn" } : dot === "running" ? { text: t("routines.running"), tone: "run" } : null}
        onOpen={() => her.setPanel({ kind: "routines", route: {} })}
      />
    </Cards>
  );
}

/** Abilities: her memory, her skills, the connectors as themselves, web search and the browsers. */
function AbilityCards({ bot }: { bot: BotSummary }) {
  const { t } = useTranslation("her");
  const { t: tc } = useTranslation("common");
  const her = useHer();
  const { memory, older } = useMemory(bot);
  const [searching, setSearching] = useState<boolean | null>(null);
  const [browsing, setBrowsing] = useState<boolean | null>(null);
  const rpc = her.env.rpc;
  useEffect(() => {
    if (!rpc) return;
    let live = true;
    const read = () => {
      if (herAnswers(her.machine, "web_search/status") !== false) her.call("web_search/status", {}).then((r) => live && setSearching(r.enabled), () => {});
      if (herAnswers(her.machine, "browser/status") !== false) {
        Promise.all([her.call("browser/status", {}), her.call("config/read", {})]).then(
          ([status, config]) => {
            const b = ((config.config as Record<string, unknown> | null)?.browser ?? {}) as Record<string, unknown>;
            const used = typeof b.extension === "boolean" ? b.extension : b.backend === "extension";
            if (live) setBrowsing(typeof b.enabled === "boolean" ? b.enabled : status.embedded || used);
          },
          () => {},
        );
      }
    };
    read();
    const off = rpc.on("config/changed", read);
    return () => {
      live = false;
      off();
    };
  }, [rpc, her.call]);
  const onOff = (v: boolean | null): Status => (v === null ? null : { text: t(v ? "hp.on" : "hp.off") });
  return (
    <Cards>
      <Card
        id="her-memory"
        title={t("memory.title")}
        icon="brain"
        status={older ? { text: tc("needsUpdate.mark"), tone: "warn" } : memory && isEmpty(memory) ? { text: t("panel.memoryNone") } : null}
        onOpen={() => her.setPanel({ kind: "settings", page: "memory" })}
      />
      <Card
        id="her-skills"
        title={t("hp.skills")}
        icon="read"
        status={herAnswers(her.machine, "skills/list") === false ? { text: tc("needsUpdate.mark"), tone: "warn" } : null}
        onOpen={() => her.setPanel({ kind: "skills" })}
      />
      <ConnectorTray />
      <Card id="her-search" title={t("hp.search")} icon="search" status={onOff(searching)} onOpen={() => her.setPanel({ kind: "search" })} />
      <Card id="her-browser" title={t("hp.browser")} icon="window" status={onOff(browsing)} onOpen={() => her.setPanel({ kind: "browser" })} />
    </Cards>
  );
}

/** A connector's two letters, for one without an icon of its own. */
function monogram(name: string): string {
  const parts = name.split(/[-_\s]+/).filter(Boolean);
  return (parts.length > 1 ? parts[0][0] + parts[1][0] : name.slice(0, 2)).toUpperCase();
}

/**
 * The connectors as themselves: each one on, marked where it needs the owner, then a place to add one. They are the
 * environment's, shared by its assistants, and managed on its console's Connectors page, which the tray opens; a
 * service opens its own.
 */
function ConnectorTray() {
  const { t } = useTranslation("her");
  const her = useHer();
  const [servers, setServers] = useState<McpServerView[] | null>(null);
  const rpc = her.env.rpc;
  useEffect(() => {
    if (!rpc) return;
    let live = true;
    const read = () => her.call("mcp/list", {}).then((r) => live && setServers(r.servers), () => {});
    read();
    const off = rpc.on("mcp/changed", read);
    return () => {
      live = false;
      off();
    };
  }, [rpc, her.call]);
  const on = servers?.filter((s) => s.enabled) ?? [];
  const needs = (s: McpServerView) => s.status.state === "failed" || s.status.state === "needs_login" || (!!s.connector && s.auth_state !== "logged_in");
  const attention = on.filter(needs).length;
  const room = on.length > 4 ? 3 : 4;
  // The console takes a page and an item of lowercase letters (main's her-console); others open its list.
  const open = (name?: string) => her.openConsole(name && /^[a-z]+$/.test(name) ? `connectors/${name}` : "connectors");
  return (
    <div className="hp-card wide hp-tray" data-testid="her-connectors-tray">
      <button className="hp-tray-head" data-testid="her-connectors" onClick={() => open()}>
        <b>{t("panel.connectors")}</b>
        {servers && (
          <span className={`hp-st ${attention > 0 ? "warn" : ""}`}>
            {attention > 0 && <i className="hp-dot" />}
            {attention > 0 ? t("panel.connectorsAttention", { count: attention }) : servers.length === 0 ? t("panel.connectorsNone") : t("panel.connectorsOn", { count: on.length })}
          </span>
        )}
        <Icon name="chevR" />
      </button>
      <div className="hp-apps">
        {on.slice(0, room).map((s) => (
          <button key={s.name} className={`hp-app ${needs(s) ? "at" : ""}`} title={s.name} onClick={() => open(s.name)}>
            {s.icon ? <img src={s.icon} alt="" /> : <i>{monogram(s.name)}</i>}
            <u>{s.name}</u>
          </button>
        ))}
        {on.length > room && (
          <button className="hp-app more" onClick={() => open()}>
            <i>+{on.length - room}</i>
            <u>{t("hp.more")}</u>
          </button>
        )}
        <button className="hp-app add" onClick={() => open()}>
          <i>
            <Icon name="plus" />
          </i>
          <u>{t("hp.add")}</u>
        </button>
      </div>
    </div>
  );
}

/** Its avatar, which on hover says it can be changed: a click opens the characters and colours in place, and a choice applies at once. */
function BotHero({ bot }: { bot: BotSummary }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const [picking, setPicking] = useState(false);
  const spec = bot.avatar;
  const hue: AvatarHue = spec.kind === "char" || spec.kind === "mono" ? spec.hue : "slate";
  const save = (avatar: AvatarInput) => her.run(() => her.call("bots/update", { bot_id: bot.id, avatar }));
  return (
    <div className="bpn-hero">
      <button className="bpn-av" title={t("panel.avatar")} aria-label={t("panel.avatar")} aria-expanded={picking} onClick={() => setPicking(!picking)}>
        <BotAvatar bot={bot} size={72} />
        <span className="bpn-av-edit" aria-hidden="true">
          <Icon name="edit" />
        </span>
      </button>
      <BotName bot={bot} />
      {picking && (
        <div className="fgroup bpn-pick">
          <AvatarChoices avatar={spec} hue={hue} name={bot.name} mark={bot.default} onChoose={(a) => void save(a)} />
          <button
            className="link"
            onClick={async () => {
              const got = await pickPicture();
              if (!got) return;
              if ("error" in got) return st.show(t(got.error === "too_large" ? "sheet.tooLarge" : "sheet.unreadable"));
              await save({ kind: "image", png: got.png });
            }}
          >
            {t("sheet.upload")}
          </button>
        </div>
      )}
    </div>
  );
}

/** Its name; a double click makes it a field. ↩ or leaving it keeps what is written, Esc what was there. */
function BotName({ bot }: { bot: BotSummary }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const [editing, setEditing] = useState(false);
  if (editing) {
    return (
      <NameField
        initial={bot.name}
        className="bpn-rename"
        end={(name) => {
          setEditing(false);
          const v = name?.trim();
          if (v && v !== bot.name) void her.run(() => her.call("bots/update", { bot_id: bot.id, name: v }));
        }}
      />
    );
  }
  return (
    <h3 className="bpn-name" title={t("panel.nameHint")} onDoubleClick={() => setEditing(true)}>
      {bot.name}
    </h3>
  );
}

/** A name edited in place: ↩ or leaving keeps what is written (end gets it), Esc puts it back (end gets null). */
function NameField({ initial, className, end }: { initial: string; className: string; end: (name: string | null) => void }) {
  const { t } = useTranslation("her");
  const [value, setValue] = useState(initial);
  const done = useRef(false);
  const finish = (keep: boolean) => {
    if (done.current) return;
    done.current = true;
    end(keep ? value : null);
  };
  return (
    <input
      className={className}
      aria-label={t("menu.rename")}
      autoFocus
      maxLength={NAME_MAX}
      value={value}
      spellCheck={false}
      onFocus={(e) => e.currentTarget.select()}
      onChange={(e) => setValue(e.target.value)}
      onClick={(e) => e.stopPropagation()}
      onDoubleClick={(e) => e.stopPropagation()}
      onKeyDown={(e) => {
        e.stopPropagation();
        if (e.nativeEvent.isComposing) return;
        if (e.key === "Enter") finish(true);
        else if (e.key === "Escape") finish(false);
      }}
      onBlur={() => finish(true)}
    />
  );
}

/**
 * What this assistant may do without asking, as a row of her Settings tab. Asking more is set at once; full access
 * asks first (AccessSheet). What it starts follows it, and the note under the group says so.
 */
function PermissionRow({ bot, mode }: { bot: BotSummary; mode: PermissionMode }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const choose = (m: PermissionMode) => {
    if (m === mode) return;
    if (m === "bypass-permissions") return her.setSheet({ kind: "access", bot });
    void her.run(() => local.setHerAssistantMode({ machineId: her.machine.machineId, botId: bot.id, permissionMode: m }));
  };
  return (
    <div className="frow">
      <span className="fl">{t("panel.permissionMode")}</span>
      <div className="fv">
        <MenuButton className="mb" align="right" below title={t("panel.permissionMode")} label={<><span className="lbl strong">{permissionTitle(mode)}</span><Icon name="chevD" /></>}>
          {(close) => modes.map((m) => <MenuItem key={m} checked={m === mode} label={permissionTitle(m)} desc={permissionDesc(m)} onClick={() => (close(), choose(m))} />)}
        </MenuButton>
      </div>
    </div>
  );
}
