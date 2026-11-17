import { useEffect, useMemo, useRef, useState, type MouseEvent as ReactMouseEvent } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { deliveryQuote, fromSource, groupDeliveries, isMediaKind, isNewDelivery, joinDeliveries, onlyDeliveries, readSent, sameSource, sourceCount, sourceGroups } from "@hermesh/her-client";
import type { BotSummary, DeliveredFile, DeliveriesListResult, DeliveriesSourcesResult, Delivery, DeliverySourceCount, DeliverySourceFilter } from "@hermesh/her-protocol";
import { Icon } from "../icons";
import { Markdown } from "../session/Markdown";
import { Dialog } from "../session/markdown/Dialog";
import { herAnswers, needsUpdate } from "../skew";
import { errorText, useNow, useStore } from "../store";
import { BotAvatar } from "./Avatar";
import { useHer } from "./context";
import { replyInComposer, showMessage } from "./Conversation";
import { reachable } from "./environments";
import { momentText, sizeText } from "./format";
import { MenuButton, MenuItem, menuKeys } from "./menus";
import { PdfView } from "./preview/PdfView";
import "./preview/preview.css";

// The files Her sent, in a place of their own (docs/her-deliverables.md §3): beside the conversation, where her
// settings and routines open. Newest first, searched by her, by the day each was last sent; three tabs: all, the
// documents (what is read: reports, sheets, slides, PDF, Markdown, code and the rest) and the media (pictures, video
// and audio, as a grid). A file sent again from the same path is one row; its versions are switched where it opens. A file opens over the window,
// from the copy her kept of it as it was sent, and is kept on this Mac by its hash once read, so it opens while the
// machine is offline too. From a file: Show in Conversation, and Reply, which quotes it so Her goes on with it.
// Where the files came from is not on their rows: the filter button by Close shows one source's, from a menu of the
// routines and sessions that sent any, the latest first (§3.5).

/** Which of the files show. */
type Tab = "all" | "documents" | "media";
const TABS: Tab[] = ["all", "documents", "media"];

/** The extension, as a file's icon shows it. */
const extOf = (name: string) => (name.includes(".") ? name.slice(name.lastIndexOf(".") + 1).toLowerCase() : "");

