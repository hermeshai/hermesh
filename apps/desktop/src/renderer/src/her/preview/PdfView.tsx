import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../icons";
import { loadPdf, outlineOf, pdfjs, viewerLib, type OutlineItem } from "./pdf";

// A PDF over the window: the bar of its own tools — the page, zoom, search, outline and pages —
// over pdf.js's viewer. Text can be selected and copied; nothing of it goes to the conversation.

type PdfViewer = InstanceType<Awaited<ReturnType<typeof viewerLib>>["PDFViewer"]>;
type EventBus = InstanceType<Awaited<ReturnType<typeof viewerLib>>["EventBus"]>;

export function PdfView({ bytes, name }: { bytes: Uint8Array; name: string }) {
  const { t } = useTranslation("her");
  const scroller = useRef<HTMLDivElement>(null);
  const pagesEl = useRef<HTMLDivElement>(null);
  const viewer = useRef<PdfViewer | null>(null);
  const bus = useRef<EventBus | null>(null);
  const [pdf, setPdf] = useState<pdfjs.PDFDocumentProxy | null>(null);
  const [outline, setOutline] = useState<OutlineItem[]>([]);
  const [error, setError] = useState<string | null>(null);
  // Asked for when pdf.js needs one; given to pdf.js only, kept nowhere.
  const [password, setPassword] = useState<string | undefined>(undefined);
  const [asking, setAsking] = useState<{ wrong: boolean } | null>(null);
  const [page, setPageNo] = useState(1);
  const [pageDraft, setPageDraft] = useState<string | null>(null);
  const [scale, setScale] = useState(1);
  const [query, setQuery] = useState<string | null>(null);
  const [matches, setMatches] = useState<{ current: number; total: number } | null>(null);
  const [side, setSide] = useState<"outline" | "pages" | null>(null);
  const searchBox = useRef<HTMLInputElement>(null);

  // The viewer: pdf.js lays out every page, renders those in sight, and keeps a text layer to
  // select and search in. Links go out only as https (the window opens nothing else).
  useEffect(() => {
    let live = true;
    let doc: pdfjs.PDFDocumentProxy | null = null;
    void (async () => {
      try {
        const lib = await viewerLib();
        const eventBus = new lib.EventBus();
        const linkService = new lib.PDFLinkService({ eventBus, externalLinkTarget: lib.LinkTarget.BLANK, externalLinkRel: "noopener noreferrer" });
        const findController = new lib.PDFFindController({ eventBus, linkService });
        const v = new lib.PDFViewer({
          container: scroller.current!,
          viewer: pagesEl.current!,
          eventBus,
          linkService,
          findController,
          textLayerMode: 1,
          annotationMode: pdfjs.AnnotationMode.ENABLE_FORMS,
          removePageBorders: true,
        });
        linkService.setViewer(v);
        doc = await loadPdf(bytes, password);
        if (!live) return void doc.destroy();
        eventBus.on("pagesinit", () => {
          v.currentScaleValue = "page-width";
        });
        eventBus.on("pagechanging", (e: { pageNumber: number }) => setPageNo(e.pageNumber));
        eventBus.on("scalechanging", (e: { scale: number }) => setScale(e.scale));
        eventBus.on("updatefindmatchescount", (e: { matchesCount: { current: number; total: number } }) => setMatches(e.matchesCount));
        eventBus.on("updatefindcontrolstate", (e: { matchesCount: { current: number; total: number } }) => setMatches(e.matchesCount));
        v.setDocument(doc);
        linkService.setDocument(doc);
        viewer.current = v;
        bus.current = eventBus;
        setPdf(doc);
        const items = await outlineOf(doc);
        if (live) setOutline(items);
      } catch (e) {
        if (live && e instanceof Error && e.name === "PasswordException") setAsking({ wrong: password !== undefined });
        else if (live) setError(e instanceof Error ? e.message : String(e));
      }
    })();
    return () => {
      live = false;
      viewer.current?.setDocument(null as never);
      viewer.current = null;
      bus.current = null;
      void doc?.destroy();
    };
  }, [bytes, password]);

  // Fit to width stays fit to width as the window's width changes.
  useEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const observer = new ResizeObserver(() => {
      const v = viewer.current;
      if (el.clientWidth && v && v.currentScaleValue === "page-width") v.currentScaleValue = "page-width";
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  // Chromium sends trackpad pinch as a control-modified wheel event. Keep the point
  // under the gesture in place as pdf.js relays out the pages at the new scale.
  useEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const pinch = (e: WheelEvent) => {
      const v = viewer.current;
      if (!e.ctrlKey || !v || !v.pdfDocument || !e.deltaY) return;
      e.preventDefault();
      const oldScale = v.currentScale;
      const next = Math.min(5, Math.max(0.1, oldScale * Math.exp(-e.deltaY * 0.01)));
      if (next === oldScale) return;
      const page = [...(pagesEl.current?.querySelectorAll<HTMLElement>(".page") ?? [])].find((p) => {
        const r = p.getBoundingClientRect();
        return e.clientX >= r.left && e.clientX <= r.right && e.clientY >= r.top && e.clientY <= r.bottom;
      });
      const before = page?.getBoundingClientRect();
      v.updateScale({ scaleFactor: next / oldScale, drawingDelay: 150 });
      if (page && before) {
        const after = page.getBoundingClientRect();
        const ratio = v.currentScale / oldScale;
        el.scrollLeft += after.left + (e.clientX - before.left) * ratio - e.clientX;
        el.scrollTop += after.top + (e.clientY - before.top) * ratio - e.clientY;
      }
    };
    el.addEventListener("wheel", pinch, { passive: false });
    return () => el.removeEventListener("wheel", pinch);
  }, []);

  const find = (again: boolean, previous = false) => {
    if (!query) return;
    bus.current?.dispatch("find", { source: null, type: again ? "again" : "", query, caseSensitive: false, entireWord: false, highlightAll: true, findPrevious: previous, matchDiacritics: false });
  };
  const closeSearch = () => {
    setQuery(null);
    setMatches(null);
    bus.current?.dispatch("findbarclose", { source: null });
  };
  const zoom = (k: number | "fit") => {
    const v = viewer.current;
    if (!v) return;
    if (k === "fit") v.currentScaleValue = "page-width";
    else v.currentScale = Math.min(5, Math.max(0.25, v.currentScale * k));
  };
  const goPage = (n: number) => {
    if (viewer.current && pdf) viewer.current.currentPageNumber = Math.min(pdf.numPages, Math.max(1, n));
  };

  // Keys while the view has focus: ⌘F search, ⌘+ ⌘- ⌘0 zoom; Esc closes the search before the preview.
  const onKeyDown = (e: React.KeyboardEvent) => {
    const mod = e.metaKey || e.ctrlKey;
    if (mod && e.key === "f") {
      e.preventDefault();
      setQuery((q) => q ?? "");
      setTimeout(() => searchBox.current?.select());
    } else if (mod && (e.key === "=" || e.key === "+")) {
      e.preventDefault();
      zoom(1.2);
    } else if (mod && e.key === "-") {
      e.preventDefault();
      zoom(1 / 1.2);
    } else if (mod && e.key === "0") {
      e.preventDefault();
      zoom("fit");
    } else if (e.key === "Escape" && query !== null) {
      e.preventDefault();
      e.stopPropagation();
      closeSearch();
    }
  };

  const pages = pdf?.numPages ?? 0;
  return (
    <div className="dcp-in" onKeyDown={onKeyDown}>
      {/* Where you are on the left, how big in the middle, the tools on the right. */}
      <div className="dcp-bar">
        <div className="dcp-bar-start">
          <button className={`tb ${side ? "on" : ""}`} title={t("preview.sidebar")} aria-label={t("preview.sidebar")} aria-pressed={side !== null} disabled={!pdf} onClick={() => setSide(side ? null : outline.length ? "outline" : "pages")}>
            <Icon name="sidebar" />
          </button>
          <span className="dcp-sep" />
          <form
            className="dcp-page"
            onSubmit={(e) => {
              e.preventDefault();
              if (pageDraft !== null) goPage(Number(pageDraft));
              setPageDraft(null);
            }}
          >
            <input aria-label={t("preview.page")} value={pageDraft ?? String(page)} onChange={(e) => setPageDraft(e.target.value.replace(/\D/g, ""))} onFocus={(e) => e.target.select()} onBlur={() => setPageDraft(null)} />
            <span>/ {pages}</span>
          </form>
        </div>
        <div className="dcp-zoom">
          <button title={t("preview.zoomOut")} aria-label={t("preview.zoomOut")} onClick={() => zoom(1 / 1.2)}>
            <Icon name="minus" />
          </button>
          <button className="dcp-scale" title={t("preview.fit")} onClick={() => zoom("fit")}>
            {Math.round(scale * 100)}%
          </button>
          <button title={t("preview.zoomIn")} aria-label={t("preview.zoomIn")} onClick={() => zoom(1.2)}>
            <Icon name="plus" />
          </button>
        </div>
        <div className="dcp-bar-end">
          <button className={`tb ${query !== null ? "on" : ""}`} title={t("preview.search")} aria-label={t("preview.search")} onClick={() => (query === null ? (setQuery(""), setTimeout(() => searchBox.current?.focus())) : closeSearch())}>
            <Icon name="search" />
          </button>
        </div>
      </div>
      {query !== null && (
        <form className="dcp-find" onSubmit={(e) => e.preventDefault()}>
          <Icon name="search" />
          <input
            ref={searchBox}
            value={query}
            placeholder={t("preview.searchPlaceholder")}
            onChange={(e) => (setQuery(e.target.value), setMatches(null))}
            onKeyDown={(e) => {
              if (e.key !== "Enter") return;
              e.preventDefault();
              if (matches === null) find(false);
              else find(true, e.shiftKey);
            }}
          />
          {matches && <span className="dcp-count">{matches.total ? `${matches.current}/${matches.total}` : t("preview.noMatch")}</span>}
        </form>
      )}
      <div className="dcp-body">
        {side && pdf && <Side side={side} setSide={setSide} outline={outline} pdf={pdf} page={page} go={goPage} />}
        <div className="dcp-view">
          <div className="dcp-scroll" ref={scroller} tabIndex={0}>
            <div className="pdfViewer" ref={pagesEl} />
          </div>
          {!pdf && !error && !asking && (
            <div className="dcp-wait">
              <span className="mini-spin" />
            </div>
          )}
          {error && <div className="dcp-error">{error}</div>}
          {asking && (
            <div className="dcp-wait">
              <PasswordForm
                name={name}
                wrong={asking.wrong}
                onSubmit={async (p) => {
                  setAsking(null);
                  setPassword(p);
                }}
              />
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function PasswordForm({ name, wrong, onSubmit }: { name: string; wrong: boolean; onSubmit: (password: string) => Promise<void> }) {
  const { t } = useTranslation("her");
  const [password, setPassword] = useState("");
  const [shown, setShown] = useState(false);
  const [busy, setBusy] = useState(false);
  // Each refusal shakes the field again.
  const [tries, setTries] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    input.current?.focus();
    if (wrong) setTries((n) => n + 1);
  }, [wrong]);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!password || busy) return;
    setBusy(true);
    await onSubmit(password);
    setBusy(false);
    setPassword("");
    input.current?.focus();
  };
  return (
    <form className="dcp-unlock" onSubmit={submit}>
      <span className="dcp-unlock-badge">
        <Icon name="lock" />
      </span>
      <b>{t("preview.lockedTitle")}</b>
      <p title={name}>{t("preview.locked", { name })}</p>
      <div key={tries} className={`dcp-unlock-field ${wrong ? "bad" : ""}`}>
        <input
          ref={input}
          type={shown ? "text" : "password"}
          autoComplete="off"
          spellCheck={false}
          value={password}
          placeholder={t("preview.password")}
          aria-label={t("preview.password")}
          aria-invalid={wrong}
          disabled={busy}
          onChange={(e) => setPassword(e.target.value)}
        />
        {password && (
          <button type="button" className="dcp-unlock-eye" title={t(shown ? "preview.hidePassword" : "preview.showPassword")} aria-label={t(shown ? "preview.hidePassword" : "preview.showPassword")} onClick={() => (setShown(!shown), input.current?.focus())}>
            <Icon name={shown ? "eyeOff" : "eye"} />
          </button>
        )}
        {(password || busy) && (
          <button className="dcp-unlock-go" title={t("preview.unlock")} aria-label={t("preview.unlock")} disabled={busy}>
            {busy ? <span className="mini-spin" /> : <Icon name="arrowR" />}
          </button>
        )}
      </div>
      <span className="dcp-unlock-bad" role="alert">{wrong ? t("preview.wrongPassword") : ""}</span>
    </form>
  );
}

