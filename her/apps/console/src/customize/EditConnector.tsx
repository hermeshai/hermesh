import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { RpcError } from "@hermesh/her-client";
import type { McpProbeResult, McpServerView } from "@hermesh/her-protocol";
import { Dialog } from "../components";
import { errorText } from "../format";
import { useHer } from "../her";
import { LocalFields, RemoteFields, type Auth, type Client, type RemoteState, type Transport } from "./ConnectorFields";
import type { Row } from "./detect";
import { localIdentity, remoteIdentity } from "./identity";
import { credentialName, credentialRef, isCommand, joinCommand, splitCommand, withCredential } from "./model";
import { ServerTile } from "./parts";
import type { Customize } from "./useCustomize";

// Edit Connector: the add sheet's settings, filled from the
// file. A remote address is checked again in the background, so the sign-in shows what the
// server takes as it did when the connector was added. Saved secrets stay in the credential
// store unless a new value is typed; switching to OAuth signs in once the file is written. A
// command's new settings are signed on the owner's Mac, as adding it was.

type Raw = {
  url?: string;
  transport?: Transport;
  headers?: Record<string, string>;
  auth?: string;
  oauth?: { client_id?: string; client_secret?: string };
  command?: string;
  args?: string[];
  env?: Record<string, string>;
};

/** A table of the file as rows: a value that refers to the credential store shows as saved. */
function rowsOf(map: Record<string, string> | undefined): Row[] {
  return Object.entries(map ?? {}).map(([key, v]) => {
    const value = String(v ?? "");
    return credentialRef(value) ? { key, value: "", locked: true, saved: value } : { key, value, locked: false };
  });
}

/** Rows back as a table; typed secrets go to `secrets` and the file keeps a reference. */
function tableOf(rows: Row[], credential: (key: string) => string, secrets: [string, string][]): Record<string, string> | null {
  const out: Record<string, string> = {};
  for (const r of rows) {
    const key = r.key.trim();
    if (!key) continue;
    if (!r.value) {
      if (r.saved) out[key] = r.saved;
      continue;
    }
    if (r.locked) {
      secrets.push([credential(key), r.value]);
      out[key] = withCredential(r.saved ?? null, credential(key));
    } else out[key] = r.value;
  }
  return Object.keys(out).length ? out : null;
}

const PROBE_DELAY = 600;

