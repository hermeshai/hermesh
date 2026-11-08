import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent as KeyEvent, type PointerEvent as PointerDown } from "react";
import { flushSync } from "react-dom";
import { useTranslation } from "react-i18next";
import type { WebSearchSource, WebSearchStatus, WebSearchTestResult } from "@hermesh/her-protocol";
import { Dialog, PageTop } from "../components";
import { PopMenu, type MenuItem } from "../customize/parts";
import { agoText, errorText } from "../format";
import { useHer } from "../her";
import { Icon } from "../icons";
import { Brand } from "./providers";

// Web Search: whether the model may
// search, and one list of services. From the top down, the first usable one searches; when it
// fails, the next. The order is the setting: a grip on every row. A service that has what it
// needs has a switch; one that does not stays in its place, greyed, with the one thing to do.

const logos = import.meta.glob<string>("../assets/search/*.{svg,png}", { eager: true, query: "?url", import: "default" });
const logoOf = (id: string) => logos[`../assets/search/${id}.svg`] ?? logos[`../assets/search/${id}.png`];
/** Marks with no ground of their own sit on a white tile, in either theme. */
const onTile = new Set(["exa", "tavily", "brave", "bocha"]);

function Mark({ source }: { source: WebSearchSource }) {
  const logo = logoOf(source.id);
  if (!logo) return <Brand id={source.id} name={source.name} />;
  return (
    <span className={`ws-mark ${onTile.has(source.id) ? "inset" : ""}`} aria-hidden="true">
      <img src={logo} alt="" />
    </span>
  );
}