/** What a file shows as here: its pages, its text as written or as code, a picture, or nothing to preview. */
type Shows = "pdf" | "markdown" | "text" | "image" | "none";
const TEXT = new Set(["txt", "csv", "tsv", "json", "yaml", "yml", "toml", "xml", "log", "diff", "patch", "sh", "py", "js", "ts", "tsx", "jsx", "go", "rs", "swift", "java", "c", "h", "cpp", "css", "sql", "rb", "kt", "html"]);
const IMAGE: Record<string, string> = { png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp" };
function shows(f: DeliveredFile): Shows {
  const ext = extOf(f.name);
  if (ext === "pdf") return "pdf";
  if (ext === "md" || ext === "markdown") return "markdown";
  if (f.kind === "image" || IMAGE[ext]) return "image";
  if (TEXT.has(ext)) return "text";
  return "none";
}

/** A file of a version, read whole: from this Mac's copy when it has one, then the copy her kept, then where it is. */
function readVersion(machineId: string, botId: string, f: DeliveredFile, v: Delivery): Promise<Uint8Array> {
  const app = window.hermesh;
  if (!app) return Promise.reject(new Error("no app"));
  return readSent({ path: f.path, snapshot: v.snapshot ?? undefined }, (p) => app.readHerFile(machineId, botId, p, v.sha256 ?? undefined));
}

/** The panel: `kept` is the first page as this Mac keeps it, shown until her answers and while the machine is offline. */
export function DeliveriesPanel({ bot, kept, onBack, onClose }: { bot: BotSummary; kept: DeliveriesListResult | null; onBack: () => void; onClose: () => void }) {
  const { t } = useTranslation("her");
  const { t: tc } = useTranslation("common");
  const her = useHer();
  const st = useStore();
  const now = useNow(60_000);
  const machine = her.machine;
  const online = reachable(machine) && !!her.env.rpc;
  const older = herAnswers(machine, "deliveries/list") === false;
  const [query, setQuery] = useState("");
  const [typed, setTyped] = useState("");
  const [tab, setTab] = useState<Tab>("all");
  const [source, setSource] = useState<DeliverySourceFilter | null>(null);
  const [sources, setSources] = useState<DeliveriesSourcesResult | null>(null);
  const [list, setList] = useState<DeliveriesListResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [lacks, setLacks] = useState(false);
  const [more, setMore] = useState(false);
  const [shown, setShown] = useState<{ file: DeliveredFile; version: number } | null>(null);
  const [menu, setMenu] = useState<{ file: DeliveredFile; x: number; y: number } | null>(null);
  const [onMac, setOnMac] = useState<Set<string>>(new Set());

  useEffect(() => {
    const wait = setTimeout(() => setQuery(typed.trim()), 200);
    return () => clearTimeout(wait);
  }, [typed]);
  // The first page, asked for again when her says the list changed (the row's latest send) or the search does.
  const latest = bot.deliveries_latest ?? 0;
  const media = tab === "all" ? undefined : tab === "media";
  const filters = { ...(query ? { query } : {}), ...(media !== undefined ? { media } : {}), ...(source ? { source } : {}) };
  useEffect(() => {
    if (older || !online) return;
    let live = true;
    her.call("deliveries/list", { bot_id: bot.id, ...filters }).then(
      (r) => live && (setList(onlyDeliveries(r, media)), setError(null), setLacks(false)),
      (e) => live && (setError(errorText(e)), setLacks(needsUpdate(e))),
    );
    return () => {
      live = false;
    };
  }, [bot.id, latest, query, tab, source, online, older]);
  // Where the files came from, for the filter: none from a her that cannot say, and then no filter.
  useEffect(() => setSource(null), [bot.id]);
  useEffect(() => {
    if (older || !online) return;
    let live = true;
    her.call("deliveries/sources", { bot_id: bot.id }).then(
      (r) => live && setSources(r),
      () => live && setSources(null),
    );
    return () => {
      live = false;
    };
  }, [bot.id, latest, online, older]);

  // Offline, or until her answers: the page this Mac keeps, searched and filtered here.
  const shownList = useMemo<DeliveriesListResult | null>(() => {
    if (list) return list;
    if (!kept) return null;
    const q = query.toLowerCase();
    const files = kept.files.filter(
      (f) => (tab === "all" || isMediaKind(f.kind) === (tab === "media")) && (!source || fromSource(f, source)) && (!q || [f.name, ...f.versions.flatMap((v) => [v.text ?? "", v.source?.name ?? ""])].some((s) => s.toLowerCase().includes(q))),
    );
    return { files, seen: kept.seen };
  }, [list, kept, query, tab, source]);

  // Which files this Mac has, for while the machine is offline.
  const shaList = (shownList?.files ?? []).flatMap((f) => f.versions.flatMap((v) => (v.sha256 ? [[v.sha256, f.name] as [string, string]] : [])));
  const shaKey = shaList.map(([s]) => s).join(",");
  useEffect(() => {
    if (online || shaList.length === 0) return;
    let live = true;
    window.hermesh?.herFilesKept(machine.machineId, shaList).then((have) => live && setOnMac(new Set(have)), () => {});
    return () => {
      live = false;
    };
  }, [online, shaKey, machine.machineId]);
  const canOpen = (f: DeliveredFile, v: Delivery) => online || (!!v.sha256 && onMac.has(v.sha256));

  // Closing the list is having seen it: up to the latest send it showed.
  const sawUpTo = useRef(0);
  sawUpTo.current = Math.max(sawUpTo.current, ...(shownList?.files ?? []).map((f) => f.versions[0]?.seq ?? 0));
  const seenBefore = useRef<number | null>(null);
  if (seenBefore.current === null && shownList) seenBefore.current = shownList.seen;
  const call = useRef(her.call);
  call.current = her.call;
  useEffect(
    () => () => {
      if (sawUpTo.current > (seenBefore.current ?? 0)) void call.current("deliveries/seen", { bot_id: bot.id, seq: sawUpTo.current }).catch(() => {});
    },
    [bot.id],
  );

  const loadMore = () => {
    if (!list?.next || more) return;
    setMore(true);
    her
      .call("deliveries/list", { bot_id: bot.id, before: list.next, ...filters })
      .then((r) => onlyDeliveries(r, media))
      .then((r) => setList((l) => (l ? joinDeliveries([l, r]) : r)), (e) => st.show(errorText(e)))
      .finally(() => setMore(false));
  };

  const act = {
    open: (f: DeliveredFile, version = 0) => {
      const v = f.versions[version];
      if (!v) return;
      if (!canOpen(f, v)) return st.show(t("files.needsMachine", { name: machine.machineName }));
      setShown({ file: f, version });
    },
    show: (f: DeliveredFile, version = 0) => {
      const v = f.versions[version];
      if (!v) return;
      if (bot.session_id && v.session_id !== bot.session_id) return st.show(t("files.earlierConversation"));
      setShown(null);
      showMessage(machine.machineId, v.call_id, v.session_id);
    },
    reply: (f: DeliveredFile, version = 0) => {
      const v = f.versions[version];
      if (!v) return;
      setShown(null);
      replyInComposer(machine.machineId, deliveryQuote(f, v));
    },
    download: (f: DeliveredFile, version = 0) => {
      const v = f.versions[version];
      const app = window.hermesh;
      if (!v || !app) return;
      if (!canOpen(f, v)) return st.show(t("files.needsMachine", { name: machine.machineName }));
      readSent({ path: f.path, snapshot: v.snapshot ?? undefined }, (p) => app.saveHerFile(machine.machineId, bot.id, p, v.sha256 ?? undefined)).then(
        () => st.show(t("files.saved", { name: f.name })),
        (e) => st.show(errorText(e)),
      );
    },
  };

  const groups = shownList ? groupDeliveries(shownList.files, now.getTime(), -now.getTimezoneOffset()) : [];
  const searching = query !== "" || tab !== "all" || !!source;
  const chosen = source && sources?.sources.find((s) => sameSource(s.filter, source));
  return (
    <aside className="her-panel rtn dlv" aria-label={t("files.title")}>
      <div className="her-panel-top drag rtn-top rtn-sub">
        <button className="tb" title={t("routines.back")} aria-label={t("routines.back")} onClick={onBack}>
          <Icon name="chevL" />
        </button>
        <span className="rtn-tt">{t("files.title")}</span>
        {sources && !older && (
          <MenuButton
            className={`tb dlv-src ${source ? "on" : ""}`}
            title={t("files.sources.button")}
            label={<Icon name="filter" />}
            align="right"
            below
            disabled={!online}
          >
            {(close) => <SourceMenu sources={sources} source={source} media={media} onPick={(s) => (close(), setSource(s))} />}
          </MenuButton>
        )}
        <button className="tb" title={t("panel.close")} aria-label={t("panel.close")} onClick={onClose}>
          <Icon name="x" />
        </button>
      </div>
      {older || (lacks && !shownList) ? (
        <div className="her-panel-body">
          <div className="rtn-empty" data-testid="needs-update">
            <b>{tc("needsUpdate.title")}</b>
            {tc("needsUpdate.body", { machine: machine.machineName })}
          </div>
        </div>
      ) : (
        <>
          <div className="dlv-tools">
            <label className="dlv-search">
              <Icon name="search" className="sm" />
              <input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={t("files.search")} aria-label={t("files.search")} />
            </label>
            <div className="dlv-tabs" role="radiogroup" aria-label={t("files.title")}>
              {TABS.map((k) => (
                <button key={k} role="radio" aria-checked={tab === k} className={tab === k ? "on" : ""} onClick={() => setTab(k)}>
                  {t(`files.tabs.${k}`)}
                </button>
              ))}
            </div>
            {source && (
              <div className="dlv-source" data-testid="files-source">
                <Icon name={sourceIcon(source)} className="sm" />
                <span>
                  {sourceName(source, chosen, t("files.sources.conversation"))}
                  {chosen && <small> · {t("files.sources.count", { count: sourceCount(chosen, media) })}</small>}
                </span>
                <button title={t("files.sources.clear")} aria-label={t("files.sources.clear")} onClick={() => setSource(null)}>
                  <Icon name="x" className="sm" />
                </button>
              </div>
            )}
          </div>
          <div className="her-panel-body dlv-body">
            {!online && shownList && <div className="dlv-banner" role="status">{t("files.offline", { name: machine.machineName })}</div>}
            {error && !shownList && <p className="set-note">{t("files.failed", { error })}</p>}
            {!shownList && !error && (
              <div className="rtn-empty">
                <span className="mini-spin" />
              </div>
            )}
            {shownList && shownList.files.length === 0 && (
              <div className="rtn-empty" data-testid="files-empty">
                <b>{t(searching ? "files.noneTitle" : "files.emptyTitle")}</b>
                {t(searching ? "files.noneBody" : "files.emptyBody")}
              </div>
            )}
            {groups.map((g) => (
              <section key={g.key} className="dlv-group">
                <h3 className="fhead">
                  {t(`files.day.${g.key}`)}
                  <small>{g.files.length}</small>
                </h3>
                {tab === "media" ? (
                  <div className="dlv-grid">
                    {g.files.map((f) => (
                      <Thumb key={f.path} file={f} fresh={isNewDelivery(f, shownList!.seen)} enabled={canOpen(f, f.versions[0])} onOpen={() => act.open(f)} />
                    ))}
                  </div>
                ) : (
                  g.files.map((f) => (
                    <Row
                      key={f.path}
                      file={f}
                      bot={bot}
                      now={now}
                      fresh={isNewDelivery(f, shownList!.seen)}
                      enabled={canOpen(f, f.versions[0])}
                      act={act}
                      onMenu={(e) => {
                        e.preventDefault();
                        setMenu({ file: f, x: e.clientX, y: e.clientY });
                      }}
                    />
                  ))
                )}
              </section>
            ))}
            {list?.next !== undefined && (
              <button className="btn dlv-more" disabled={more} onClick={loadMore}>
                {t("files.more")}
              </button>
            )}
            {shownList && shownList.files.length > 0 && <p className="set-note">{t("files.note", { machine: machine.machineName })}</p>}
          </div>
        </>
      )}
      {menu && <RowMenu at={menu} onClose={() => setMenu(null)} act={act} />}
      {shown && (
        <DeliveryPreview
          bot={bot}
          file={shown.file}
          version={shown.version}
          now={now}
          canOpen={canOpen}
          onVersion={(version) => setShown({ file: shown.file, version })}
          act={act}
          onClose={() => setShown(null)}
        />
      )}
    </aside>
  );
}

/** A source's icon: a routine's clock, a session's code, the conversation's bubble. */
const sourceIcon = (f: DeliverySourceFilter) => (f.kind === "routine" ? "clock" : f.kind === "session" ? "code" : "bubble");

/** What a source is called: the conversation by what it is (`conversation`), the rest by their names as her says them now. */
function sourceName(f: DeliverySourceFilter, s: DeliverySourceCount | null | undefined, conversation: string): string {
  if (f.kind === "conversation") return conversation;
  return s?.name ?? (f.kind === "session" ? f.name : f.id);
}

/** With more sources than this, the menu has a search. */
const MANY_SOURCES = 8;

/**
 * The filter's menu: all sources; then the conversation, the routines and the sessions that sent files, the latest
 * first, each with how many files the tab shows; one with none in the tab can't be chosen. A deleted routine stays
 * (its files do); the routines that sent no file are a line at the end.
 */
function SourceMenu({ sources, source, media, onPick }: { sources: DeliveriesSourcesResult; source: DeliverySourceFilter | null; media: boolean | undefined; onPick: (s: DeliverySourceFilter | null) => void }) {
  const { t } = useTranslation("her");
  const [query, setQuery] = useState("");
  const find = useRef<HTMLInputElement>(null);
  const box = useRef<HTMLDivElement>(null);
  const many = sources.sources.length > MANY_SOURCES;
  const groups = sourceGroups(sources.sources, query);
  const total = sources.sources.reduce((n, s) => n + sourceCount(s, media), 0);
  useEffect(() => {
    const menu = box.current?.closest<HTMLElement>("[role=menu]");
    if (many && menu && document.activeElement === menu) find.current?.focus();
  }, [many]);
  const item = (s: DeliverySourceCount) => {
    const n = sourceCount(s, media);
    return (
      <MenuItem
        key={JSON.stringify(s.filter)}
        checked={sameSource(s.filter, source)}
        label={sourceName(s.filter, s, t("files.sources.conversation"))}
        desc={s.gone ? t("files.sources.gone") : undefined}
        value={String(n)}
        disabled={n === 0}
        onClick={() => onPick(s.filter)}
      />
    );
  };
  const none = !groups.conversation && groups.routines.length === 0 && groups.sessions.length === 0;
  return (
    <div className="mmodels dlv-srcmenu" ref={box}>
      {many && (
        <label className="mfind">
          <Icon name="search" className="sm" />
          <input
            ref={find}
            value={query}
            placeholder={t("files.sources.find")}
            aria-label={t("files.sources.find")}
            spellCheck={false}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown" || e.key === "Escape" || e.key === "Tab") return;
              e.stopPropagation();
            }}
          />
        </label>
      )}
      <div className="mlist">
        {!query.trim() && (
          <>
            <MenuItem checked={!source} label={t("files.sources.all")} value={String(total)} onClick={() => onPick(null)} />
            <div className="msep" role="separator" />
          </>
        )}
        {groups.conversation && item(groups.conversation)}
        {groups.routines.length > 0 && (
          <div role="group" aria-label={t("files.sources.routines")}>
            <div className="mgh">{t("files.sources.routines")}</div>
            {groups.routines.map(item)}
          </div>
        )}
        {groups.sessions.length > 0 && (
          <div role="group" aria-label={t("files.sources.sessions")}>
            <div className="mgh">{t("files.sources.sessions")}</div>
            {groups.sessions.map(item)}
          </div>
        )}
        {none && <div className="mempty">{t("files.sources.noMatch", { query: query.trim() })}</div>}
        {!query.trim() && sources.silent > 0 && <div className="mempty dlv-silent">{t("files.sources.silent", { count: sources.silent })}</div>}
      </div>
    </div>
  );
}

