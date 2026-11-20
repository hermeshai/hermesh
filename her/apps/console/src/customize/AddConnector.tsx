import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { McpProbeResult, McpProbeStep, McpProbeStepId, McpServerConfig } from "@hermesh/her-protocol";
import { Dialog } from "../components";
import { errorText } from "../format";
import { useHer } from "../her";
import { Icon } from "../icons";
import { openPage, signInRemotely } from "../signin";
import { LocalFields, RemoteFields, type Auth, type Client, type RemoteState, type Transport } from "./ConnectorFields";
import { detect, suggestName, validateLocalRows, validateProgram, type Detected, type Row } from "./detect";
import { localIdentity, remoteIdentity } from "./identity";
import { credentialName, serverName } from "./model";
import { ServerTile } from "./parts";
import type { Customize } from "./useCustomize";

// Add Connector:
// explicit choice between Remote URL (HTTP/SSE) and Local Program (stdio). A remote address is
// checked (mcp/probe), then its sign-in is set up with what the check found, and nothing is
// written before a check passes unless the person adds it anyway. A local stdio program shows
// its execution host and structured executable/args/env, then the owner's device signs it with
// the root key (docs/her-server.md §8) and checks it once added: a command is never run before that.

type Mode = "remote" | "local";
type Step = "entry" | "check" | "remote" | "login" | "approve" | "done";
const REMOTE_STEPS: McpProbeStepId[] = ["connect", "resource", "auth_server"];

