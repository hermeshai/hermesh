import type { Machine } from "@hermesh/proto/local";
import { t } from "./i18n";

export type SetupAction = "none" | "thisMac" | "addProject" | "newGrant" | "loginCodex" | "addMachine";

export interface SetupItem {
  title: string;
  done: boolean;
  optional: boolean;
  action: SetupAction;
}

/** The first run's checklist on the overview, done items ticked. */
export function setupItems(o: {
  localMachine: Machine | undefined;
  projects: number;
  grant: string | undefined;
  finishedTask: boolean;
  machines: number;
}): SetupItem[] {
  const codex = o.localMachine?.status?.agents.find((a) => a.name === "codex");
  const items: SetupItem[] = [
    { title: t("setup.signIn"), done: true, optional: false, action: "none" },
    {
      title: o.localMachine ? t("setup.agentsShared") : t("setup.shareAgents"),
      done: o.localMachine !== undefined,
      optional: false,
      action: "thisMac",
    },
    { title: t("setup.addProject"), done: o.projects > 0, optional: false, action: "addProject" },
    {
      title: o.grant !== undefined ? t("setup.connectGrant", { grant: o.grant }) : t("setup.connectAgent"),
      done: o.grant !== undefined,
      optional: false,
      action: "newGrant",
    },
    { title: t("setup.finishTask"), done: o.finishedTask, optional: false, action: "none" },
  ];
  if (codex?.available) {
    items.push({ title: t("setup.signInCodex"), done: codex.loggedIn, optional: true, action: "loginCodex" });
  }
  items.push({ title: t("setup.addMachine"), done: o.machines > 1, optional: true, action: "addMachine" });
  return items;
}
