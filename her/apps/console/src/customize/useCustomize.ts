import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { RpcError } from "@hermesh/her-client";
import type { ConfigEdit, ConfigReadResult, McpCheckResult, McpListResult, McpServerView, SkillsListResult } from "@hermesh/her-protocol";
import { errorText } from "../format";
import { TOAST_MS, useHer } from "../her";
import { i18n } from "../i18n";
import { signCommandConnector, type CommandConnector } from "../routes";
import { openPage, signInRemotely } from "../signin";
import { quoteKey, skillItems, type SkillItem } from "./model";

const t = i18n.getFixedT(null, "customize");

// The data of the Connectors and Skills pages: skills/list,
// mcp/list and config/read of her's user layer; read again on skills/changed, config/changed
// and mcp/changed (asking subscribes), and when the window comes back. Changes show at once
// and go back if her refuses them; removals wait for the toast's Undo. Sign-ins end on the
// owner's device (docs/her-server.md §5.4), which also signs a connector started as a command
// (§8): the page never writes one.

export type Customize = ReturnType<typeof useCustomize>;

export function useCustomize() {
  const { rpc, show } = useHer();
  const [skills, setSkills] = useState<SkillsListResult | null>(null);
  const [mcp, setMcp] = useState<McpListResult | null>(null);
  const [config, setConfig] = useState<ConfigReadResult | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  /** Switches turned here that her has not confirmed yet: skill folder or server name → on. */
  const [switching, setSwitching] = useState<Map<string, boolean>>(new Map());
  /** Removed here, waiting for the toast's Undo to pass. */
  const [hidden, setHidden] = useState<Set<string>>(new Set());
  const [checking, setChecking] = useState<Set<string>>(new Set());
  /** Servers waiting for their sign-in in the browser: name → the address to open again. */
  const [signingIn, setSigningIn] = useState<Map<string, string>>(new Map());
  const loginStops = useRef(new Map<string, () => void>());
  useEffect(
    () => () => {
      for (const stop of loginStops.current.values()) stop();
      loginStops.current.clear();
    },
    [rpc],
  );

  const readSkills = useCallback(async () => {
    if (rpc) setSkills(await rpc.request("skills/list", {}));
  }, [rpc]);
  const readServers = useCallback(async () => {
    if (!rpc) return;
    const [m, c] = await Promise.all([rpc.request("mcp/list", {}), rpc.request("config/read", { layers: true })]);
    setMcp(m);
    setConfig(c);
  }, [rpc]);
  const readAll = useCallback(
    () =>
      Promise.all([readSkills(), readServers()])
        .then(() => setFailed(null))
        .catch((e) => setFailed(errorText(e))),
    [readSkills, readServers],
  );

  useEffect(() => {
    if (!rpc) return;
    void readAll();
    const offs = [
      rpc.on("skills/changed", () => void readSkills().catch(() => {})),
      rpc.on("config/changed", () => void readServers().then(readSkills).catch(() => {})),
      rpc.on("mcp/changed", () => void readServers().catch(() => {})),
    ];
    const focus = () => void readAll();
    window.addEventListener("focus", focus);
    return () => {
      offs.forEach((off) => off());
      window.removeEventListener("focus", focus);
    };
  }, [rpc, readAll, readSkills, readServers]);

  // What her says now makes a switch turned here moot.
  useEffect(() => {
    setSwitching((m) => (m.size ? new Map() : m));
  }, [skills, mcp]);

  // Removals still waiting go through when the page closes.
  const removals = useRef(new Map<string, { timer: ReturnType<typeof setTimeout>; commit: () => Promise<unknown> }>());
  useEffect(
    () => () => {
      for (const r of removals.current.values()) {
        clearTimeout(r.timer);
        void r.commit().catch(() => {});
      }
      removals.current.clear();
    },
    [],
  );

  /** Takes `key` off the page now, and runs `commit` once the toast's Undo has passed. */
  const removeLater = useCallback(
    (key: string, text: string, commit: () => Promise<unknown>) => {
      setHidden((h) => new Set(h).add(key));
      const unhide = () => setHidden((h) => (h.delete(key) ? new Set(h) : h));
      const timer = setTimeout(() => {
        removals.current.delete(key);
        commit()
          .then(() => readAll())
          .catch((e) => show(errorText(e), undefined, true))
          .finally(unhide);
      }, TOAST_MS);
      removals.current.set(key, { timer, commit });
      show(text, () => {
        clearTimeout(timer);
        removals.current.delete(key);
        unhide();
      });
    },
    [show, readAll],
  );

  /** config/write of the user's file against the version read; when the file changed meanwhile, read it again and write once more. */
  const write = useCallback(
    async (edits: ConfigEdit[]) => {
      if (!rpc) throw new Error(i18n.t("console:error.notConnected"));
      const call = (expected_version?: string | null) => rpc.request("config/write", { layer: "user", edits, expected_version });
      try {
        return await call(config?.layers?.find((l) => l.layer === "user")?.version);
      } catch (e) {
        if (!(e instanceof RpcError && e.kind === "config_version_conflict")) throw e;
        const fresh = await rpc.request("config/read", { layers: true });
        setConfig(fresh);
        return call(fresh.layers?.find((l) => l.layer === "user")?.version);
      }
    },
    [rpc, config],
  );

  /** mcp/check: connects once, lists the tools, and the cache the page reads remembers them. */
  const check = useCallback(
    async (name: string): Promise<McpCheckResult | null> => {
      if (!rpc) return null;
      setChecking((c) => new Set(c).add(name));
      try {
        const r = await rpc.request("mcp/check", { name });
        await readServers();
        return r;
      } catch (e) {
        show(errorText(e), undefined, true);
        return null;
      } finally {
        setChecking((c) => {
          const next = new Set(c);
          next.delete(name);
          return next;
        });
      }
    },
    [rpc, readServers, show],
  );

  /** Signs in to a server already added, through the owner's device, then checks it once. */
  const login = useCallback(
    async (name: string) => {
      if (!rpc || loginStops.current.has(name)) return;
      const abort = new AbortController();
      loginStops.current.set(name, () => abort.abort());
      setSigningIn((m) => new Map(m).set(name, ""));
      try {
        await signInRemotely({
          path: "/callback",
          start: (redirect_uri) => rpc.request("mcp/login", { name, redirect_uri }),
          open: (url) => {
            setSigningIn((m) => new Map(m).set(name, url));
            openPage(url);
          },
          complete: (login_id, g) => rpc.request("mcp/login_complete", { login_id, code: g.code, state: g.state, iss: g.iss }),
          cancel: () => rpc.request("mcp/login_cancel", { name }),
          signal: abort.signal,
        });
        show(t("toast.signedIn", { name }));
        await check(name);
      } catch (e) {
        if (!abort.signal.aborted) show(t("toast.loginFailed", { name, error: errorText(e) }), undefined, true);
      } finally {
        loginStops.current.delete(name);
        setSigningIn((m) => {
          const next = new Map(m);
          next.delete(name);
          return next;
        });
      }
    },
    [rpc, check, show],
  );

  /** Stops waiting for a server's sign-in; her's side is cancelled too. */
  const cancelLogin = useCallback((name: string) => loginStops.current.get(name)?.(), []);

  /** mcp/logout: forgets the server's sign-in, then checks it, so the page says it needs one. */
  const logout = useCallback(
    async (name: string) => {
      if (!rpc) return;
      try {
        await rpc.request("mcp/logout", { name });
        show(t("toast.signedOut", { name }));
        await check(name);
      } catch (e) {
        show(errorText(e), undefined, true);
      }
    },
    [rpc, check, show],
  );

  // ---- skills ----

  const items = useMemo(
    () =>
      skillItems(skills)
        .filter((i) => !hidden.has(`skill:${i.dir}`))
        .map((i) => (switching.has(i.dir) ? { ...i, on: switching.get(i.dir)! } : i)),
    [skills, hidden, switching],
  );

  const switchSkill = useCallback(
    async (item: SkillItem, on: boolean, done: string) => {
      if (!rpc) return;
      setSwitching((m) => new Map(m).set(item.dir, on));
      try {
        await rpc.request("skills/set_enabled", { path: item.dir, enabled: on });
        await readSkills();
        show(done);
      } catch (e) {
        setSwitching((m) => {
          const next = new Map(m);
          next.delete(item.dir);
          return next;
        });
        show(errorText(e), undefined, true);
      }
    },
    [rpc, readSkills, show],
  );

  const removeSkill = useCallback(
    (item: SkillItem) => {
      if (!rpc) return;
      removeLater(`skill:${item.dir}`, t("toast.skillRemoved", { name: item.entry.name }), () => rpc.request("skills/remove", { path: item.dir }));
    },
    [rpc, removeLater],
  );

  // ---- servers ----

  const servers = useMemo(
    () =>
      (mcp?.servers ?? [])
        .filter((s) => !hidden.has(`server:${s.name}`))
        .map((s) => (switching.has(s.name) ? { ...s, enabled: switching.get(s.name)! } : s)),
    [mcp, hidden, switching],
  );

  /** On or off in the user's file: off writes `enabled = false`, on takes it out (or says `true` over another file). */
  const switchServer = useCallback(
    async (s: McpServerView, on: boolean, done: string) => {
      const value = on ? (s.disabled_by === "user" ? null : true) : false;
      setSwitching((m) => new Map(m).set(s.name, on));
      try {
        const r = await write([{ key_path: `mcp_servers.${quoteKey(s.name)}.enabled`, value, merge: "replace" }]);
        await readServers();
        const over = r.overridden_by?.[0];
        if (over) show(t(on ? "toast.overriddenOff" : "toast.overriddenOn", { name: s.name, file: over.file ?? over.layer }), undefined, true);
        else show(done);
        if (on && s.status.state !== "ok") void check(s.name);
      } catch (e) {
        setSwitching((m) => {
          const next = new Map(m);
          next.delete(s.name);
          return next;
        });
        show(errorText(e), undefined, true);
      }
    },
    [write, readServers, check, show],
  );

  /** Goes once the toast's Undo has passed. Taking a connector away lets Her do less, so one started as a command needs no signature for it. */
  const removeServer = useCallback(
    (s: McpServerView) => {
      const credentials = !!s.connector || (s.credentials ?? []).length > 0;
      removeLater(`server:${s.name}`, t(credentials ? "toast.serverRemovedWithSecret" : "toast.serverRemoved", { name: s.name }), async () => {
        if (s.connector) await rpc?.request("mcp/logout", { name: s.name });
        return write([{ key_path: `mcp_servers.${quoteKey(s.name)}`, value: null, merge: "replace" }]);
      });
    },
    [rpc, write, removeLater],
  );

  /**
   * Adds or changes a remote server: its secrets go to the credential store first, then its
   * settings to the user's file, then one check. `upsert` changes only the fields given (null
   * takes one out), keeping what else the file says.
   */
  const saveServer = useCallback(
    async (name: string, server: Record<string, unknown>, secrets: [string, string][], merge: "replace" | "upsert" = "replace") => {
      if (!rpc) throw new Error(i18n.t("console:error.notConnected"));
      for (const [credential, value] of secrets) await rpc.request("credentials/set", { name: credential, value });
      const written = await write([{ key_path: `mcp_servers.${quoteKey(name)}`, value: server, merge }]);
      await readServers();
      return { written, check: await check(name) };
    },
    [rpc, write, readServers, check],
  );

  /** Adds or changes a server started as a command: the owner's Mac signs it with Touch ID, then it is checked once. */
  const saveCommand = useCallback(
    async (c: CommandConnector) => {
      await signCommandConnector(c);
      await readServers();
      return check(c.name);
    },
    [readServers, check],
  );

  return {
    skills,
    items,
    mcp,
    servers,
    config,
    failed,
    loaded: skills !== null && mcp !== null && config !== null,
    checking,
    signingIn,
    readAll,
    readSkills,
    readServers,
    write,
    check,
    login,
    cancelLogin,
    logout,
    switchSkill,
    removeSkill,
    switchServer,
    removeServer,
    saveServer,
    saveCommand,
  };
}
