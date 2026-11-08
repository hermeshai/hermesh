import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { McpProbeResult } from "@hermesh/her-protocol";
import { Icon } from "../icons";
import { secretish, type Row } from "./detect";
import { splitCommand } from "./model";
import { PopUp, type Choice } from "./parts";

// A connector's settings as grouped forms (customize-design §10.9, §10.11): the sign-in, the
// advanced options, the command and its variables. Adding (AddConnector) and editing
// (EditConnector) draw the same parts, so a setting reads and works the same in both.

export type Auth = "oauth" | "token" | "none";
// Which client her signs in as: her finds one on its own (a published identity, else a
// registration; the order is the kernel's), or the person's own OAuth app.
export type Client = "auto" | "own";
export type Transport = "streamable_http" | "sse";

export function Rows({ rows, set, keyHint, valueHint }: { rows: Row[]; set: (rows: Row[]) => void; keyHint: string; valueHint: string }) {
  const { t } = useTranslation("customize");
  const change = (i: number, patch: Partial<Row>) => set(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <div className="cz-kvs">
      {rows.map((r, i) => (
        <div key={i} className="cz-kvrow">
          <input className="field mono" value={r.key} placeholder={keyHint} aria-label={keyHint} spellCheck={false} onChange={(e) => change(i, { key: e.target.value, locked: r.locked || (secretish.test(e.target.value) && !r.value) })} />
          <input
            className="field mono"
            type={r.locked ? "password" : "text"}
            value={r.value}
            aria-label={valueHint}
            placeholder={r.saved && !r.value ? t("addc.saved") : r.locked ? t("addc.keychain") : valueHint}
            spellCheck={false}
            onChange={(e) => change(i, { value: e.target.value })}
          />
          <button className={`cz-lock ${r.locked ? "on" : ""}`} title={r.locked ? t("addc.locked") : t("addc.unlocked")} aria-label={r.locked ? t("addc.locked") : t("addc.unlocked")} onClick={() => change(i, { locked: !r.locked })}>
            <Icon name={r.locked ? "lock" : "unlock"} />
          </button>
          <button className="cz-lock" title={t("addc.removeRow")} aria-label={t("addc.removeRow")} onClick={() => set(rows.filter((_, j) => j !== i))}>
            <Icon name="x" />
          </button>
        </div>
      ))}
      <button className="link cz-addrow" onClick={() => set([...rows, { key: "", value: "", locked: false }])}>
        <Icon name="plus" />
        {t("addc.addRow")}
      </button>
    </div>
  );
}

/** A remote server's sign-in and options, and the setters the sheet keeps them with. */
export type RemoteState = {
  auth: Auth;
  setAuth: (a: Auth) => void;
  client: Client;
  setClient: (c: Client) => void;
  clientId: string;
  setClientId: (v: string) => void;
  clientSecret: string;
  setClientSecret: (v: string) => void;
  token: string;
  setToken: (v: string) => void;
  header: string;
  setHeader: (v: string) => void;
  prefix: string;
  setPrefix: (v: string) => void;
  headers: Row[];
  setHeaders: (rows: Row[]) => void;
  transport: Transport;
  setTransport: (t: Transport) => void;
};

/**
 * The sign-in, its footnote and the advanced options. `probed` is what a check of the address
 * found: absent while it runs (`probing`) or when it could not run, and the choices then stay
 * open. `saved` marks secrets already in the credential store, which an empty field keeps.
 */
