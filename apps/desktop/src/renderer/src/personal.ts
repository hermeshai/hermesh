import type { GetStatusResponse } from "@hermesh/proto/local";
import type { PersonalAgentKind } from "@hermesh/ui";
import { i18n } from "../../shared/i18n";

/** A personal agent kind's name as the picker shows it (@hermesh/ui names Other in Chinese, for the console). */
export function kindName(k: PersonalAgentKind): string {
  return k.id === "other" ? i18n.t("common:personal.other") : k.name;
}

/**
 * What the owner pastes into the personal agent's chat; name is what it will
 * be called. With the official service the agent signs in through a link it
 * gives the owner (the steps are on the console's /connect page); with a
 * server of the owner's own it pairs with the connection address.
 */
export function connectMessage(status: GetStatusResponse | null | undefined, name: string): string {
  if (status?.hosted && URL.canParse(status.consoleUrl)) {
    return i18n.t("common:personal.connectLink", { url: `${new URL(status.consoleUrl).origin}/connect`, name });
  }
  return i18n.t("common:personal.connectMessage", { address: status?.connectAddress ?? "", name, project: `<${i18n.t("common:personal.projectArg")}>` });
}

/** Whether the agent signs in through a link (connectMessage). */
export function signsInByLink(status: GetStatusResponse | null | undefined): boolean {
  return !!status?.hosted && URL.canParse(status.consoleUrl);
}