export function AddConnector({ c, onClose, onAdded, onOpen }: { c: Customize; onClose: () => void; onAdded: (name: string) => void; onOpen: (name: string) => void }) {
  const { t } = useTranslation("customize");
  const { rpc, show, host } = useHer();
  const [mode, setMode] = useState<Mode>("remote");
  const [step, setStep] = useState<Step>("entry");
  const [name, setName] = useState("");
  const [address, setAddress] = useState("");
  const [paste, setPaste] = useState("");
  const [pasteError, setPasteError] = useState<string | null>(null);
  const [pasteRemote, setPasteRemote] = useState<Extract<Detected, { kind: "remote" }> | null>(null);
  const [errors, setErrors] = useState<{ name?: string; address?: string; program?: string; args?: string; env?: string }>({});
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
  const [program, setProgram] = useState("");
  const [args, setArgs] = useState<string[]>([]);
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
  const detected = mode === "remote" ? detect(address) : null;
  const localDetected: Detected | null = mode === "local" && program.trim() ? { kind: "local", argv: [program.trim(), ...args], env } : null;
  const duplicate = mode === "remote" ? sameAs(detected) : sameAs(localDetected);
  const suggested = name ? "" : suggestName(mode === "remote" ? (detected?.kind === "remote" ? detected : null) : localDetected);

  const switchMode = (next: Mode) => {
    setMode(next);
    setErrors({});
    setFailure(null);
    setPasteError(null);
    setPasteRemote(null);
  };

  const applyLocalDetected = (det: Extract<Detected, { kind: "local" }>) => {
    setProgram(det.argv[0] ?? "");
    setArgs(det.argv.slice(1));
    setEnv(det.env);
    if (!name.trim()) {
      const sug = suggestName(det);
      if (sug) setName(sug);
    }
    setMode("local");
    setErrors({});
    setFailure(null);
    setPaste("");
    setPasteError(null);
    setPasteRemote(null);
  };

  const applyQuickPaste = () => {
    setPasteError(null);
    setPasteRemote(null);
    const det = detect(paste);
    if (!det) return;
    if (det.kind === "bad") {
      setPasteError(t(`addc.bad.${det.why}`));
      return;
    }
    if (det.kind === "remote") {
      setPasteRemote(det);
      return;
    }
    applyLocalDetected(det);
  };

  const discard = async () => {
    for (const credential of staged.current) await rpc?.request("credentials/delete", { name: credential }).catch(() => {});
    staged.current = [];
  };
  const close = () => {
    if (mode === "local" && busy) return;
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
    const n = (name || suggestName(det?.kind === "remote" ? det : null)).trim();
    const errs: typeof errors = {};
    if (!serverName.test(n)) errs.name = t("addc.badName");
    else if (existing.has(n)) errs.name = t("addc.taken", { name: n });
    if (!det || det.kind === "bad") errs.address = t(`addc.bad.${det?.kind === "bad" ? det.why : "empty"}`);
    else if (det.kind === "local") errs.address = t("addc.looksLikeLocal");
    else if (sameAs(det)) return;
    setErrors(errs);
    if (errs.name || errs.address || !det || det.kind !== "remote" || !rpc) return;
    setName(n);
    setD(det);
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
    done(name, check?.tools.map((tool) => tool.name) ?? []);
  };

  const done = (addedName: string, listed: string[]) => {
    setTools(listed);
    setStep("done");
    onAdded(addedName);
    show(t("toast.serverAdded", { name: addedName, count: listed.length }));
  };

  /** The local stdio command, validated and signed on the owner's device; then checked once. */
  const addCommand = async () => {
    if (!rpc) return;
    const trimmedProgram = program.trim();
    const n = (name || suggestName(localDetected)).trim();
    const errs: typeof errors = {};
    if (!serverName.test(n)) errs.name = t("addc.badName");
    else if (existing.has(n)) errs.name = t("addc.taken", { name: n });
    const pIssue = validateProgram(program);
    if (pIssue) errs.program = t(`addc.errProgram.${pIssue}`);
    const rowCheck = validateLocalRows(n || "connector", args, env);
    if (rowCheck.argsError) errs.args = t(`addc.errArgs.${rowCheck.argsError}`);
    if (rowCheck.envError) errs.env = t(`addc.errEnv.${rowCheck.envError.kind}`, { key: rowCheck.envError.key });
    if (!errs.program && duplicate) errs.program = t("addc.duplicate", { name: duplicate.name });
    setErrors(errs);
    if (Object.keys(errs).length) return;

    setName(n);
    setBusy(true);
    setFailure(null);
    try {
      setStep("approve");
      const check = await c.saveCommand({ name: n, command: trimmedProgram, args }, env);
      if (!alive.current) return;
      if (check?.server.status.state === "ok") return done(n, check.tools.map((tool) => tool.name));
      // Added, but it does not work yet: its page says why, with Retry.
      onOpen(n);
      show(t("toast.serverAddedFailed", { name: n, error: check?.server.status.error ?? "" }), undefined, true);
    } catch (e) {
      if (!alive.current) return;
      setFailure(errorText(e));
      setStep("entry");
    } finally {
      setBusy(false);
    }
  };

  const add = async () => {
    if (!rpc) return;
    setBusy(true);
    setFailure(null);
    try {
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
      void add();
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
      <button type="button" className="link" onClick={() => onOpen(duplicate.name)}>
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
      <span>{t("addc.looksLikeLocal")}</span>
      <button type="button" className="link" onClick={() => applyLocalDetected(detected)}>
        {t("addc.switchToLocal")}
      </button>
    </div>
  );

  const summary = (editable: boolean) => (
    <div className="cz-summary">
      {probed?.icon ? (
        <ServerTile name={name} icon={probed.icon} />
      ) : (
        <div className="cz-tile">
          <Icon name="web" />
        </div>
      )}
      <div className="t">
        <div className="n">
          {name}
          <span className="badge">{transport === "sse" ? t("addc.remoteSse") : t("addc.remoteShort")}</span>
        </div>
        <div className="a mono">{(d as { url?: string } | null)?.url ?? address}</div>
      </div>
      {editable && (
        <button type="button" className="link" onClick={edit}>
          {t("addc.change")}
        </button>
      )}
    </div>
  );

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
      <p>{host?.machine_name ? t("addc.approveTextHost", { name, host: host.machine_name }) : t("addc.approveText", { name })}</p>
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
      <div className="seg cz-modeseg" role="group" aria-label={t("addc.modeLabel")}>
        <button type="button" className={mode === "remote" ? "on" : ""} aria-pressed={mode === "remote"} onClick={() => switchMode("remote")}>
          <Icon name="web" />
          {t("addc.modeRemote")}
        </button>
        <button type="button" className={mode === "local" ? "on" : ""} aria-pressed={mode === "local"} onClick={() => switchMode("local")}>
          <Icon name="terminal" />
          {t("addc.modeLocal")}
        </button>
      </div>
      <p className="sub">{mode === "remote" ? t("addc.subRemote") : t("addc.subLocal")}</p>
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
            onChange={(e) => (setName(e.target.value), setErrors((prev) => ({ ...prev, name: undefined })))}
            onKeyDown={(e) => {
              if (e.key === "Enter" && mode === "remote") void connect();
              // Tab takes the suggestion, as a completion does; with a name typed it moves on.
              if (e.key === "Tab" && !e.shiftKey && !name && suggested) {
                e.preventDefault();
                setName(suggested);
                setErrors((prev) => ({ ...prev, name: undefined }));
              }
            }}
          />
          {errors.name ? <div className="cz-ferr">{errors.name}</div> : <div className="note">{suggested ? t("addc.nameNoteSuggested", { name: suggested }) : t("addc.nameNote")}</div>}
        </div>
        {mode === "remote" ? (
          <div className="cz-fi">
            <label htmlFor="cz-add-address">{t("addc.address")}</label>
            <textarea
              id="cz-add-address"
              rows={1}
              className={`field mono cz-grow ${errors.address ? "bad" : ""}`}
              value={address}
              placeholder={t("addc.addressPlaceholderRemote")}
              spellCheck={false}
              autoFocus
              onChange={(e) => {
                setAddress(e.target.value);
                setErrors((prev) => ({ ...prev, address: undefined }));
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
        ) : (
          <>
            <div className="cz-fi">
              <label htmlFor="cz-quick-paste">{t("addc.quickPaste")}</label>
              <div className="cz-paste-row">
                <textarea
                  id="cz-quick-paste"
                  rows={1}
                  className={`field mono cz-grow ${pasteError ? "bad" : ""}`}
                  value={paste}
                  placeholder={t("addc.quickPastePlaceholder")}
                  spellCheck={false}
                  onChange={(e) => {
                    setPaste(e.target.value);
                    setPasteError(null);
                    setPasteRemote(null);
                    e.target.style.height = "auto";
                    e.target.style.height = `${e.target.scrollHeight + 2}px`;
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && !e.shiftKey) {
                      e.preventDefault();
                      applyQuickPaste();
                    }
                  }}
                />
                <button type="button" className="btn" disabled={!paste.trim()} onClick={applyQuickPaste}>
                  {t("addc.fillFields")}
                </button>
              </div>
              {pasteError && <div className="cz-ferr">{pasteError}</div>}
              {pasteRemote && (
                <div className="cz-detect">
                  <Icon name="web" />
                  <span>{t("addc.looksLikeRemote")}</span>
                  <button
                    type="button"
                    className="link"
                    onClick={() => {
                      setAddress(pasteRemote.json ? paste : pasteRemote.url);
                      setPaste("");
                      if (!name.trim() && suggestName(pasteRemote)) setName(suggestName(pasteRemote));
                      switchMode("remote");
                    }}
                  >
                    {t("addc.switchToRemote")}
                  </button>
                </div>
              )}
            </div>
            <LocalFields
              program={program}
              setProgram={(v) => (setProgram(v), setErrors((prev) => ({ ...prev, program: undefined })))}
              args={args}
              setArgs={(v) => (setArgs(v), setErrors((prev) => ({ ...prev, args: undefined })))}
              env={env}
              setEnv={(v) => (setEnv(v), setErrors((prev) => ({ ...prev, env: undefined })))}
              hostName={host?.machine_name}
              programError={errors.program}
              argsError={errors.args}
              envError={errors.env}
            />
            {duplicate && (
              <div className="cz-detect bad">
                <Icon name="warn" />
                <span>{t("addc.duplicate", { name: duplicate.name })}</span>
                <button type="button" className="link" onClick={() => onOpen(duplicate.name)}>
                  {t("addc.openIt", { name: duplicate.name })}
                </button>
              </div>
            )}
            {failure && <div className="cz-errbox cz-gap">{failure}</div>}
            <p className="cz-trust">{t("addc.localTrust")}</p>
          </>
        )}
      </div>
    </>
  );

  const okToAddRemote = auth === "token" ? !!token.trim() || headers.some((h) => h.value) : auth === "oauth" && client === "own" ? !!clientId.trim() : true;
  const foot =
    step === "entry" ? (
      mode === "remote" ? (
        <>
          <div className="left" />
          <button className="btn" onClick={close}>
            {t("addc.cancel")}
          </button>
          <button className="btn primary" disabled={!detected || detected.kind !== "remote" || !!duplicate} onClick={() => void connect()}>
            {t("addc.connect")}
          </button>
        </>
      ) : (
        <>
          <div className="left" />
          <button className="btn" onClick={close}>
            {t("addc.cancel")}
          </button>
          <button className="btn primary" disabled={busy || !program.trim() || !!duplicate} onClick={() => void addCommand()}>
            {busy && <span className="mini-spin cz-spin-solid" />}
            {t("addc.addAndApprove")}
          </button>
        </>
      )
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
        <button className="btn primary" disabled={busy || !okToAddRemote} onClick={() => void add()}>
          {busy && <span className="mini-spin cz-spin-solid" />}
          {auth === "oauth" ? t("addc.addAndLogin") : t("addc.add")}
        </button>
      </>
    );

  const title = mode === "local" ? t("addc.titleLocal") : t("addc.title");
  return (
    <Dialog label={title} width={520} className="cz-sheet cz-add" onClose={step === "approve" ? () => {} : close}>
      <h3>{title}</h3>
      <div className="cz-add-body" data-step={step === "entry" && mode === "local" ? "local" : step}>
        {step === "entry" ? entryView : step === "check" ? checkView : step === "remote" ? remoteView : step === "login" ? loginView : step === "approve" ? approveView : doneView}
      </div>
      <div className="sheet-foot">{foot}</div>
    </Dialog>
  );
}