type Acts = Record<"open" | "show" | "reply" | "download", (f: DeliveredFile, version?: number) => void>;

function RowMenu({ at, act, onClose }: { at: { file: DeliveredFile; x: number; y: number }; act: Acts; onClose: () => void }) {
  const { t } = useTranslation("her");
  const menu = useRef<HTMLDivElement>(null);
  useEffect(() => {
    menu.current?.focus();
    const outside = (e: MouseEvent) => !menu.current?.contains(e.target as Node) && onClose();
    document.addEventListener("mousedown", outside);
    window.addEventListener("blur", onClose);
    return () => {
      document.removeEventListener("mousedown", outside);
      window.removeEventListener("blur", onClose);
    };
  }, []);
  const f = at.file;
  const item = (icon: string, label: string, run: () => void) => <MenuItem icon={icon} label={label} onClick={() => (onClose(), run())} />;
  return createPortal(
    <div className="vmenu her-menu down" role="menu" aria-label={f.name} tabIndex={-1} ref={menu} style={{ left: Math.min(at.x, innerWidth - 220), top: Math.min(at.y, innerHeight - 170) }} onKeyDown={(e) => menuKeys(e, menu.current, onClose)}>
      {item("eye", t("files.open"), () => act.open(f))}
      {item("bubble", t("files.show"), () => act.show(f))}
      {item("reply", t("files.reply"), () => act.reply(f))}
      {item("arrowD", t("files.download"), () => act.download(f))}
    </div>,
    document.body,
  );
}