export function RemoteFields({ s, probed, probing, host, saved }: { s: RemoteState; probed: McpProbeResult | null; probing?: boolean; host: string; saved?: { token?: boolean; secret?: boolean } }) {
  const { t } = useTranslation("customize");
  const [sendAs, setSendAs] = useState(s.header !== "Authorization" || s.prefix !== "Bearer ");
  const [adv, setAdv] = useState(false);
  const reached = probed && !probed.error ? probed : null;
  const oauthFound = reached?.auth?.oauth ?? null;
  // The sign-in service by its host, as people know it.
  const issuer = (oauthFound ? URL.parse(oauthFound.authorization_server)?.host || oauthFound.authorization_server : host) ?? "";
  const authChoices: Choice<Auth>[] = [
    { id: "oauth", label: t("addc.oauth"), tag: "OAuth", desc: t("addc.oauthDesc", { issuer }), off: !!reached && !oauthFound?.pkce && s.auth !== "oauth" && t("addc.noOAuth") },
    { id: "token", label: t("addc.token"), desc: t("addc.tokenDesc") },
    { id: "none", label: t("addc.none"), desc: t("addc.noneDesc") },
  ];
  const ownRequired = reached?.suggested.client === "own";
  const clientChoices: Choice<Client>[] = [
    { id: "auto", label: t("addc.clientAuto"), desc: t("addc.clientAutoDesc"), off: ownRequired && t("addc.clientAutoOff", { issuer }) },
    { id: "own", label: t("addc.own"), desc: t("addc.ownDesc") },
  ];
  const transportChoices: Choice<Transport>[] = [
    { id: "streamable_http", label: "Streamable HTTP", desc: t("addc.httpDesc") },
    { id: "sse", label: t("addc.sse"), desc: t("addc.sseDesc") },
  ];
  // What the check found, then what saving does with the choice made.
  const found = probing ? null : !reached ? (probed ? t("addc.found.unreached") : null) : !reached.auth?.required ? t("addc.found.open") : oauthFound ? t(ownRequired ? "addc.found.oauthOwn" : "addc.found.oauth", { issuer }) : t("addc.found.token");
  // Sentences run on without a space in Chinese, with one in English.
  const sep = t("addc.sep");
  const what = s.auth === "oauth" ? t("addc.oauthDesc", { issuer }) : s.auth === "token" ? t("addc.what.token") : null;
  const lead = (text: string | null) => (found ? sep + text : text);
  return (
    <>
      <div className="cz-gh">{t("addc.auth")}</div>
      <div className="fgroup">
        <div className="frow">
          <span className="fl">{t("addc.method")}</span>
          <span className="fv">
            <PopUp label={t("addc.method")} value={s.auth} choices={authChoices} found={reached?.suggested.auth} onChange={s.setAuth} />
          </span>
        </div>
        {s.auth === "oauth" && s.client === "own" && (
          <>
            <div className="frow">
              <label className="fl" htmlFor="cz-client-id">
                Client ID
              </label>
              <span className="fv">
                <input id="cz-client-id" className="field mono" value={s.clientId} spellCheck={false} onChange={(e) => s.setClientId(e.target.value)} />
              </span>
            </div>
            <div className="frow">
              <label className="fl" htmlFor="cz-client-secret">
                Client Secret
              </label>
              <span className="fv">
                <input id="cz-client-secret" className="field mono" type="password" value={s.clientSecret} placeholder={saved?.secret ? t("addc.saved") : t("addc.secretOptional")} onChange={(e) => s.setClientSecret(e.target.value)} />
              </span>
            </div>
          </>
        )}
        {s.auth === "token" && (
          <>
            <div className="frow">
              <label className="fl" htmlFor="cz-token">
                {t("addc.tokenRow")}
              </label>
              <span className="fv">
                <input id="cz-token" className="field mono" type="password" value={s.token} placeholder={saved?.token ? t("addc.saved") : t("addc.tokenPlaceholder")} autoComplete="off" onChange={(e) => s.setToken(e.target.value)} />
              </span>
            </div>
            <div className="frow">
              <span className="fl">{t("addc.sendAs")}</span>
              {sendAs ? (
                <span className="fv cz-two">
                  <input className="field mono" value={s.header} placeholder="Header" aria-label="Header" onChange={(e) => s.setHeader(e.target.value)} />
                  <input className="field mono" value={s.prefix} placeholder={t("addc.prefix")} aria-label={t("addc.prefix")} onChange={(e) => s.setPrefix(e.target.value)} />
                </span>
              ) : (
                <span className="fv">
                  <code className="cz-code">{`${s.header}: ${s.prefix}‹${t("addc.tokenRow")}›`}</code>
                  <button className="link" onClick={() => setSendAs(true)}>
                    {t("addc.change")}
                  </button>
                </span>
              )}
            </div>
          </>
        )}
      </div>
      <p className="cz-fn">
        {probing && (
          <span className="det">
            <span className="mini-spin cz-spin" />
            {t("addc.found.checking")}
          </span>
        )}
        {found && <span className="det">{found}</span>}
        {/* When the person must bring their own app, what to do comes first; the rest is as usual. */}
        {what && !(s.auth === "oauth" && ownRequired) && lead(what)}
        {s.auth === "oauth" && s.client === "own" && (
          <>
            {lead(t("addc.redirect", { issuer })) + " "}
            <code className="cz-code">http://127.0.0.1/callback</code>
            {t("addc.stop")}
          </>
        )}
        {s.auth === "oauth" && ownRequired && lead(t("addc.patHint"))}
      </p>
      <button className={`cz-fold ${adv ? "open" : ""}`} aria-expanded={adv} onClick={() => setAdv(!adv)}>
        <Icon name="chevR" />
        {t("addc.advanced")}
        {!adv && <small>{t("addc.advancedNow", { transport: s.transport === "sse" ? t("addc.sse") : "Streamable HTTP", headers: s.headers.filter((h) => h.key.trim()).length || t("addc.noneShort") })}</small>}
      </button>
      {adv && (
        <div className="fgroup cz-advgroup">
          {/* Who her signs in as: her sees to it, unless the person must or wants to use their own app. */}
          {s.auth === "oauth" && (
            <div className="frow">
              <span className="fl">{t("addc.client")}</span>
              <span className="fv">
                <PopUp label={t("addc.client")} value={s.client} choices={clientChoices} onChange={s.setClient} />
              </span>
            </div>
          )}
          <div className="frow">
            <span className="fl">{t("addc.transport")}</span>
            <span className="fv">
              <PopUp label={t("addc.transport")} value={s.transport} choices={transportChoices} found={reached?.transport} onChange={s.setTransport} />
            </span>
          </div>
          <div className="frow stack">
            <span className="fl">{t("addc.headers")}</span>
            <Rows rows={s.headers} set={s.setHeaders} keyHint="Header" valueHint={t("addc.value")} />
          </div>
        </div>
      )}
    </>
  );
}

