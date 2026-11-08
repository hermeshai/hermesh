import type { TFunction } from "i18next";
import { Trans, useTranslation } from "react-i18next";
import type { McpServerView } from "@hermesh/her-protocol";
import { Icon } from "../icons";
import { brokenSkills, isCommand, statusOf, type SkillGroup, type SkillItem } from "./model";
import { MoreButton, Section, ServerTile, SkillTile, Status, Switch, type MenuItem } from "./parts";
import type { Customize } from "./useCustomize";

// The lists of the Skills and Connectors pages: one to a row, grouped by where each lives.

export type SkillActions = {
  open: (item: SkillItem) => void;
  upload: () => void;
  showBroken: () => void;
  toggle: (item: SkillItem, on: boolean) => void;
};

export type ServerActions = {
  open: (s: McpServerView) => void;
  add: () => void;
  edit: (s: McpServerView) => void;
  toggle: (s: McpServerView, on: boolean) => void;
};

export function skillMenu(item: SkillItem, a: SkillActions, c: Customize, t: TFunction<"customize">, withView: boolean): MenuItem[] {
  return [
    ...(withView ? [{ id: "view", label: t("skill.view"), icon: "info" as const, run: () => a.open(item) }] : []),
    // Only the person's own, in ~/.her/skills.
    ...(item.entry.removable ? [null, { id: "remove", label: withView ? t("skill.remove") : t("skill.removeSkill"), icon: "trash" as const, danger: true, run: () => c.removeSkill(item) }] : []),
  ];
}

export function serverMenu(s: McpServerView, a: ServerActions, c: Customize, t: TFunction<"customize">, withView: boolean): MenuItem[] {
  return [
    ...(withView ? [{ id: "view", label: t("connector.view"), icon: "info" as const, run: () => a.open(s) }] : []),
    ...(s.editable && !s.connector ? [{ id: "edit", label: t("connector.edit"), icon: "edit" as const, run: () => a.edit(s) }] : []),
    { id: "check", label: t("connector.recheck"), icon: "retry" as const, run: () => void c.check(s.name) },
    ...(s.editable ? [null, { id: "remove", label: withView ? t("connector.remove") : t("connector.removeConnector"), icon: "trash" as const, danger: true, run: () => c.removeServer(s) }] : []),
  ];
}

export function MySkills({ c, a }: { c: Customize; a: SkillActions }) {
  const { t } = useTranslation("customize");
  const group = (g: SkillGroup) => c.items.filter((i) => i.group === g);
  const broken = brokenSkills(c.skills);
  const note = broken.length > 0 && (
    <div className="cz-anote">
      <Icon name="warn" />
      <span>
        <Trans t={t} i18nKey="broken.note" count={broken.length} values={{ path: broken[0].path, reason: broken[0].detail ?? "" }} components={{ code: <code /> }} />
      </span>
      <button className="link" onClick={a.showBroken}>
        {t("broken.view")}
      </button>
    </div>
  );
  const rows = (items: SkillItem[]) => (
    <div className="cz-rows">
      {items.map((i) => (
        <SkillRow key={i.dir} item={i} c={c} a={a} />
      ))}
    </div>
  );
  const personal = group("user");
  return (
    <>
      <Section title={t("group.personal")} count={personal.length} note={note}>
        {personal.length ? (
          rows(personal)
        ) : (
          <div className="cz-empty">
            <Icon name="read" />
            <b>{t("empty.skills")}</b>
            <span>{t("empty.skillsHint")}</span>
            <div className="acts">
              <button className="btn" onClick={a.upload}>
                <Icon name="upload" />
                {t("add.skill")}
              </button>
            </div>
          </div>
        )}
      </Section>
      {group("extra").length > 0 && (
        <Section title={t("group.extra")} count={group("extra").length} hint={t("group.extraHint")}>
          {rows(group("extra"))}
        </Section>
      )}
      {group("system").length > 0 && (
        <Section title={t("group.system")} count={group("system").length} hint={t("group.systemHint")}>
          {rows(group("system"))}
        </Section>
      )}
    </>
  );
}

function SkillRow({ item, c, a }: { item: SkillItem; c: Customize; a: SkillActions }) {
  const { t } = useTranslation("customize");
  const open = () => a.open(item);
  return (
    <div className={`cz-row ${item.on ? "" : "off"}`} role="button" tabIndex={0} onClick={open} onKeyDown={(e) => e.key === "Enter" && e.target === e.currentTarget && open()}>
      <SkillTile />
      <div className="rt">
        <div className="rn">
          <span className="nm">{item.entry.name}</span>
          {item.from && <span className="badge">{t("skill.sharedWith", { from: item.from })}</span>}
        </div>
        <div className="rd">{item.entry.description}</div>
      </div>
      <div className="ra">
        {!item.on && <span className="cz-st">{t("skill.off")}</span>}
        <Switch on={item.on} label={item.on ? t("skill.turnOff", { name: item.entry.name }) : t("skill.turnOn", { name: item.entry.name })} onChange={(on) => a.toggle(item, on)} />
        {/* Without Delete, its menu would only open the page a click on the row opens. */}
        {item.entry.removable ? <MoreButton label={t("more")} items={() => skillMenu(item, a, c, t, true)} /> : <span className="cz-more-slot" />}
      </div>
    </div>
  );
}

export function MyConnectors({ c, a }: { c: Customize; a: ServerActions }) {
  const { t } = useTranslation("customize");
  if (c.servers.length === 0)
    return (
      <section className="cz-sect">
        <div className="cz-empty">
          <Icon name="plug" />
          <b>{t("empty.connectors")}</b>
          <span>{t("empty.connectorsHint")}</span>
          <div className="acts">
            <button className="btn" onClick={a.add}>
              <Icon name="plus" />
              {t("add.connector")}
            </button>
          </div>
        </div>
      </section>
    );
  return (
    <Section title={t("group.connected")} count={c.servers.length}>
      <div className="cz-rows">
        {c.servers.map((s) => (
          <ServerRow key={s.name} s={s} c={c} a={a} />
        ))}
      </div>
    </Section>
  );
}

function ServerRow({ s, c, a }: { s: McpServerView; c: Customize; a: ServerActions }) {
  const { t } = useTranslation("customize");
  const open = () => a.open(s);
  return (
    <div className={`cz-row ${s.enabled ? "" : "off"}`} role="button" tabIndex={0} onClick={open} onKeyDown={(e) => e.key === "Enter" && e.target === e.currentTarget && open()}>
      <ServerTile name={s.name} icon={s.icon} />
      <div className="rt">
        <div className="rn">
          <span className="nm">{s.name}</span>
          {isCommand(s) && <span className="badge">{t("addc.local")}</span>}
        </div>
        {s.description ? (
          <div className="rd">{s.description}</div>
        ) : (
          <div className="rd mono" title={s.summary}>
            {s.summary}
          </div>
        )}
      </div>
      <div className="ra">
        <Status look={statusOf(s, c.checking.has(s.name))} onCheck={() => void c.check(s.name)} onLogin={c.signingIn.has(s.name) || s.connector ? undefined : () => void c.login(s.name)} />
        <Switch disabled={!!s.connector && s.auth_state !== "logged_in"} on={s.enabled} label={s.enabled ? t("connector.turnOff", { name: s.name }) : t("connector.turnOn", { name: s.name })} onChange={(on) => a.toggle(s, on)} />
        <MoreButton label={t("more")} items={() => serverMenu(s, a, c, t, true)} />
      </div>
    </div>
  );
}