/** A file's icon: its extension on its kind's colour. */
function FileIcon({ file }: { file: DeliveredFile }) {
  const ext = extOf(file.name);
  const tone = ext === "pdf" ? "pdf" : ext === "md" || ext === "markdown" ? "md" : file.kind;
  return (
    <span className={`dlv-fi k-${tone}`} aria-hidden>
      {ext.slice(0, 4).toUpperCase()}
    </span>
  );
}

/** When a file was last sent, as its row says it: the time under Today and Yesterday, the day before that. */
function whenText(at: Date, now: Date, language: string): string {
  const day = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const ago = Math.round((day(now) - day(at)) / 86_400_000);
  return ago <= 1 ? at.toLocaleTimeString(language, { hour: "2-digit", minute: "2-digit" }) : at.toLocaleDateString(language, { month: "short", day: "numeric" });
}

/**
 * A row, two lines: the name, with the new dot and when it was last sent; then Her's words with it (its size when
 * she said nothing), and how many versions there are. Its size, where it came from and its versions one by one are
 * where it opens. Hovering shows its actions over the time.
 */
function Row({ file, bot, now, fresh, enabled, act, onMenu }: {
  file: DeliveredFile;
  bot: BotSummary;
  now: Date;
  fresh: boolean;
  enabled: boolean;
  act: Acts;
  onMenu: (e: ReactMouseEvent) => void;
}) {
  const { t, i18n } = useTranslation("her");
  const v = file.versions[0];
  const earlier = !!bot.session_id && v.session_id !== bot.session_id;
  const stem = file.name.includes(".") ? file.name.slice(0, file.name.lastIndexOf(".")) : file.name;
  return (
    <div className={`dlv-row ${enabled ? "" : "off"}`} data-testid="her-file-row" data-name={file.name} onContextMenu={onMenu}>
      <button className="dlv-open" aria-label={`${t("files.open")} ${file.name}`} onClick={() => act.open(file)}>
        <FileIcon file={file} />
        <span className="dlv-k">
          <span className="dlv-l1">
            <span className="dlv-n">
              <span className="dlv-stem">{stem}</span>
              {stem !== file.name && <em>{file.name.slice(stem.length)}</em>}
              {fresh && <i className="dlv-dot" aria-label={t("files.new")} />}
            </span>
            <span className="dlv-when">{whenText(new Date(v.sent_at), now, i18n.language)}</span>
          </span>
          <span className="dlv-l2">
            <span className="dlv-say">{v.text ?? sizeText(v.size)}</span>
            {file.versions.length > 1 && <span className="dlv-vers">{t("files.versions", { count: file.versions.length })}</span>}
          </span>
        </span>
      </button>
      <span className="dlv-acts">
        {!earlier && (
          <button className="tb" title={t("files.show")} aria-label={t("files.show")} onClick={() => act.show(file)}>
            <Icon name="bubble" className="sm" />
          </button>
        )}
        <button className="tb" title={t("files.reply")} aria-label={t("files.reply")} onClick={() => act.reply(file)}>
          <Icon name="reply" className="sm" />
        </button>
        <button className="tb" title={t("files.download")} aria-label={t("files.download")} onClick={() => act.download(file)}>
          <Icon name="arrowD" className="sm" />
        </button>
      </span>
    </div>
  );
}

