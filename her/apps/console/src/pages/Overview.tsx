import { useEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { McpListResult, SkillsListResult, WebSearchStatus } from "@hermesh/her-protocol";
import { PageTop } from "../components";
import { useConfig, useHer } from "../her";
import { Icon } from "../icons";
import { readyModel, sitesOf, startsWith } from "./settings";
import type { Page } from "../App";

// Overview: whether Her can work here, what this environment has set up, each line opening
// its page, and her's version.

/** One line: what, its state, and its page. */
function Line({ label, children, onOpen, action }: { label: string; children: ReactNode; onOpen: () => void; action?: ReactNode }) {
  return (
    <div className="frow ov-line">
      <button className="ov-go" onClick={onOpen}>
        <span className="fn">{label}</span>
        <span className="fv">{children}</span>
        {!action && <Icon name="chevR" className="chev" />}
      </button>
      {action}
    </div>
  );
}

export function OverviewPage({ go }: { go: (page: Page) => void }) {
  const { t } = useTranslation();
  const { rpc, models } = useHer();
  const [mcp, setMcp] = useState<McpListResult | null>(null);
  const [skills, setSkills] = useState<SkillsListResult | null>(null);
  const [search, setSearch] = useState<WebSearchStatus | null>(null);
  const { config } = useConfig();
  useEffect(() => {
    if (!rpc) return;
    const readMcp = () => void rpc.request("mcp/list", {}).then(setMcp, () => {});
    const readSkills = () => void rpc.request("skills/list", {}).then(setSkills, () => {});
    const readSearch = () => void rpc.request("web_search/status", {}).then(setSearch, () => {});
    readMcp();
    readSkills();
    readSearch();
    const offs = [
      rpc.on("mcp/changed", readMcp),
      rpc.on("skills/changed", readSkills),
      // The file names servers, skills turned off and search services too.
      rpc.on("config/changed", () => (readSearch(), readMcp(), readSkills())),
    ];
    return () => offs.forEach((off) => off());
  }, [rpc]);

  const model = readyModel(models, startsWith(models, config).model);
  const servers = mcp?.servers ?? [];
  const attention = servers.filter((s) => s.enabled && (s.status.state === "failed" || s.status.state === "needs_login")).length;
  const on = (skills?.skills ?? []).filter((s) => s.state === "active").length;
  const current = search?.sources.find((s) => s.id === search.current);
  const sites = sitesOf(config).filter(([, level]) => level !== "deny").length;
  const extra = (config as { instructions?: { extra?: unknown } } | null)?.instructions?.extra;
  const wait = <span className="mini-spin" />;

  return (
    <>
      <PageTop title={t("nav.overview")} />
      <div className="set-body">
        <div className="form overview">
          <section>
            <div className="fgroup">
              <Line
                label={t("overview.model")}
                onOpen={() => go(model ? "defaults" : "models")}
                action={
                  models &&
                  config !== null &&
                  !model && (
                    <button className="btn primary" onClick={() => go("models")}>
                      {t("overview.connect")}
                    </button>
                  )
                }
              >
                {!models || config === null ? (
                  wait
                ) : model ? (
                  <span className="ov-state ok">
                    <i className="dotc on" />
                    {t("overview.ready", { name: model.name })}
                  </span>
                ) : (
                  <span className="ov-state warn">
                    <i className="dotc warn" />
                    {t("overview.noModel")}
                  </span>
                )}
              </Line>
              <Line label={t("nav.connectors")} onOpen={() => go("connectors")}>
                {!mcp ? wait : !servers.length ? t("overview.none") : attention ? (
                  <span className="ov-state warn">{t("overview.connectorsAttention", { count: servers.length, attention })}</span>
                ) : (
                  t("overview.connectors", { count: servers.length })
                )}
              </Line>
              <Line label={t("nav.skills")} onOpen={() => go("skills")}>
                {!skills ? wait : t("overview.skills", { count: on })}
              </Line>
              <Line label={t("nav.search")} onOpen={() => go("search")}>
                {!search ? wait : !search.enabled ? t("overview.searchOff") : current ? t("overview.searchVia", { name: current.name }) : <span className="ov-state warn">{t("overview.searchNone")}</span>}
              </Line>
              <Line label={t("nav.browser")} onOpen={() => go("browser")}>
                {config === null ? wait : sites ? t("overview.sites", { count: sites }) : t("overview.noSites")}
              </Line>
              <Line label={t("nav.instructions")} onOpen={() => go("instructions")}>
                {config === null ? wait : typeof extra === "string" && extra.trim() ? t("overview.instructionsSet") : t("overview.none")}
              </Line>
            </div>
          </section>
          <section>
            <div className="fgroup">
              <div className="frow">
                <span className="fn">{t("overview.version")}</span>
                <span className="fv mono">her {__HER_VERSION__}</span>
              </div>
            </div>
          </section>
        </div>
      </div>
    </>
  );
}