/** The outline and the pages, beside the canvas. */
function Side({ side, setSide, outline, pdf, page, go }: { side: "outline" | "pages"; setSide: (s: "outline" | "pages") => void; outline: OutlineItem[]; pdf: pdfjs.PDFDocumentProxy; page: number; go: (n: number) => void }) {
  const { t } = useTranslation("her");
  return (
    <div className="dcp-side">
      <div className="dcp-side-tabs">
        {outline.length > 0 && (
          <button className={side === "outline" ? "on" : ""} onClick={() => setSide("outline")}>
            {t("preview.outline")}
          </button>
        )}
        <button className={side === "pages" ? "on" : ""} onClick={() => setSide("pages")}>
          {t("preview.pages")}
        </button>
      </div>
      <div className="dcp-side-list">
        {side === "outline" ? <Outline items={outline} go={go} depth={0} /> : Array.from({ length: pdf.numPages }, (_, i) => <Thumb key={i} pdf={pdf} n={i + 1} on={page === i + 1} go={go} />)}
      </div>
    </div>
  );
}

function Outline({ items, go, depth }: { items: OutlineItem[]; go: (n: number) => void; depth: number }) {
  return (
    <>
      {items.map((item, i) => (
        <div key={i}>
          <button className="dcp-ol" style={{ paddingLeft: 10 + depth * 12 }} disabled={!item.page} onClick={() => item.page && go(item.page)}>
            <span>{item.title}</span>
            {item.page && <span className="dcp-ol-p">{item.page}</span>}
          </button>
          {item.children.length > 0 && <Outline items={item.children} go={go} depth={depth + 1} />}
        </div>
      ))}
    </>
  );
}