export function SearchPage({ go }: { go: (page: "models") => void }) {
  const { t } = useTranslation("settings");
  const { t: tc } = useTranslation();
  const her = useHer();
  const { rpc } = her;
  const [status, setStatus] = useState<WebSearchStatus | null>(null);
  const [sheet, setSheet] = useState<WebSearchSource | null>(null);
  const [menu, setMenu] = useState<{ source: WebSearchSource; anchor: DOMRect } | null>(null);
  const [moving, setMoving] = useState<string | null>(null);

  // The order and switches on screen are ahead of what her says for a moment; while a row is moving nothing else changes them.
  const movingId = useRef<string | null>(null);
  // Answers can overtake each other: only the last question's counts.
  const asked = useRef(0);
  const read = useCallback(async () => {
    if (!rpc) return;
    const mine = ++asked.current;
    const next = await rpc.request("web_search/status", {});
    if (mine === asked.current && movingId.current === null) setStatus(next);
  }, [rpc]);
  useEffect(() => {
    const again = () => void read().catch(() => {});
    again();
    const off = rpc?.on("config/changed", again);
    // A search may have failed, or a key arrived from elsewhere, while the window was behind.
    window.addEventListener("focus", again);
    return () => {
      off?.();
      window.removeEventListener("focus", again);
    };
  }, [rpc, read]);
  /** Writes to the user's own file, then the page asks again (a failed write puts the page back). Whether it was written. */
  const write = useCallback(
    async (edits: { key_path: string; value: unknown }[]) => {
      let written = false;
      try {
        await her.call("config/write", { layer: "user", edits });
        written = true;
      } catch (e) {
        her.show(errorText(e), undefined, true);
      }
      await read().catch(() => {});
      return written;
    },
    [her, read],
  );

  const sources = status?.sources ?? [];
  const ids = sources.map((s) => s.id);
  const saveOrder = (order: string[]) => void write([{ key_path: "web_search.order", value: order }]);
  const toggle = (source: WebSearchSource) => {
    const next = sources.map((s) => (s.id === source.id ? { ...s, enabled: !s.enabled } : s));
    setStatus((now) => now && { ...now, sources: next });
    void write([{ key_path: "web_search.disabled", value: next.filter((s) => !s.enabled).map((s) => s.id) }]);
  };
  const setMaster = (on: boolean) => {
    setStatus((now) => now && { ...now, enabled: on });
    void write([{ key_path: "web_search.enabled", value: on }]).then((written) => written && her.show(t(on ? "search.masterOn" : "search.masterOff")));
  };

  // Rows that changed places glide there from where they were. Layout positions: a row's own
  // sliding never decides where another goes. A forced reflow starts it.
  const rows = useRef(new Map<string, HTMLDivElement>());
  const tops = useRef(new Map<string, number>());
  useLayoutEffect(() => {
    for (const [id, el] of rows.current) {
      const before = tops.current.get(id);
      const now = el.offsetTop;
      tops.current.set(id, now);
      if (before === undefined || before === now || id === movingId.current) continue;
      el.style.transition = "none";
      el.style.transform = `translateY(${before - now}px)`;
      void el.offsetHeight;
      el.style.transition = "transform .16s var(--ease-out)";
      el.style.transform = "";
    }
  });
  const place = (id: string, to: number) =>
    flushSync(() =>
      setStatus((now) => {
        if (!now) return now;
        const list = [...now.sources];
        const from = list.findIndex((s) => s.id === id);
        list.splice(to, 0, ...list.splice(from, 1));
        return { ...now, sources: list };
      }),
    );
  const latest = useRef(ids);
  latest.current = ids;

  const drag = (e: PointerDown<HTMLButtonElement>, id: string) => {
    if (e.button !== 0) return;
    e.preventDefault();
    e.currentTarget.focus({ preventScroll: true });
    const row = rows.current.get(id);
    if (!row) return;
    const started = latest.current.join();
    let start = e.clientY;
    movingId.current = id;
    setMoving(id);
    document.documentElement.classList.add("ws-moving");
    const midOf = (el: HTMLElement) => el.offsetTop + el.offsetHeight / 2;
    const move = (ev: PointerEvent) => {
      // A fast drag can pass several rows at once.
      for (;;) {
        const order = latest.current;
        const at = order.indexOf(id);
        const mid = midOf(row) + ev.clientY - start;
        const next = rows.current.get(order[at + 1]);
        const prev = rows.current.get(order[at - 1]);
        const below = !!next && mid > midOf(next);
        const above = !!prev && mid < midOf(prev);
        if (!below && !above) break;
        const top = row.offsetTop;
        place(id, below ? at + 1 : at - 1);
        start += row.offsetTop - top;
      }
      row.style.transition = "none";
      row.style.transform = `translateY(${ev.clientY - start}px)`;
    };
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      window.removeEventListener("pointercancel", up);
      document.documentElement.classList.remove("ws-moving");
      movingId.current = null;
      setMoving(null);
      row.style.transition = "transform .16s var(--ease-out)";
      row.style.transform = "";
      gripOf(id)?.focus({ preventScroll: true });
      if (latest.current.join() !== started) saveOrder(latest.current);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
    window.addEventListener("pointercancel", up);
  };
  /** Moving a row in the document takes the focus from its grip. */
  const gripOf = (id: string) => rows.current.get(id)?.querySelector<HTMLButtonElement>(".ws-grip");
  const stepKey = (e: KeyEvent<HTMLButtonElement>, id: string) => {
    if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
    e.preventDefault();
    const at = latest.current.indexOf(id);
    const to = e.key === "ArrowUp" ? at - 1 : at + 1;
    if (to < 0 || to >= latest.current.length) return;
    place(id, to);
    gripOf(id)?.focus({ preventScroll: true });
    saveOrder(latest.current);
  };

  const worked = (name: string, r: WebSearchTestResult) => t("search.worked", { name, count: r.count, seconds: (r.elapsed_ms / 1000).toFixed(1) });
  const test = async (source: WebSearchSource) => {
    try {
      const r = await her.call("web_search/test", { source: source.id });
      if (r.ok) her.show(worked(source.name, r));
      else her.show(t("search.testFailed", { name: source.name, why: t(`search.why.${r.error ?? "unreachable"}`) }), undefined, true);
    } catch (e) {
      her.show(errorText(e), undefined, true);
    }
    await read().catch(() => {});
  };
  const removeKey = (source: WebSearchSource) => {
    const free = source.needs === "nothing";
    her.confirm({
      title: t("search.removeTitle", { name: source.name }),
      message: t(free ? "search.removeNoteFree" : "search.removeNote"),
      button: t("search.remove"),
      run: async () => {
        await her.call("credentials/delete", { name: `web-search.${source.id}` });
        await read().catch(() => {});
        her.show(free ? t("search.removedFree") : t("search.removed", { name: source.name }));
      },
    });
  };
  const menuItems = (source: WebSearchSource): MenuItem[] => [
    { id: "test", label: t("search.test"), run: () => void test(source) },
    ...(source.key === "saved" ? [{ id: "replace", label: t("search.replaceKey"), run: () => setSheet(source) }, null, { id: "remove", label: t("search.removeKey"), danger: true, run: () => removeKey(source) }] : []),
  ];

  const master = status?.enabled;
  return (
    <>
      <PageTop title={tc("nav.search")} />
      <div className="set-body">
        <div className="form search-set">
          <section>
            <div className="fgroup">
              <div className="frow">
                <div className="ft">
                  <div className="fn">{t("search.master")}</div>
                  <div className="fdesc">{t("search.masterNote")}</div>
                </div>
                <button className={`sw ${master ? "on" : ""}`} role="switch" aria-checked={!!master} aria-label={t("search.master")} disabled={!status} onClick={() => setMaster(!master)} />
              </div>
            </div>
          </section>
          <section className={status && !master ? "ws-dim" : ""}>
            <h3 className="fhead">{t("search.services")}</h3>
            <p className="ws-intro">{t("search.intro")}</p>
            <div className="fgroup" role="list" aria-label={t("search.services")}>
              {sources.map((source) => {
                const off = !source.configured || !source.enabled;
                const shown = source.id === status?.current && master;
                return (
                  <div
                    key={source.id}
                    role="listitem"
                    aria-label={source.name}
                    ref={(el) => {
                      if (el) rows.current.set(source.id, el);
                      else rows.current.delete(source.id);
                    }}
                    className={`frow mrow ws-row ${off ? "ws-off" : ""} ${moving === source.id ? "dragging" : ""}`}
                  >
                    <button className="ws-grip" aria-label={t("search.grip", { name: source.name })} title={t("search.gripTitle")} onPointerDown={(e) => drag(e, source.id)} onKeyDown={(e) => stepKey(e, source.id)}>
                      <Icon name="grip" />
                    </button>
                    <Mark source={source} />
                    <div className="ft">
                      <div className="fn">
                        {source.name}
                        {shown && <span className="badge ok">{t("search.current")}</span>}
                      </div>
                      <Description source={source} current={status?.current ?? null} />
                    </div>
                    <RowAction source={source} open={menu?.source.id === source.id} onKey={() => setSheet(source)} onConnect={() => go("models")} onMenu={(anchor) => setMenu(menu?.source.id === source.id ? null : { source, anchor })} />
                    {source.configured ? (
                      <button
                        className={`sw ${source.enabled ? "on" : ""}`}
                        role="switch"
                        aria-checked={source.enabled}
                        aria-label={t("search.use", { name: source.name })}
                        title={t(source.enabled ? "search.usedTitle" : "search.skippedTitle")}
                        onClick={() => toggle(source)}
                      />
                    ) : (
                      <span className="ws-slot" />
                    )}
                  </div>
                );
              })}
            </div>
            {status && master && !status.current && <p className="set-note ws-none">{t("search.noneLeft")}</p>}
          </section>
        </div>
      </div>
      {menu && <PopMenu anchor={menu.anchor} items={menuItems(menu.source)} label={t("search.more")} onClose={() => setMenu(null)} />}
      {sheet && (
        <KeySheet
          source={sheet}
          first={ids[0] === sheet.id}
          onClose={() => setSheet(null)}
          onSaved={(said, bad) => {
            setSheet(null);
            her.show(said, undefined, bad);
            void read().catch(() => {});
          }}
          worked={worked}
        />
      )}
    </>
  );
}