/** A picture's bytes as a URL, read once it is asked for; null while reading or when it can't be. */
function usePicture(file: DeliveredFile, v: Delivery | undefined, enabled: boolean): string | null {
  const her = useHer();
  const bot = her.bot?.id ?? "";
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    if (!v || !enabled || !bot) return;
    let live = true;
    let made: string | null = null;
    readVersion(her.machine.machineId, bot, file, v).then(
      (bytes) => {
        if (!live) return;
        made = URL.createObjectURL(new Blob([bytes as BlobPart], { type: IMAGE[extOf(file.name)] ?? "image/png" }));
        setUrl(made);
      },
      () => {},
    );
    return () => {
      live = false;
      if (made) URL.revokeObjectURL(made);
    };
  }, [file.path, v?.seq, enabled, bot]);
  return url;
}

function Thumb({ file, fresh, enabled, onOpen }: { file: DeliveredFile; fresh: boolean; enabled: boolean; onOpen: () => void }) {
  const url = usePicture(file, file.versions[0], enabled && file.kind === "image");
  return (
    <button className={`dlv-thumb ${enabled ? "" : "off"}`} title={file.name} aria-label={file.name} data-testid="her-file-thumb" onClick={onOpen}>
      {url ? <img src={url} alt="" draggable={false} /> : <FileIcon file={file} />}
      {fresh && <i className="dlv-dot" />}
    </button>
  );
}