/** A page's small picture, drawn once it scrolls into sight. */
function Thumb({ pdf, n, on, go }: { pdf: pdfjs.PDFDocumentProxy; n: number; on: boolean; go: (n: number) => void }) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const holder = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (on) holder.current?.scrollIntoView({ block: "nearest" });
  }, [on]);
  useEffect(() => {
    const el = holder.current;
    if (!el) return;
    let task: { cancel: () => void } | null = null;
    const observer = new IntersectionObserver(async ([entry]) => {
      if (!entry.isIntersecting || task) return;
      observer.disconnect();
      const p = await pdf.getPage(n);
      const vp = p.getViewport({ scale: 1 });
      const viewport = p.getViewport({ scale: (110 / vp.width) * window.devicePixelRatio });
      const c = canvas.current;
      if (!c) return;
      c.width = viewport.width;
      c.height = viewport.height;
      const render = p.render({ canvas: c, viewport });
      task = render;
      await render.promise.catch(() => {});
    });
    observer.observe(el);
    return () => {
      observer.disconnect();
      task?.cancel();
    };
  }, [pdf, n]);
  return (
    <button ref={holder} className={`dcp-thumb ${on ? "on" : ""}`} onClick={() => go(n)}>
      <canvas ref={canvas} />
      <span>{n}</span>
    </button>
  );
}
