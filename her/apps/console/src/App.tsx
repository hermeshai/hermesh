import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Mark } from "@hermesh/ui";
import { AskSheet, ToastView } from "./components";
import { ConnectorsPage } from "./customize/Connectors";
import { SkillsPage } from "./customize/Skills";
import { useHer } from "./her";
import { Icon, type IconName } from "./icons";
import { BrowserPage } from "./pages/Browser";
import { DataPage } from "./pages/Data";
import { DefaultsPage } from "./pages/Defaults";
import { InstructionsPage } from "./pages/Instructions";
import { ModelsPage } from "./pages/Models";
import { OverviewPage } from "./pages/Overview";
import { SearchPage } from "./pages/Search";

// The console's frame, as the Mac app's settings window: the pages in a sidebar, the page on
// the right under its bar. The page and the item open in it are the address's hash
// (#connectors/github), so a reload or the browser's Back keeps the place, and the console
// needs nothing from the server but its files.

const pages = [
  { id: "overview", icon: "grid" },
  { id: "models", icon: "chip" },
  { id: "connectors", icon: "plug" },
  { id: "skills", icon: "read" },
  { id: "search", icon: "search" },
  { id: "browser", icon: "web" },
  { id: "instructions", icon: "text" },
  { id: "defaults", icon: "toggles" },
  { id: "data", icon: "database" },
] as const satisfies readonly { id: string; icon: IconName }[];

export type Page = (typeof pages)[number]["id"];

function route(hash: string): { page: Page; item: string | null } {
  const [p, ...rest] = hash.replace(/^#/, "").split("/");
  const page = pages.find((x) => x.id === p)?.id ?? "overview";
  return { page, item: rest.length ? decodeURIComponent(rest.join("/")) : null };
}

function useRoute() {
  const [at, setAt] = useState(() => route(location.hash));
  useEffect(() => {
    const changed = () => setAt(route(location.hash));
    window.addEventListener("hashchange", changed);
    return () => window.removeEventListener("hashchange", changed);
  }, []);
  const go = useCallback((page: Page, item?: string) => {
    location.hash = item ? `${page}/${encodeURIComponent(item)}` : page;
  }, []);
  return { ...at, go };
}

export function App() {
  const { t } = useTranslation();
  const { rpc, lost } = useHer();
  const { page, item, go } = useRoute();
  const open = useCallback((i?: string) => go(page, i), [go, page]);
  useEffect(() => {
    document.title = `${t(`nav.${page}`)} · Her`;
  }, [page, t]);

  return (
    <div className="console">
      <aside className="side">
        <div className="side-top">
          <Mark size={18} />
          <b>Her</b>
        </div>
        <nav className="side-nav" aria-label={t("nav.label")}>
          {pages.map((p) => (
            <button key={p.id} className={`nav-i ${p.id === page ? "sel" : ""}`} aria-current={p.id === page ? "page" : undefined} onClick={() => go(p.id)}>
              <Icon name={p.icon} />
              {t(`nav.${p.id}`)}
            </button>
          ))}
        </nav>
      </aside>
      <main className="set-main" aria-label={t(`nav.${page}`)}>
        {/* Until a connection answers again: one being opened is not yet one that works. */}
        {(!rpc || lost) && (
          <div className="conn-bar" role="status">
            <span className="mini-spin" />
            {lost ?? t("conn.connecting")}
          </div>
        )}
        {page === "overview" && <OverviewPage go={go} />}
        {page === "models" && <ModelsPage />}
        {page === "connectors" && <ConnectorsPage item={item} go={open} />}
        {page === "skills" && <SkillsPage item={item} go={open} />}
        {page === "search" && <SearchPage go={go} />}
        {page === "browser" && <BrowserPage />}
        {page === "instructions" && <InstructionsPage />}
        {page === "defaults" && <DefaultsPage go={go} />}
        {page === "data" && <DataPage />}
      </main>
      <ToastView />
      <AskSheet />
    </div>
  );
}
