import { useEffect, useState, type ReactNode } from "react";
import { Trans, useTranslation } from "react-i18next";
import type { McpServerView, McpToolsResult, SkillsFilesResult } from "@hermesh/her-protocol";
import { agoText, errorText } from "../format";
import { useHer, useNow } from "../her";
import { Icon } from "../icons";
import { openPage } from "../signin";
import { serverMenu, skillMenu, type ServerActions, type SkillActions } from "./Lists";
import { isCommand, statusOf, type SkillItem } from "./model";
import { MoreButton, ServerTile, SkillTile, Switch } from "./parts";
import { SkillContents } from "./SkillContents";
import type { Customize } from "./useCustomize";

// An item's own page: the tile and name large
// with the item's controls at the right, its facts in a row, a note when something needs the
// person, what it does under its own heading, then its tools or its files.

/** One of a page's facts: a small label over its value. */
function DFact({ k, grow, children }: { k: string; grow?: boolean; children: ReactNode }) {
  return (
    <div className={`cz-fact ${grow ? "grow" : ""}`}>
      <div className="k">{k}</div>
      <div className="v">{children}</div>
    </div>
  );
}

export function SkillPage({ c, a, item }: { c: Customize; a: SkillActions; item: SkillItem }) {
  const { t } = useTranslation("customize");
  const { rpc, show } = useHer();
  const [files, setFiles] = useState<SkillsFilesResult | null>(null);
  const [filesError, setFilesError] = useState<string | null>(null);
  useEffect(() => {
    if (!rpc) return;
    let current = true;
    rpc.request("skills/files", { path: item.dir }).then(
      (f) => current && (setFiles(f), setFilesError(null)),
      (e) => current && setFilesError(errorText(e)),
    );
    return () => {
      current = false;
    };
  }, [item.dir, rpc]);
  const name = item.entry.name;
  const count = files ? files.files.filter((f) => !f.dir).length : 0;
  const source = item.group === "system" ? t("skill.source.system") : item.from ? t("skill.sharedWith", { from: item.from }) : item.group === "extra" ? t("skill.source.extra") : t(`skill.source.${item.entry.source?.kind ?? "local"}`);
  const command = `/skill:${name}`;
  const copy = () => {
    void navigator.clipboard?.writeText(command);
    show(t("skill.copied", { command }));
  };
  // Only something to know, never to do: a quiet line under the facts it explains.
  const note = item.from ? (
    <Trans t={t} i18nKey="skill.sharedNote" values={{ folder: `~/${item.from}/skills` }} components={{ code: <code /> }} />
  ) : item.group === "extra" ? (
    t("skill.extraNote")
  ) : null;
  return (
    <div className={`cz-dpage ${item.on ? "" : "off"}`}>
      <div className="cz-dhead">
        <div className="cz-dtitle">
          <SkillTile size="xl" />
          <h1>{name}</h1>
          {item.group === "system" && <span className="badge">{t("skill.builtIn")}</span>}
          <div className="cz-dacts">
            <label className="cz-swl">
              {t("skill.enabled")}
              <Switch on={item.on} label={item.on ? t("skill.turnOff", { name }) : t("skill.turnOn", { name })} onChange={(on) => a.toggle(item, on)} />
            </label>
            {item.entry.removable && <MoreButton label={t("more")} items={() => skillMenu(item, a, c, t, false)} />}
          </div>
        </div>
        <div className="cz-facts">
          <DFact k={t("skill.sourceLabel")}>{source}</DFact>
          <DFact k={t("skill.invoke")}>
            <span className="cut">{command}</span>
            {!item.entry.model_invocable && <span className="sub">{t("skill.userOnly")}</span>}
            <button className="cz-ghost" title={t("skill.copy")} aria-label={t("skill.copy")} onClick={copy}>
              <Icon name="copy" />
            </button>
          </DFact>
          <DFact k={t("skill.path")} grow>
            <span className="cut" title={item.dir}>
              {item.dir}
            </span>
          </DFact>
        </div>
        {note && <p className="cz-aside">{note}</p>}
      </div>
      {item.entry.description && (
        <section className="cz-dsum">
          <div className="k">{t("skill.summary")}</div>
          <p>{item.entry.description}</p>
        </section>
      )}
      <section className="cz-dfiles">
        <div className="cz-th">
          <h2>{t("skill.filesTitle")}</h2>
          <span className="n">{count}</span>
        </div>
        <SkillContents key={item.dir} dir={item.dir} files={files} error={filesError} />
      </section>
    </div>
  );
}