/** A row's second line: what went wrong last, else what it needs or where its key is from. */
function Description({ source, current }: { source: WebSearchSource; current: string | null }) {
  const { t } = useTranslation("settings");
  if (source.failure && source.configured && source.enabled) {
    const when = agoText(new Date(source.failure.at_ms), new Date());
    return (
      <div className="fdesc warn">
        {t("search.failed", { what: t(`search.failure.${source.failure.kind}`), when })}
        {current && current !== source.id ? t("search.usedNext") : ""}
      </div>
    );
  }
  if (source.needs === "account" && !source.configured) return <div className="fdesc">{t("search.needsAccount", { name: source.name })}</div>;
  if (source.key === "env") return <div className="fdesc">{t("search.fromEnv", { env: source.key_env ?? "" })}</div>;
  return <div className="fdesc">{t(`search.sources.${source.id}.${source.key === "saved" ? "keyed" : "desc"}` as "search.sources.exa.desc", { defaultValue: "" })}</div>;
}

/** The one thing to do with a row: connect its account, add a key, or (with a key) its menu. */
function RowAction({ source, open, onKey, onConnect, onMenu }: { source: WebSearchSource; open: boolean; onKey: () => void; onConnect: () => void; onMenu: (anchor: DOMRect) => void }) {
  const { t } = useTranslation("settings");
  if (source.needs === "account") {
    return source.configured ? null : (
      <button className="btn" onClick={onConnect}>
        {t("search.connect")}
      </button>
    );
  }
  if (source.key) {
    return (
      <button className="tb" aria-label={t("search.more")} title={t("search.more")} aria-expanded={open} aria-haspopup="menu" onClick={(e) => onMenu(e.currentTarget.getBoundingClientRect())}>
        <Icon name="more" />
      </button>
    );
  }
  return (
    <button className="btn" onClick={onKey}>
      {t("search.addKey")}
    </button>
  );
}

