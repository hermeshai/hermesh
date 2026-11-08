import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { RpcClient } from "@hermesh/her-client";
import type { BrowserStatus, ExtensionProblem, ExtensionStatus } from "@hermesh/her-protocol";
import { browserName, extensionEdits, yours, yoursConfig, type YoursConfig } from "./yours";
import "./yours.css";

// The console's Browser section, its first part: whether the owner's own Chrome, on the Mac Her runs on, is connected
// through the Hermesh extension and used by Her, with the one thing to do about it: connect it
// (a guide: add the extension, then wait for it to connect, then use it), use it, stop using it,
// or pick which of several to use. Links open with window.open: the console may be on another
// device than the Mac. The site permissions are the section's other part.
//
// Mount it on the Browser page, above the site permissions, with the page's connection:
//   <YourBrowser rpc={useHer().rpc} />
// and give i18n.ts its strings as the namespace "yours" (locales/en/yours.json, locales/zh-CN/yours.json).

/** The extension's page in the Chrome Web Store, once it is published (her/apps/chrome-extension/store/listing.md); until then the guide loads it from its folder. */
export const STORE_URL = "";

/** The folder to load the extension from until it is in the store. */
const EXTENSION_FOLDER = "her/apps/chrome-extension";

/** How long a refused host gets to exit and come back as the current program. */
const REFUSED_STUCK_MS = 10_000;

/** How long the extension gets to connect before the guide says what to check. */
const WAIT_SECONDS = 5;

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e));

