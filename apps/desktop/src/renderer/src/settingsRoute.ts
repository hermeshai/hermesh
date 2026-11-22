// The settings window's pages (#settings/<page>[/<item>], docs/settings-environments.md §4.3).

export const settingsPages = ["account", "general", "notifications", "grants", "env"] as const;
export type SettingsPage = (typeof settingsPages)[number];

/**
 * A page and the item opened in it. An environment's item is its machine id ("this": this Mac,
 * whether or not it is one) and what of it is open: "<id>", "<id>/providers",
 * "<id>/providers/<agent>", "<id>/projects", "<id>/her", "<id>/storage". Older links still work:
 * "devices" (now account), "machines[/<id>[/<agent>]]".
 */
export function settingsRoute(hash: string): { page: SettingsPage; item: string } {
  const [p, ...rest] = hash.split("/").filter(Boolean);
  if (p === "devices") return { page: "account", item: "" };
  if (p === "machines") {
    const [id, agent] = rest;
    return { page: "env", item: !id ? "this" : agent ? `${id}/providers/${agent}` : id };
  }
  const page = settingsPages.find((x) => x === p) ?? "general";
  const item = rest.join("/");
  return { page, item: page === "env" && !item ? "this" : item };
}

/** What the back button goes to: the item one level up, "" at the top. */
export function settingsUp(page: SettingsPage, item: string): string {
  const parts = item.split("/").filter(Boolean);
  // An environment is the top of its page; a grant's list is the top of Access.
  if (page === "env" && parts.length <= 1) return item;
  return parts.slice(0, -1).join("/");
}