/** A version of a file over the window: what it shows as, the switch between versions, and what Her said with it. */
function DeliveryPreview({ bot, file, version, now, canOpen, onVersion, act, onClose }: {
  bot: BotSummary;
  file: DeliveredFile;
  version: number;
  now: Date;
  canOpen: (f: DeliveredFile, v: Delivery) => boolean;
  onVersion: (i: number) => void;
  act: Acts;
  onClose: () => void;
}) {
  const { t } = useTranslation("her");
  const her = useHer();
  const v = file.versions[version];
  const n = file.versions.length;
  const kind = shows(file);
  const [read, setRead] = useState<{ seq: number; bytes: Uint8Array } | { seq: number; error: string } | null>(null);
  useEffect(() => {
    if (kind === "none" || !v) return;
    let live = true;
    setRead(null);
    readVersion(her.machine.machineId, bot.id, file, v).then(
      (bytes) => live && setRead({ seq: v.seq, bytes }),
      (e) => live && setRead({ seq: v.seq, error: errorText(e) }),
    );
    return () => {
      live = false;
    };
  }, [file.path, v?.seq, kind]);
  const picture = useMemo(() => (read && "bytes" in read && kind === "image" ? URL.createObjectURL(new Blob([read.bytes as BlobPart], { type: IMAGE[extOf(file.name)] ?? "image/png" })) : null), [read, kind]);
  useEffect(() => () => void (picture && URL.revokeObjectURL(picture)), [picture]);
  if (!v) return null;
  const earlier = !!bot.session_id && v.session_id !== bot.session_id;
  const body = (() => {
    if (kind === "none") return <div className="fpv-msg">{t("files.noPreview")}</div>;
    if (!read || read.seq !== v.seq) return <div className="fpv-msg"><span className="mini-spin" /></div>;
    if ("error" in read) return <div className="fpv-msg">{t("files.failed", { error: read.error })}</div>;
    if (kind === "pdf") return <PdfView key={v.seq} bytes={read.bytes} name={file.name} />;
    if (kind === "image") return picture ? <div className="dlv-pic"><img src={picture} alt={file.name} /></div> : null;
    const text = new TextDecoder().decode(read.bytes);
    return (
      <div className="fpv-read">
        <div className="fpv-col">{kind === "markdown" ? <Markdown text={text} mode="document" /> : <pre className="fpv-text sel-text">{text}</pre>}</div>
      </div>
    );
  })();
  return createPortal(
    <Dialog label={file.name} width="min(var(--md-rich-width), calc(100vw - 96px))" className="fpv dlv-pv" onClose={onClose}>
      <div className="fpv-in" data-testid="her-file-preview" onKeyDown={(e) => e.key !== "Escape" && e.stopPropagation()}>
        <div className="fpv-head">
          <span className="fpv-name" title={file.name}>
            {file.name}
            <small>
              {sizeText(v.size)}
              {n > 1 && ` · ${t("files.version", { n: n - version })}${version === 0 ? t("files.latestMark") : ""}`}
            </small>
          </span>
          <button className="tb" aria-label={t("files.download")} title={t("files.download")} onClick={() => act.download(file, version)}>
            <Icon name="arrowD" />
          </button>
          <button className="tb" aria-label={t("preview.close")} title={t("preview.close")} onClick={onClose}>
            <Icon name="x" />
          </button>
        </div>
        {n > 1 && (
          <div className="dlv-switch" role="radiogroup" aria-label={t("files.versions", { count: n })}>
            {file.versions.map((x, i) => (
              <button key={x.seq} role="radio" aria-checked={i === version} className={i === version ? "on" : ""} disabled={!canOpen(file, x)} onClick={() => onVersion(i)}>
                {t("files.version", { n: n - i })}
              </button>
            ))}
          </div>
        )}
        <div className="fpv-body" tabIndex={-1} ref={(el) => el?.setAttribute("autofocus", "")}>
          {body}
        </div>
        <div className="dlv-foot">
          <BotAvatar bot={bot} size={28} />
          <span className="dlv-said">
            <b>
              {bot.name} · {momentText(new Date(v.sent_at), now)}
            </b>
            {v.text && <span>{v.text}</span>}
          </span>
          {!earlier && (
            <button className="btn" onClick={() => act.show(file, version)}>
              <Icon name="bubble" className="sm" />
              {t("files.show")}
            </button>
          )}
          <button className="btn primary" onClick={() => act.reply(file, version)}>
            <Icon name="reply" className="sm" />
            {t("files.replyShort")}
          </button>
        </div>
      </div>
    </Dialog>,
    document.body,
  );
}