export function YourBrowser({ rpc }: { rpc: RpcClient | null }) {
  const { t } = useTranslation("yours");
  const [status, setStatus] = useState<BrowserStatus | null>(null);
  const [config, setConfig] = useState<YoursConfig | null>(null);
  const [guide, setGuide] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  // her's word on the browsers (browser/status, then browser/changed) and `[browser]`
  // (config/read, then config/changed), on the page's connection.
  const readConfig = useCallback(async () => {
    if (rpc) setConfig(yoursConfig((await rpc.request("config/read", {})).config));
  }, [rpc]);
  useEffect(() => {
    if (!rpc) return;
    let live = true;
    const offStatus = rpc.on("browser/changed", (s) => live && setStatus(s));
    rpc.request("browser/status", {}).then((s) => live && setStatus(s), () => {});
    readConfig().catch(() => {});
    const offConfig = rpc.on("config/changed", () => void readConfig().catch(() => {}));
    return () => {
      live = false;
      offStatus();
      offConfig();
    };
  }, [rpc, readConfig]);

  /** Her uses the owner's Chrome, or stops: every conversation from its next step. */
  const legacy = config?.legacyBackend ?? false;
  const use = useCallback(
    async (on: boolean) => {
      if (!rpc) return;
      await rpc.request("config/write", { layer: "user", edits: extensionEdits(on, legacy) });
      await readConfig();
    },
    [rpc, legacy, readConfig],
  );

  const y = yours(status, config);
  // A host that exits on refusal is gone in moments. One that stays runs code from before it could.
  const [stuck, setStuck] = useState(false);
  useEffect(() => {
    setStuck(false);
    if (!y.refused) return;
    const timer = setTimeout(() => setStuck(true), REFUSED_STUCK_MS);
    return () => clearTimeout(timer);
  }, [!!y.refused]);

  const act = async (work: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await work();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const { state } = y;
  const name = browserName(y.connected?.browser);
  const desc =
    state === "lost" ? (y.refused ? (stuck ? "refusedStuck" : "refused") : "lost")
    : state === "outdated" ? (y.problem === "update_her" ? "outdated.her" : "outdated.extension")
    : state;
  const badge = { on: "ok", ready: "acc", lost: "warn", outdated: "warn" } as const;

  return (
    <section className="yours">
      <h3 className="fhead">{t("title")}</h3>
      <div className="fgroup">
        <div className="frow">
          <div className="ft">
            <div className="fn yours-name">
              <span>{state === "on" || state === "ready" ? name : t("connectTitle")}</span>
              {state !== "off" && <span className={`badge ${badge[state]}`}>{t(`badge.${state}`)}</span>}
            </div>
            <div className={`fdesc ${state === "lost" || state === "outdated" ? "warn" : ""}`}>{t(desc, { name: browserName(status?.extension?.browser) })}</div>
          </div>
          {state === "off" && (
            <button className="btn primary" disabled={!rpc} onClick={() => setGuide(true)}>
              {t("connect")}
            </button>
          )}
          {state === "ready" && (
            <button className="btn primary" disabled={busy} onClick={() => void act(() => use(true))}>
              {t("use")}
            </button>
          )}
          {state === "on" && (
            <button className="btn" disabled={busy} onClick={() => void act(() => use(false))}>
              {t("disconnect")}
            </button>
          )}
          {state === "lost" && (
            <button className="btn" onClick={() => setGuide(true)}>
              {t("reconnect")}
            </button>
          )}
        </div>
      </div>
      {/* It lists one only when that one is not in use: the browser chosen is away. */}
      {y.several.length > 0 && (
        <div className="fgroup yours-several">
          {y.several.map((e) => (
            <div className="frow" key={e.instance ?? e.browser ?? ""}>
              <div className="ft">
                <div className="fn">
                  {browserName(e.browser)}
                  {e.account && <span className="yours-account"> · {e.account}</span>}
                </div>
              </div>
              {e.in_use ? (
                // The one Her would use: in use only while Her uses the owner's Chrome.
                config?.extension ? <span className="badge ok">{t("badge.on")}</span> : <span className="badge">{t("badge.chosen")}</span>
              ) : (
                <button className="btn" disabled={!rpc || !e.instance || busy} onClick={() => void act(() => rpc!.request("browser/use_extension", { instance: e.instance! }))}>
                  {t("useThis")}
                </button>
              )}
            </div>
          ))}
        </div>
      )}
      {error && <p className="set-note warn">{error}</p>}
      {guide && <Guide connected={y.connected} problem={y.problem} onConnected={() => use(true)} onClose={() => setGuide(false)} />}
    </section>
  );
}

type Step = "install" | "wait" | "done";

/** Connecting, one step at a time: adding the extension is the owner's; the rest happens by itself. */
function Guide({ connected, problem, onConnected, onClose }: { connected: ExtensionStatus | null; problem: ExtensionProblem | null; onConnected: () => Promise<unknown>; onClose: () => void }) {
  const { t } = useTranslation("yours");
  const [step, setStep] = useState<Step>("install");
  const [error, setError] = useState("");
  // Seconds left before saying what to check, counted down on screen so the wait has an end.
  const [left, setLeft] = useState(WAIT_SECONDS);
  const slow = left === 0;
  const connect = useRef(onConnected);
  connect.current = onConnected;

  // Waiting: her says when the extension connects; then Her uses it. Choosing to connect is the consent.
  useEffect(() => {
    if (step !== "wait" || !connected) return;
    let live = true;
    connect.current().then(
      () => live && setStep("done"),
      (e) => live && setError(errorText(e)),
    );
    return () => {
      live = false;
    };
  }, [step, !!connected]);
  useEffect(() => {
    setLeft(WAIT_SECONDS);
    if (step !== "wait" || connected) return;
    const timer = setInterval(() => setLeft((n) => Math.max(0, n - 1)), 1000);
    return () => clearInterval(timer);
  }, [step, !!connected]);

  const order = ["install", "wait"] as const;
  const index = step === "done" ? order.length : order.indexOf(step);
  const name = browserName(connected?.browser);

  return (
    <Sheet label={t("guide.title")} onClose={onClose}>
      <div className="yours-guide-top">
        <h3>{t("guide.title")}</h3>
        <button className="tb" aria-label={t("guide.close")} title={t("guide.close")} onClick={onClose}>
          <Glyph d="M4.3 4.3l7.4 7.4M11.7 4.3l-7.4 7.4" />
        </button>
      </div>
      <ol className="yours-steps" aria-label={t("guide.title")}>
        {order.map((s, i) => (
          <li key={s} className={i < index ? "done" : i === index ? "now" : ""}>
            <span className="yours-num">{i < index ? <Glyph d="M3.4 8.4l3 3 6.2-6.8" /> : i + 1}</span>
            {t(`guide.steps.${s}`)}
          </li>
        ))}
      </ol>

      {step === "install" &&
        (STORE_URL ? (
          <>
            <p className="sub">{t("guide.store")}</p>
            <div className="sheet-foot">
              <button className="btn primary" onClick={() => (window.open(STORE_URL, "_blank", "noopener"), setStep("wait"))}>
                {t("guide.addToChrome")}
              </button>
            </div>
          </>
        ) : (
          <>
            <p className="sub">{t("guide.install")}</p>
            <ol className="yours-howto">
              <li>
                {t("guide.howto.open")} <code className="sel-text">chrome://extensions</code>
              </li>
              <li>{t("guide.howto.developer")}</li>
              <li>
                {t("guide.howto.load")} <code className="sel-text">{EXTENSION_FOLDER}</code>
              </li>
            </ol>
            <p className="set-note">{t("guide.already")}</p>
            <div className="sheet-foot">
              <button className="btn primary" onClick={() => setStep("wait")}>
                {t("guide.added")}
              </button>
            </div>
          </>
        ))}

      {step === "wait" && (
        <>
          {error ? (
            <p className="set-note warn">{error}</p>
          ) : problem ? (
            <p className="set-note warn">{t(problem === "update_her" ? "outdated.her" : "outdated.extension", { name })}</p>
          ) : (
            <p className="sub yours-waiting">
              <span className="mini-spin" />
              {t("guide.waiting")}
              {!slow && <span className="yours-count">{t("guide.countdown", { count: left })}</span>}
            </p>
          )}
          {!error && !problem && slow && <p className="set-note">{t("guide.waitHint")}</p>}
          <div className="sheet-foot">
            <button className="btn" onClick={() => setStep("install")}>
              {t("guide.back")}
            </button>
          </div>
        </>
      )}

      {step === "done" && (
        <>
          <p className="sub yours-done">
            <Glyph d="M3.4 8.4l3 3 6.2-6.8" />
            {t("guide.done", { name })}
          </p>
          <p className="set-note">{t("guide.doneHint")}</p>
          <div className="sheet-foot">
            <button className="btn primary" onClick={onClose}>
              {t("guide.finish")}
            </button>
          </div>
        </>
      )}
    </Sheet>
  );
}

/** A sheet over the page, as the console's own: a native dialog that Esc and a click beside it close. */
function Sheet({ label, onClose, children }: { label: string; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    if (ref.current && !ref.current.open) ref.current.showModal();
  }, []);
  return (
    <dialog
      ref={ref}
      className="sheet yours-guide"
      aria-label={label}
      onCancel={(e) => (e.preventDefault(), onClose())}
      onClick={(e) => e.target === e.currentTarget && onClose()}
    >
      <div className="sheet-in">{children}</div>
    </dialog>
  );
}

/** A line icon on the console's 16 grid. */
function Glyph({ d }: { d: string }) {
  return (
    <svg className="ic" viewBox="0 0 16 16" aria-hidden="true">
      <path d={d} />
    </svg>
  );
}
