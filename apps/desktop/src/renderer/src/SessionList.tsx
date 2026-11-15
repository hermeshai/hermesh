import { useEffect, useRef } from "react";
import { Trans, useTranslation } from "react-i18next";
import { sectionCounted } from "@hermesh/core";
import { Icon } from "./icons";
import { filterName, inFilter, listSections, ListViewMenu, sectionTitle, senderName, SessionRow } from "./Sidebar";
import { useNow, useStore } from "./store";

// The session list's column (docs/her-team.md §14.2): beside the chosen session, the sessions of what the
// sidebar chose (all of them, a machine, a coding agent there, or a connected agent), as the view groups them.

export function SessionList() {
  const st = useStore();
  const { t } = useTranslation("common");
  const now = useNow(30_000);
  const sections = listSections(st, now);
  const total = sections.reduce((n, x) => n + x.sessions.length, 0);
  const list = useRef<HTMLDivElement>(null);
  const end = useRef<HTMLDivElement>(null);
  // The rows get the store as of a click, so a change elsewhere doesn't render them again.
  const latest = useRef(st);
  latest.current = st;
  // Older sessions load as the end of the list comes into view (again
  // after each page, until the list is full or there are no more).
  const { moreSessions, loadOlderSessions } = st;
  useEffect(() => {
    if (!moreSessions || !end.current) return;
    const io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && loadOlderSessions(), { root: list.current, rootMargin: "200px" });
    io.observe(end.current);
    return () => io.disconnect();
  }, [moreSessions, loadOlderSessions, st.sessions]);
  // A session opened from elsewhere (Her's card, Work, a notification) the filter leaves out: the list shows all.
  const sel = st.selection;
  const chosen = sel.kind === "session" ? st.sessions.find((s) => s.sessionId === sel.id) : undefined;
  const { set } = st;
  useEffect(() => {
    if (chosen && !inFilter(st.listFilter, chosen)) set({ listFilter: null });
  }, [chosen, st.listFilter, set]);
  const f = st.listFilter;
  const title = !f ? t("sidebar.allSessions") : f.kind === "client" ? t("sidebar.sentBy", { name: senderName(st, f.client) }) : filterName(st, f);
  return (
    <section className="slist" aria-label={title}>
      <div className="slist-head drag">
        <h2 title={title}>{title}</h2>
        <span className="n">{total}</span>
        <ListViewMenu />
      </div>
      <div className="side-list" ref={list}>
        <div className="side-sessions" role="list" aria-label={t("sidebar.sessions")}>
          {sections.map((sec) => {
            const id = `${sec.grouping}:${sec.key}`;
            const open = !st.collapsed.includes(id);
            return (
              <div key={id} role="group" aria-label={sectionTitle(st, sec)}>
                <button
                  className={`grp-h ${open ? "" : "shut"}`}
                  aria-expanded={open}
                  onClick={() => st.set({ collapsed: open ? [...st.collapsed, id] : st.collapsed.filter((c) => c !== id) })}
                >
                  {sectionTitle(st, sec)}
                  {sectionCounted(sec) && <span className="n">{sec.sessions.length}</span>}
                  <Icon name="caret" className="sm" />
                </button>
                {open && sec.sessions.map((s) => (
                  <SessionRow
                    key={s.sessionId}
                    s={s}
                    sel={sel.kind === "session" && sel.id === s.sessionId}
                    grant={senderName(st, s.createdBy)}
                    meta={st.view.metadata}
                    renaming={st.renaming === s.sessionId}
                    store={latest}
                  />
                ))}
              </div>
            );
          })}
          {!st.sessionsLoaded ? null : st.moreSessions ? (
            <div className="endrow" ref={end}>{t("sidebar.loadingOlder")}</div>
          ) : total === 0 ? (
            <div className="endrow" style={{ paddingTop: 30 }}>
              {f ? t("sidebar.noneHere") : <Trans t={t} i18nKey="sidebar.empty" components={{ br: <br /> }} />}
            </div>
          ) : (
            <div className="endrow">
              {t("sidebar.noOlder")}
              {" · "}
              <button className="link" onClick={() => st.set({ archived: !st.archived })}>{t(st.archived ? "sidebar.hideArchived" : "sidebar.showArchived")}</button>
            </div>
          )}
        </div>
      </div>
    </section>
  );
}

/** Beside the list with no session chosen. */
export function NoSession() {
  const { t } = useTranslation("common");
  return (
    <>
      <div className="ctool drag" />
      <div className="nosession">{t("sidebar.chooseSession")}</div>
    </>
  );
}