/** A command connector's command line and its variables. */
export function LocalFields({ command, setCommand, env, setEnv }: { command: string; setCommand: (v: string) => void; env: Row[]; setEnv: (rows: Row[]) => void }) {
  const { t } = useTranslation("customize");
  const argv = splitCommand(command);
  return (
    <>
      <div className="cz-gh">{t("addc.command")}</div>
      <div className="fgroup">
        <div className="frow stack">
          <input className="field mono" value={command} aria-label={t("addc.command")} spellCheck={false} onChange={(e) => setCommand(e.target.value)} />
          <div className="cz-cmdline">
            {argv.map((a, i) => (
              <span key={i} className={i === 0 ? "tok p" : "tok"}>
                {a}
              </span>
            ))}
          </div>
        </div>
      </div>
      {["npx", "uvx", "bunx", "pnpm", "docker"].includes(argv[0] ?? "") && <p className="cz-fn">{t("addc.download", { program: argv[0] })}</p>}
      <div className="cz-gh">{t("addc.env")}</div>
      <div className="fgroup">
        <div className="frow stack">
          <Rows rows={env} set={setEnv} keyHint={t("addc.varName")} valueHint={t("addc.value")} />
        </div>
      </div>
      <p className="cz-fn">{t("addc.envNote")}</p>
    </>
  );
}