export function EditConnector({ c, server, onClose, onSaved }: { c: Customize; server: McpServerView; onClose: () => void; onSaved: (name: string) => void }) {
  const { t } = useTranslation("customize");
  const { rpc, show } = useHer();
  const name = server.name;
  // What the file says about the server, references kept (config/read).
  const raw = ((c.config?.config as { mcp_servers?: Record<string, Raw> })?.mcp_servers?.[name] ?? {}) as Raw;
  const remote = !isCommand(server);

  // The token the add sheet saved: a header whose value refers to mcp.<name>.
  const tokenCredential = `mcp.${name}`;
  const tokenEntry = Object.entries(raw.headers ?? {}).find(([, v]) => credentialRef(String(v)) === tokenCredential);
  const startAuth: Auth = raw.auth === "oauth" ? "oauth" : tokenEntry ? "token" : "none";
  const startClient: Client = raw.oauth?.client_id ? "own" : "auto";
  const urlSays = (u: string): Transport => (/\/sse\/?$/.test(URL.parse(u.trim())?.pathname ?? "") ? "sse" : "streamable_http");

  const [url, setUrl] = useState(raw.url ?? "");
  const [auth, setAuth] = useState<Auth>(startAuth);
  const [client, setClient] = useState<Client>(startClient);
  const [clientId, setClientId] = useState(raw.oauth?.client_id ?? "");
  const [clientSecret, setClientSecret] = useState("");
  const [token, setToken] = useState("");
  const [header, setHeader] = useState(tokenEntry?.[0] ?? "Authorization");
  const [prefix, setPrefix] = useState(tokenEntry ? String(tokenEntry[1]).slice(0, String(tokenEntry[1]).indexOf("${credential:")) : "Bearer ");
  const [headers, setHeaders] = useState<Row[]>(() => rowsOf(Object.fromEntries(Object.entries(raw.headers ?? {}).filter(([k]) => k !== tokenEntry?.[0]))));
  const [transport, setTransport] = useState<Transport>(raw.transport ?? urlSays(raw.url ?? ""));
  const [command, setCommand] = useState(raw.command ? joinCommand(raw.command, raw.args ?? []) : "");
  const [env, setEnv] = useState<Row[]>(() => rowsOf(raw.env));
  const [probed, setProbed] = useState<McpProbeResult | null>(null);
  const [probing, setProbing] = useState(false);
  const [errors, setErrors] = useState<{ url?: string; command?: string; other?: string }>({});
  const [busy, setBusy] = useState(false);
  const probeId = useRef<string | null>(null);

  const cancelProbe = () => {
    if (probeId.current) void rpc?.request("mcp/probe_cancel", { probe_id: probeId.current }).catch(() => {});
    probeId.current = null;
  };

  // Checks the address as the add sheet does, again whenever it changes.
  useEffect(() => {
    if (!remote || !rpc) return;
    const address = url.trim();
    if (!/^https?:\/\/./i.test(address) || !URL.parse(address)) {
      setProbed(null);
      setProbing(false);
      return;
    }
    setProbing(true);
    const timer = setTimeout(async () => {
      cancelProbe();
      const id = `${Date.now()}-${Math.random().toString(36).slice(2)}`;
      probeId.current = id;
      try {
        const result = await rpc.request("mcp/probe", { probe_id: id, server: { name, url: address } });
        if (probeId.current !== id) return;
        setProbed(result);
        if (result.transport && !raw.transport) setTransport(result.transport);
      } catch (e) {
        if (probeId.current === id) setProbed({ error: errorText(e) } as McpProbeResult);
      } finally {
        if (probeId.current === id) {
          probeId.current = null;
          setProbing(false);
        }
      }
    }, PROBE_DELAY);
    return () => clearTimeout(timer);
  }, [url, rpc]);
  useEffect(() => cancelProbe, []);

  const close = () => {
    cancelProbe();
    onClose();
  };

  const tokenSaved = !!tokenEntry && header === tokenEntry[0];
  const secretSaved = !!raw.oauth?.client_secret && credentialRef(raw.oauth.client_secret) !== null;
  // Signing in is due when OAuth is newly chosen, or what it signs in to or as has changed.
  const signIn = remote && auth === "oauth" && (startAuth !== "oauth" || url.trim() !== (raw.url ?? "") || client !== startClient || clientId.trim() !== (raw.oauth?.client_id ?? "") || server.status.state === "needs_login");
  const okToSave = remote ? (auth === "token" ? !!token.trim() || tokenSaved : auth === "oauth" && client === "own" ? !!clientId.trim() : true) : splitCommand(command).length > 0;

  const save = async () => {
    const e: typeof errors = {};
    const argv = splitCommand(command);
    if (remote && !/^https?:\/\//i.test(url.trim())) e.url = t("edit.errUrl");
    if (!remote && !argv.length) e.command = t("edit.errCommand");
    // One service, one connector (mcp-design §13.10); only a change of address counts, so
    // entries that were alike already can still be edited.
    const id = remote ? remoteIdentity(url) : argv.length ? localIdentity(argv[0], argv.slice(1)) : null;
    const same = id && id !== server.identity && c.servers.find((s) => s.identity === id && s.name !== name);
    if (same) e[remote ? "url" : "command"] = t("addc.duplicate", { name: same.name });
    setErrors(e);
    if (Object.keys(e).length) return;

    const secrets: [string, string][] = [];
    setBusy(true);
    try {
      let check;
      if (remote) {
        const table = tableOf(headers, (key) => credentialName(name, `header.${key}`), secrets) ?? {};
        if (auth === "token") {
          if (token.trim()) secrets.push([tokenCredential, token.trim()]);
          table[header.trim() || "Authorization"] = `${prefix}\${credential:${tokenCredential}}`;
        }
        const secretCredential = `mcp.${name}.oauth_secret`;
        if (auth === "oauth" && client === "own" && clientSecret) secrets.push([secretCredential, clientSecret]);
        const fields = {
          url: url.trim(),
          transport: transport !== urlSays(url) ? transport : null,
          headers: Object.keys(table).length ? table : null,
          auth: auth === "oauth" ? "oauth" : null,
          oauth:
            auth === "oauth" && client === "own"
              ? { client_id: clientId.trim(), ...(clientSecret ? { client_secret: `\${credential:${secretCredential}}` } : secretSaved ? { client_secret: raw.oauth!.client_secret } : {}) }
              : null,
        };
        ({ check } = await c.saveServer(name, fields, secrets, "upsert"));
        // What the old sign-in kept is let go of: the token, or the OAuth sign-in.
        if (startAuth === "token" && auth !== "token") void rpc?.request("credentials/delete", { name: tokenCredential }).catch(() => {});
        if (startAuth === "oauth" && auth !== "oauth") void rpc?.request("mcp/logout", { name }).catch(() => {});
      } else {
        const vars = tableOf(env, (key) => credentialName(name, key), secrets) ?? {};
        for (const [credential, value] of secrets) await rpc!.request("credentials/set", { name: credential, value });
        check = await c.saveCommand({ name, command: argv[0], args: argv.slice(1), env: vars });
      }
      onSaved(name);
      close();
      if (signIn) void c.login(name);
      else if (check?.server.status.state === "failed") show(t("toast.serverSavedFailed", { name, error: check.server.status.error ?? "" }), undefined, true);
      else show(t("toast.serverSaved", { name }));
    } catch (err) {
      // her's checks, by field (config_validation_error carries the key paths).
      const found = err instanceof RpcError && err.kind === "config_validation_error" ? ((err.data as { errors?: { key_path: string; message: string }[] })?.errors ?? []) : [];
      if (found.length) {
        const next: typeof errors = {};
        for (const f of found) {
          if (/\.url$/.test(f.key_path)) next.url = f.message;
          else next.other = f.message;
        }
        setErrors(next);
      } else setErrors({ other: errorText(err) });
    } finally {
      setBusy(false);
    }
  };

  const fields: RemoteState = { auth, setAuth, client, setClient, clientId, setClientId, clientSecret, setClientSecret, token, setToken, header, setHeader, prefix, setPrefix, headers, setHeaders, transport, setTransport };
  const title = t("edit.title", { name });
  return (
    <Dialog label={title} width={520} className="cz-sheet cz-add" onClose={close}>
      <h3>{title}</h3>
      <div className="cz-add-body" data-step={remote ? "remote" : "local"}>
        <div className="cz-summary">
          <ServerTile name={name} icon={server.icon} />
          <div className="t">
            <div className="n">
              {name}
              <span className="badge">{remote ? (transport === "sse" ? t("addc.remoteSse") : t("addc.remoteShort")) : t("addc.local")}</span>
            </div>
            {server.file && <div className="a">{t("edit.where", { file: server.file })}</div>}
          </div>
        </div>
        {remote ? (
          <>
            <div className="cz-gh">{t("edit.address")}</div>
            <div className="fgroup">
              <div className="frow stack">
                <input className={`field mono ${errors.url ? "bad" : ""}`} value={url} aria-label={t("edit.address")} spellCheck={false} onChange={(e) => (setUrl(e.target.value), setErrors({}))} />
              </div>
            </div>
            {errors.url && <div className="cz-ferr">{errors.url}</div>}
            <RemoteFields s={fields} probed={probed} probing={probing} host={URL.parse(url.trim())?.hostname ?? ""} saved={{ token: tokenSaved, secret: secretSaved }} />
          </>
        ) : (
          <>
            <LocalFields command={command} setCommand={(v) => (setCommand(v), setErrors({}))} env={env} setEnv={setEnv} />
            {errors.command && <div className="cz-ferr">{errors.command}</div>}
            <p className="cz-trust">{t("addc.localTrust")}</p>
          </>
        )}
        {errors.other && <div className="cz-errbox cz-gap">{errors.other}</div>}
      </div>
      <div className="sheet-foot">
        <div className="left" />
        <button className="btn" onClick={close}>
          {t("edit.cancel")}
        </button>
        <button className="btn primary" disabled={busy || !okToSave} onClick={() => void save()}>
          {busy && <span className="mini-spin cz-spin-solid" />}
          {signIn ? t("edit.saveAndLogin") : remote ? t("edit.save") : t("edit.saveAndApprove")}
        </button>
      </div>
    </Dialog>
  );
}