/** Where a remote server's account lives, as people say it: its host without the service label. */
function accountHost(url: string): string {
  const host = URL.parse(url)?.hostname ?? "";
  return host.replace(/^(mcp|api|www|server|gateway|sse)\./, "");
}

/** A note under the facts, when something needs the person: what, why, and its one action. */
function Note({ kind, title, children, action }: { kind: "warn" | "bad" | "info"; title?: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className={`cz-note ${kind}`}>
      <Icon name={kind === "info" ? "info" : "warn"} />
      <div className="tx">
        {title && <b>{title}</b>}
        {children}
      </div>
      {action}
    </div>
  );
}

export function ServerPage({ c, a, s }: { c: Customize; a: ServerActions; s: McpServerView }) {
  const { t } = useTranslation("customize");
  const { rpc, show } = useHer();
  const now = useNow();
  const look = statusOf(s, c.checking.has(s.name));
  const remote = !isCommand(s);
  const [tools, setTools] = useState<McpToolsResult | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  useEffect(() => {
    if (!rpc) return;
    let current = true;
    rpc.request("mcp/tools", { name: s.name }).then(
      (r) => current && setTools(r),
      () => current && setTools({ tools: [], source: "none" }),
    );
    return () => {
      current = false;
    };
  }, [rpc, s.name, s.status.checked_at, s.status.state]);
  const copy = () => {
    void navigator.clipboard?.writeText(s.summary);
    show(t(remote ? "connector.copiedAddress" : "connector.copiedCommand"));
  };

  // The state in a word, coloured as the list colours it.
  const [stCls, stDot, stText] =
    look.kind === "ok"
      ? ["ok", "on", t("connector.stateOk")]
      : look.kind === "failed"
        ? ["bad", "bad", t("connector.failed")]
        : look.kind === "needs_login"
          ? ["warn", "warn", t("connector.needsLogin")]
          : look.kind === "off"
            ? ["", "", t("connector.off")]
            : look.kind === "checking"
              ? ["", "", t("connector.checking")]
              : ["", "", t("connector.unknown")];
  // How it is let in: the account it signs in with, or the secret it sends.
  const keyNames = (s.credentials ?? []).map((cr) => cr.replace(new RegExp(`^mcp\\.${s.name}\\.?`), "").replace(/^header\./, "")).filter(Boolean);
  const access =
    s.auth === "oauth"
      ? { k: t("connector.account"), v: s.connector ?? accountHost(s.summary), sub: s.auth_state === "logged_in" ? t("connector.signedIn") : t("connector.signedOut") }
      : s.auth === "credential"
        ? { k: remote ? t("connector.token") : t("connector.secret"), v: keyNames.join(", "), sub: s.auth_state === "missing" ? t("connector.keyMissing") : t("connector.keySaved") }
        : null;

  const menu = () => [
    ...serverMenu(s, a, c, t, false).filter((m) => m && "id" in m && m.id !== "remove"),
    ...(!s.connector ? [{ id: "copy", label: remote ? t("connector.copyAddress") : t("connector.copyCommand"), icon: "copy" as const, run: copy }] : []),
    ...(s.auth === "oauth" && s.auth_state === "logged_in" ? [{ id: "logout", label: t("connector.signOut"), icon: "logout" as const, run: () => void c.logout(s.name) }] : []),
    ...(s.editable ? [null, { id: "remove", label: t("connector.removeConnector"), icon: "trash" as const, danger: true, run: () => c.removeServer(s) }] : []),
  ];

  const signing = c.signingIn.get(s.name);
  let note: ReactNode = null;
  // A curated connector signs in through its own program on the machine, not from here.
  if (s.enabled && !s.connector && look.kind === "needs_login")
    note = (
      <Note
        kind="warn"
        title={t("connector.loginTitle")}
        action={
          signing !== undefined ? (
            <span className="cz-note-wait">
              <span className="mini-spin cz-spin" />
              {t("connector.signingIn")}
              {signing && (
                <button className="link" onClick={() => openPage(signing)}>
                  {t("addc.openAgain")}
                </button>
              )}
              <button className="link" onClick={() => c.cancelLogin(s.name)}>
                {t("connector.cancelSignIn")}
              </button>
            </span>
          ) : (
            <button className="btn primary" onClick={() => void c.login(s.name)}>
              {t("connector.signIn")}
            </button>
          )
        }
      >
        {t("connector.loginText", { account: accountHost(s.summary) })}
      </Note>
    );
  else if (s.enabled && look.kind === "failed")
    note = (
      <Note
        kind="bad"
        title={t("connector.failedText")}
        action={
          <button className="btn" onClick={() => void c.check(s.name)}>
            <Icon name="retry" />
            {t("connector.retry")}
          </button>
        }
      >
        {s.status.error && <span className="err">{s.status.error}</span>}
      </Note>
    );
  else if (s.auth_partial && s.auth_state === "logged_in") note = <Note kind="warn">{t("connector.partialPermissions")}</Note>;
  else if (!remote) note = <Note kind="info">{t("connector.commandNote")}</Note>;

  const list = tools?.tools ?? [];
  const from = tools?.source === "session" ? t("connector.toolsFromSessions", { count: s.status.sessions ?? 1 }) : tools?.checked_at ? t("connector.checkedAt", { ago: agoText(new Date(tools.checked_at), now) }) : "";
  const empty = look.kind === "needs_login" ? t("connector.toolsAfterLogin") : look.kind === "failed" ? t("connector.toolsAfterConnect") : t("connector.noTools");
  return (
    <div className={`cz-dpage ${s.enabled ? "" : "off"}`}>
      <div className="cz-dhead">
        {/* The controls belong to the item, so they sit on its line, centred on the logo. */}
        <div className="cz-dtitle">
          <ServerTile name={s.name} icon={s.icon} size="xl" />
          <h1>{s.name}</h1>
          <span className="badge">{remote ? t("connector.custom") : t("addc.local")}</span>
          <div className="cz-dacts">
            <label className="cz-swl">
              {t("connector.enabled")}
              <Switch disabled={!!s.connector && s.auth_state !== "logged_in"} on={s.enabled} label={s.enabled ? t("connector.turnOff", { name: s.name }) : t("connector.turnOn", { name: s.name })} onChange={(on) => a.toggle(s, on)} />
            </label>
            <MoreButton label={t("more")} items={menu} />
          </div>
        </div>
        <div className="cz-facts">
          <div className="cz-fact">
            <div className="k">{t("connector.state")}</div>
            <div className={`v cz-st ${stCls}`}>
              <i className={`dotc ${stDot}`} />
              {stText}
            </div>
          </div>
          {access && (
            <div className="cz-fact">
              <div className="k">{access.k}</div>
              <div className="v">
                {access.v}
                <span className="sub">{access.sub}</span>
              </div>
            </div>
          )}
          <div className="cz-fact grow">
            <div className="k">{remote ? t("connector.address") : t("connector.command")}</div>
            <div className="v">
              <span className="cut" title={s.summary}>
                {s.summary}
              </span>
              <button className="cz-ghost" title={remote ? t("connector.copyAddress") : t("connector.copyCommand")} aria-label={remote ? t("connector.copyAddress") : t("connector.copyCommand")} onClick={copy}>
                <Icon name="copy" />
              </button>
            </div>
          </div>
        </div>
      </div>
      {note}
      {s.description && (
        <section className="cz-dsum">
          <div className="k">{t("connector.summary")}</div>
          <p>{s.description}</p>
        </section>
      )}
      <section className="cz-dtools">
        <div className="cz-th">
          <h2>{t("connector.tools")}</h2>
          {list.length > 0 && <span className="n">{list.length}</span>}
          <span className="right">
            {list.length > 0 && from}
            <button className="link" onClick={() => void c.check(s.name)}>
              {t("connector.recheck")}
            </button>
          </span>
        </div>
        {tools === null ? (
          <p className="cz-dempty">{t("loading")}</p>
        ) : list.length === 0 ? (
          <p className="cz-dempty">{empty}</p>
        ) : (
          <div className="cz-tlist">
            {list.map((tool) => (
              <div key={tool.name} className={`cz-tool ${open === tool.name ? "open" : ""}`} onClick={() => setOpen(open === tool.name ? null : tool.name)}>
                <span className="tn">{tool.name}</span>
                <span className="td">{tool.description || tool.title}</span>
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}
