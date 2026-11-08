import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { McpProbeResult, McpProbeStep, McpProbeStepId, McpServerConfig } from "@hermesh/her-protocol";
import { Dialog } from "../components";
import { errorText } from "../format";
import { useHer } from "../her";
import { Icon } from "../icons";
import { openPage, signInRemotely } from "../signin";
import { LocalFields, RemoteFields, type Auth, type Client, type RemoteState, type Transport } from "./ConnectorFields";
import { detect, suggestName, type Detected, type Row } from "./detect";
import { localIdentity, remoteIdentity } from "./identity";
import { credentialName, joinCommand, serverName, splitCommand } from "./model";
import { ServerTile } from "./parts";
import type { Customize } from "./useCustomize";

// Add Connector:
// a name and an address first. A remote address is checked (mcp/probe), then its sign-in is
// set up with what the check found, and nothing is written before a check passes unless the
// person adds it anyway. A command gets its form, then the owner's Mac signs it with Touch ID
// (docs/her-server.md §8) and it is checked once added: a command is never run before that.

type Step = "entry" | "check" | "remote" | "local" | "login" | "approve" | "done";
const REMOTE_STEPS: McpProbeStepId[] = ["connect", "resource", "auth_server"];

export function AddConnector({ c, onClose, onAdded, onOpen }: { c: Customize; onClose: () => void; onAdded: (name: string) => void; onOpen: (name: string) => void }) {
  const { t } = useTranslation("customize");
  const { rpc, show } = useHer();
  const [step, setStep] = useState<Step>("entry");
  const [name, setName] = useState("");
  const [address, setAddress] = useState("");
  const [errors, setErrors] = useState<{ name?: string; address?: string }>({});
  const [d, setD] = useState<Detected | null>(null);
  const [steps, setSteps] = useState<McpProbeStep[]>([]);
  const [probed, setProbed] = useState<McpProbeResult | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [auth, setAuth] = useState<Auth>("none");
  const [client, setClient] = useState<Client>("auto");
  const [clientId, setClientId] = useState("");
  const [clientSecret, setClientSecret] = useState("");
  const [token, setToken] = useState("");
  const [header, setHeader] = useState("Authorization");
  const [prefix, setPrefix] = useState("Bearer ");
  const [headers, setHeaders] = useState<Row[]>([]);
  const [transport, setTransport] = useState<Transport>("streamable_http");
  const [command, setCommand] = useState("");
  const [env, setEnv] = useState<Row[]>([]);
  const [busy, setBusy] = useState(false);
  const [back, setBack] = useState<Step>("entry");
  const [tools, setTools] = useState<string[]>([]);
  const [authorizeUrl, setAuthorizeUrl] = useState<string | null>(null);
  // Credentials saved for a check or a signature, deleted again when the person gives up.
  const staged = useRef<string[]>([]);
  const probeId = useRef<string | null>(null);
  const login = useRef<AbortController | null>(null);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      login.current?.abort();
    };
  }, []);

  useEffect(() => {
    if (!rpc) return;
    return rpc.on("mcp/probe_progress", (p) => {
      if (p.probe_id !== probeId.current) return;
      setSteps((list) => [...list.filter((s) => s.id !== p.step.id), p.step]);
    });
  }, [rpc]);

  const existing = new Set(c.servers.map((s) => s.name));
  // One service, one connector (mcp-design §13.10): the one that already reaches this address.
  const sameAs = (det: Detected | null) => {
    if (!det || det.kind === "bad") return null;
    const id = det.kind === "remote" ? remoteIdentity(det.url) : localIdentity(det.argv[0], det.argv.slice(1));
    return c.servers.find((s) => s.identity === id) ?? null;
  };
  const detected = detect(address);
  const duplicate = sameAs(detected);
  const suggested = name ? "" : suggestName(detected);

  const discard = async () => {
    for (const credential of staged.current) await rpc?.request("credentials/delete", { name: credential }).catch(() => {});
    staged.current = [];
  };
  const close = () => {
    alive.current = false;
    if (probeId.current) void rpc?.request("mcp/probe_cancel", { probe_id: probeId.current }).catch(() => {});
    login.current?.abort();
    if (step !== "done") void discard();
    onClose();
  };

  const stage = async (credential: string, value: string) => {
    await rpc!.request("credentials/set", { name: credential, value });
    if (!staged.current.includes(credential)) staged.current.push(credential);
    return `\${credential:${credential}}`;
  };

  const runProbe = async (server: McpServerConfig): Promise<McpProbeResult> => {
    const id = `${Date.now()}-${Math.random().toString(36).slice(2)}`;
    probeId.current = id;
    setSteps([]);
    try {
      return await rpc!.request("mcp/probe", { probe_id: id, server });
    } finally {
      probeId.current = null;
    }
  };

  const connect = async () => {
    const det = detect(address);
    const n = (name || suggestName(det)).trim();
    const errs: typeof errors = {};
    if (!serverName.test(n)) errs.name = t("addc.badName");
    else if (existing.has(n)) errs.name = t("addc.taken", { name: n });
    if (!det || det.kind === "bad") errs.address = t(`addc.bad.${det?.kind === "bad" ? det.why : "empty"}`);
    else if (sameAs(det)) return;
    setErrors(errs);
    if (errs.name || errs.address || !det || det.kind === "bad" || !rpc) return;
    setName(n);
    setD(det);
    if (det.kind === "local") {
      setCommand(joinCommand(det.argv[0], det.argv.slice(1)));
      setEnv(det.env);
      setStep("local");
      return;
    }
    setHeaders(det.headers);
    setTransport(det.sse ? "sse" : "streamable_http");
    await checkRemote(n, det);
  };

  const checkRemote = async (n: string, det: Extract<Detected, { kind: "remote" }>) => {
    setFailure(null);
    setBack("entry");
    setStep("check");
    try {
      const result = await runProbe({ name: n, url: det.url });
      setProbed(result);
      setSteps(result.steps);
      if (result.error) {
        setFailure(result.error);
        return;
      }
      setTransport(result.transport ?? "streamable_http");
      setAuth(result.suggested.auth);
      setClient(result.suggested.client === "own" ? "own" : "auto");
      setTimeout(() => alive.current && setStep("remote"), 500);
    } catch (e) {
      setFailure(errorText(e));
    }
  };

  /** The remote server as it will be written; secrets as references. */
  const remoteServer = async (): Promise<McpServerConfig> => {
    const det = d as Extract<Detected, { kind: "remote" }>;
    const server: McpServerConfig = { name, url: det.url };
    const urlSays = det.sse ? "sse" : "streamable_http";
    if (transport !== urlSays) server.transport = transport;
    const extra: Record<string, string> = {};
    for (const h of headers.filter((h) => h.key.trim())) extra[h.key.trim()] = h.locked && h.value ? await stage(credentialName(name, `header.${h.key.trim()}`), h.value) : h.value;
    if (auth === "token" && token.trim()) extra[header.trim() || "Authorization"] = `${prefix}${await stage(`mcp.${name}`, token.trim())}`;
    if (Object.keys(extra).length) server.headers = extra;
    if (auth === "oauth") {
      server.auth = "oauth";
      if (client === "own") server.oauth = { client_id: clientId.trim(), ...(clientSecret ? { client_secret: await stage(`mcp.${name}.oauth_secret`, clientSecret) } : {}) };
    }
    return server;
  };

  /** Writes the remote server, checks it once, and shows the result. */
  const finish = async (server: McpServerConfig) => {
    if (!alive.current) return;
    const { name: _name, ...settings } = server;
    void _name;
    const { check } = await c.saveServer(name, settings as Record<string, unknown>, []);
    staged.current = [];
    done(check?.tools.map((tool) => tool.name) ?? []);
  };

  const done = (listed: string[]) => {
    setTools(listed);
    setStep("done");
    onAdded(name);
    show(t("toast.serverAdded", { name, count: listed.length }));
  };

  /** The command, signed on the owner's device; then checked once. A refusal goes back to the form. */
  const addCommand = async () => {
    const argv = splitCommand(command);
    const vars: Record<string, string> = {};
    for (const r of env.filter((r) => r.key.trim())) vars[r.key.trim()] = r.locked && r.value ? await stage(credentialName(name, r.key.trim()), r.value) : r.value;
    setStep("approve");
    try {
      const check = await c.saveCommand({ name, command: argv[0], args: argv.slice(1), env: vars });
      staged.current = [];
      if (!alive.current) return;
      if (check?.server.status.state === "ok") return done(check.tools.map((tool) => tool.name));
      // Added, but it does not work yet: its page says why, with Retry.
      onOpen(name);
      show(t("toast.serverAddedFailed", { name, error: check?.server.status.error ?? "" }), undefined, true);
    } catch (e) {
      if (!alive.current) return;
      setFailure(errorText(e));
      setStep("local");
    }
  };

  const add = async (mode: Step = step) => {
    if (!rpc) return;
    setBusy(true);
    setFailure(null);
    try {
      if (mode === "local") return await addCommand();
      const server = await remoteServer();
      if (auth === "oauth") {
        const abort = new AbortController();
        login.current = abort;
        try {
          await signInRemotely({
            path: "/callback",
            start: (redirect_uri) => rpc.request("mcp/login", { name, server, redirect_uri }),
            open: (url) => {
              setAuthorizeUrl(url);
              setStep("login");
              openPage(url);
            },
            complete: (login_id, g) => rpc.request("mcp/login_complete", { login_id, code: g.code, state: g.state, iss: g.iss }),
            // Nothing is added yet: logging out ends the sign-in and forgets what it saved.
            cancel: () => rpc.request("mcp/logout", { name }),
            signal: abort.signal,
          });
        } catch (e) {
          if (abort.signal.aborted || !alive.current) return;
          setFailure(errorText(e));
          setStep("remote");
          return;
        } finally {
          login.current = null;
        }
        await finish(server);
        return;
      }
      if (auth === "token" || headers.some((h) => h.value)) {
        // The token is checked before anything is written.
        setBack("remote");
        setStep("check");
        const result = await runProbe(server);
        setSteps(result.steps);
        if (result.error) {
          setFailure(result.steps.some((s) => s.http_status === 401) ? t("addc.tokenRefused") : result.error);
          setProbed(result);
          return;
        }
      }
      await finish(server);
    } catch (e) {
      setFailure(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const addAnyway = async () => {
    setBusy(true);
    try {
      await finish(await remoteServer());
    } catch (e) {
      setFailure(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const retry = () => {
    if (back === "remote") {
      setStep(back);
      void add(back);
    } else if (d?.kind === "remote") {
      void checkRemote(name, d);
    }
  };
  const edit = () => {
    void discard();
    setFailure(null);
    setStep(back === "check" ? "entry" : back);
  };

  // ── views ──
  const hint = !detected ? null : duplicate ? (
    <div className="cz-detect bad">
      <Icon name="warn" />
      <span>{t("addc.duplicate", { name: duplicate.name })}</span>
      <button className="link" onClick={() => onOpen(duplicate.name)}>
        {t("addc.openIt", { name: duplicate.name })}
      </button>
    </div>
  ) : detected.kind === "bad" ? (
    <div className="cz-detect bad">
      <Icon name="warn" />
      {t(`addc.bad.${detected.why}`)}
    </div>
  ) : detected.kind === "remote" ? (
    <div className="cz-detect">
      <Icon name="web" />
      <span>
        <b>{t("addc.remote")}</b> · {detected.insecure ? t("addc.insecure") : t("addc.willCheck")}
      </span>
    </div>
  ) : (
    <div className="cz-detect">
      <Icon name="terminal" />
      <span>
        <b>{t("addc.local")}</b> · {detected.argv[0]}
        {detected.env.length > 0 && ` · ${t("addc.vars", { count: detected.env.length })}`}
      </span>
    </div>
  );

  const summary = (editable: boolean) => {
    const remote = d?.kind === "remote";
    return (
      <div className="cz-summary">
        {probed?.icon ? (
          <ServerTile name={name} icon={probed.icon} />
        ) : (
          <div className="cz-tile">
            <Icon name={remote ? "web" : "terminal"} />
          </div>
        )}
        <div className="t">
          <div className="n">
            {name}
            <span className="badge">{remote ? (transport === "sse" ? t("addc.remoteSse") : t("addc.remoteShort")) : t("addc.local")}</span>
          </div>
          {(remote || step !== "local") && <div className="a mono">{remote ? (d as { url: string }).url : command}</div>}
        </div>
        {editable && (
          <button className="link" onClick={edit}>
            {t("addc.change")}
          </button>
        )}
      </div>
    );
  };

  const stepNote = (s: McpProbeStep | undefined): string | null => {
    if (!s?.detail && !(s?.state === "ok" && s.id === "connect")) return s?.detail ?? null;
    if (s.state === "failed") return s.detail ?? null;
    if (s.id === "connect" && s.state === "ok") return s.http_status === 401 ? t("addc.note.needsAuth") : t("addc.note.connected");
    if (s.id === "resource" && s.state === "ok") return t("addc.note.authServer", { issuer: s.detail?.replace(/^authorization server /, "") ?? "" });
    if (s.state === "skipped") return /API key/.test(s.detail ?? "") ? t("addc.note.apiKey") : t("addc.note.noLogin");
    if (s.id === "auth_server") return null;
    return s.detail ?? null;
  };
  const stepBadges = (s: McpProbeStep | undefined): string[] => {
    if (!s || s.state !== "ok") return [];
    if (s.id === "connect" && s.http_status) return [String(s.http_status)];
    if (s.id === "resource") return ["OAuth"];
    if (s.id === "auth_server") return [/no PKCE/.test(s.detail ?? "") ? t("addc.noPkce") : "PKCE", ...(probed?.suggested.client && probed.suggested.client !== "own" ? [t("addc.cap.auto")] : [])];
    return [];
  };
  const checkView = (
    <>
      {summary(false)}
      <div className="cz-steps-h">
        {failure ? (
          t("addc.checkFailed")
        ) : REMOTE_STEPS.every((id) => steps.some((s) => s.id === id && s.state !== "running")) ? (
          t("addc.checked")
        ) : (
          <>
            <span className="mini-spin cz-spin" />
            {t("addc.checking")}
          </>
        )}
      </div>
      <div className="cz-steps">
        {REMOTE_STEPS.map((id) => {
          const s = steps.find((x) => x.id === id);
          const state = s?.state ?? "wait";
          return (
            <div key={id} className={`stp ${state === "wait" ? "wait" : ""}`}>
              <span className="si">
                {state === "running" ? (
                  <span className="mini-spin" />
                ) : state === "ok" ? (
                  <span className="done">
                    <Icon name="check" />
                  </span>
                ) : state === "failed" ? (
                  <span className="fail">
                    <Icon name="x" />
                  </span>
                ) : state === "skipped" ? (
                  <span className="skip">
                    <Icon name="minus" />
                  </span>
                ) : (
                  <span className="ring" />
                )}
              </span>
              <div className="st">
                <b>{t(`addc.step.${id}` as "addc.step.connect")}</b>
                {stepNote(s) && <small>{stepNote(s)}</small>}
              </div>
              <div className="bs">
                {stepBadges(s).map((b) => (
                  <span key={b} className="badge mono">
                    {b}
                  </span>
                ))}
              </div>
            </div>
          );
        })}
      </div>
      {failure && !steps.some((s) => s.detail === failure) && <div className="cz-errbox cz-gap">{failure}</div>}
    </>
  );

  const fields: RemoteState = { auth, setAuth, client, setClient, clientId, setClientId, clientSecret, setClientSecret, token, setToken, header, setHeader, prefix, setPrefix, headers, setHeaders, transport, setTransport };
  const remoteView = (
    <>
      {summary(true)}
      {failure && <div className="cz-errbox cz-gap">{failure}</div>}
      <RemoteFields s={fields} probed={probed} host={(d as { host?: string } | null)?.host ?? ""} />
      <p className="cz-trust">{t("addc.trust")}</p>
    </>
  );

  const argv = splitCommand(command);
  const localView = (
    <>
      {summary(true)}
      <LocalFields command={command} setCommand={setCommand} env={env} setEnv={setEnv} />
      {failure && <div className="cz-errbox cz-gap">{failure}</div>}
      <p className="cz-trust">{t("addc.localTrust")}</p>
    </>
  );

  const loginView = (
    <div className="cz-center">
      <div className="big">
        <span className="mini-spin" />
      </div>
      <b>{t("addc.loginTitle", { name })}</b>
      <p>{t("addc.loginText", { issuer: probed?.auth?.oauth?.authorization_server ?? "" })}</p>
      {authorizeUrl && (
        <button className="link" onClick={() => openPage(authorizeUrl)}>
          {t("addc.openAgain")}
        </button>
      )}
    </div>
  );

  const approveView = (
    <div className="cz-center">
      <div className="big">
        <span className="mini-spin" />
      </div>
      <b>{t("addc.approveTitle")}</b>
      <p>{t("addc.approveText", { name })}</p>
    </div>
  );

  const doneView = (
    <div className="cz-center">
      <div className="big ok">
        <Icon name="check" />
      </div>
      <b>{t("addc.added", { name })}</b>
      <p>{t("addc.addedText", { count: tools.length })}</p>
      <div className="cz-toolchips">
        {tools.slice(0, 6).map((tool) => (
          <span key={tool}>{tool}</span>
        ))}
        {tools.length > 6 && <span>{t("addc.more", { count: tools.length })}</span>}
      </div>
    </div>
  );

  const entryView = (
    <>
      <p className="sub">{t("addc.sub")}</p>
      <div className="cz-form">
        <div className="cz-fi">
          <label htmlFor="cz-add-name">{t("addc.name")}</label>
          <input
            id="cz-add-name"
            className={`field ${errors.name ? "bad" : ""}`}
            value={name}
            placeholder={suggested || t("addc.namePlaceholder")}
            spellCheck={false}
            autoComplete="off"
            onChange={(e) => (setName(e.target.value), setErrors({}))}
            onKeyDown={(e) => {
              if (e.key === "Enter") void connect();
              // Tab takes the suggestion, as a completion does; with a name typed it moves on.
              if (e.key === "Tab" && !e.shiftKey && !name && suggested) {
                e.preventDefault();
                setName(suggested);
                setErrors({});
              }
            }}
          />
          {errors.name ? <div className="cz-ferr">{errors.name}</div> : <div className="note">{suggested ? t("addc.nameNoteSuggested", { name: suggested }) : t("addc.nameNote")}</div>}
        </div>
        <div className="cz-fi">
          <label htmlFor="cz-add-address">{t("addc.address")}</label>
          <textarea
            id="cz-add-address"
            rows={1}
            className={`field mono cz-grow ${errors.address ? "bad" : ""}`}
            value={address}
            placeholder={t("addc.addressPlaceholder")}
            spellCheck={false}
            autoFocus
            onChange={(e) => {
              setAddress(e.target.value);
              setErrors({});
              e.target.style.height = "auto";
              e.target.style.height = `${e.target.scrollHeight + 2}px`;
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                void connect();
              }
            }}
          />
          {errors.address ? <div className="cz-ferr">{errors.address}</div> : hint}
        </div>
      </div>
    </>
  );

  const okToAdd = step === "local" ? argv.length > 0 : auth === "token" ? !!token.trim() || headers.some((h) => h.value) : auth === "oauth" && client === "own" ? !!clientId.trim() : true;
  const foot =
    step === "entry" ? (
      <>
        <div className="left" />
        <button className="btn" onClick={close}>
          {t("addc.cancel")}
        </button>
        <button className="btn primary" disabled={!detected || detected.kind === "bad" || !!duplicate} onClick={() => void connect()}>
          {t("addc.connect")}
        </button>
      </>
    ) : step === "check" ? (
      failure ? (
        <>
          <div className="left">
            <button className="link" disabled={busy} onClick={() => void addAnyway()}>
              {t("addc.addAnyway")}
            </button>
          </div>
          <button className="btn" onClick={edit}>
            {t("addc.edit")}
          </button>
          <button className="btn primary" onClick={retry}>
            {t("addc.retry")}
          </button>
        </>
      ) : (
        <>
          <button className="btn" onClick={close}>
            {t("addc.cancel")}
          </button>
          <button className="btn primary" disabled>
            <span className="mini-spin cz-spin-solid" />
          </button>
        </>
      )
    ) : step === "login" ? (
      <button className="btn" onClick={() => (login.current?.abort(), setStep("remote"))}>
        {t("addc.cancel")}
      </button>
    ) : step === "approve" ? (
      <span className="note">{t("addc.approveFoot")}</span>
    ) : step === "done" ? (
      <button className="btn primary" onClick={onClose}>
        {t("addc.finish")}
      </button>
    ) : (
      <>
        <button className="btn" onClick={edit}>
          {t("addc.back")}
        </button>
        <button className="btn primary" disabled={busy || !okToAdd} onClick={() => void add(step)}>
          {busy && <span className="mini-spin cz-spin-solid" />}
          {step === "remote" && auth === "oauth" ? t("addc.addAndLogin") : step === "local" ? t("addc.addAndApprove") : t("addc.add")}
        </button>
      </>
    );

  const title = step !== "entry" && d?.kind === "local" ? t("addc.titleLocal") : t("addc.title");
  return (
    <Dialog label={title} width={520} className="cz-sheet cz-add" onClose={step === "approve" ? () => {} : close}>
      <h3>{title}</h3>
      <div className="cz-add-body" data-step={step}>
        {step === "entry" ? entryView : step === "check" ? checkView : step === "remote" ? remoteView : step === "local" ? localView : step === "login" ? loginView : step === "approve" ? approveView : doneView}
      </div>
      <div className="sheet-foot">{foot}</div>
    </Dialog>
  );
}