/** Adding or replacing a key: her tries it with a real search, and a key the service refuses is not saved. */
function KeySheet({ source, first, onClose, onSaved, worked }: { source: WebSearchSource; first: boolean; onClose: () => void; onSaved: (said: string, bad?: boolean) => void; worked: (name: string, r: WebSearchTestResult) => string }) {
  const { t } = useTranslation("settings");
  const her = useHer();
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const field = useId();
  const replacing = source.key === "saved";
  const save = async () => {
    if (busy || !key.trim()) return;
    setBusy(true);
    setError("");
    try {
      const r = await her.call("web_search/set_key", { source: source.id, key: key.trim() });
      if (!r.saved) setError(t("search.refused", { name: source.name }));
      else if (!r.test.ok) onSaved(t("search.savedUntested"), true);
      else onSaved(worked(source.name, r.test) + (!replacing && !first ? t("search.movedFirst") : ""));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  const title = t(replacing ? "search.replaceTitle" : "search.addTitle", { name: source.name });
  return (
    <Dialog label={title} width={420} onClose={onClose}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <div className="ws-sheet-head">
          <Mark source={source} />
          <div>
            <h3>{title}</h3>
            <p>{t(`search.sources.${source.id}.sheet` as "search.sources.exa.sheet", { defaultValue: "" })}</p>
          </div>
        </div>
        <div className="ws-key">
          <div className="ws-key-h">
            <label htmlFor={field}>{t("search.apiKey")}</label>
            {source.key_url && (
              <a className="link" href={source.key_url} target="_blank" rel="noreferrer">
                {t("search.getKey", { name: source.name })}
                <Icon name="ext" />
              </a>
            )}
          </div>
          <input id={field} className="field" type="password" autoComplete="off" spellCheck={false} autoFocus disabled={busy} value={key} onChange={(e) => setKey(e.target.value)} />
        </div>
        {error && (
          <p className="ws-err" role="alert">
            {error}
          </p>
        )}
        <div className="sheet-foot">
          <span className="left ws-lock">
            <Icon name="lock" />
            {t("search.keychain")}
          </span>
          <button type="button" className="btn" disabled={busy} onClick={onClose}>
            {t("search.cancel")}
          </button>
          <button type="submit" className="btn primary" disabled={busy || !key.trim()}>
            {t(busy ? "search.testing" : "search.saveAndTest")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
