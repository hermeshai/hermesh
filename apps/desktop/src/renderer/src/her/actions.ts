import type { BotSummary } from "@hermesh/her-protocol";
import { i18n } from "../../../shared/i18n";
import type { Store } from "../store";
import type { Her } from "./context";

// What can be done to an assistant: in the
// list, from its right-click menu, pin, rename in place, delete. Its name,
// avatar and model are in its settings panel, deleting too.

const t = i18n.getFixedT(null, "her");

export type BotAction = "pin" | "rename" | "delete";

/** A row's native menu: pin, rename, and delete after a separator (the default assistant can't be deleted). */
export async function botContextMenu(st: Store, her: Her, b: BotSummary) {
  const id = await window.hermesh?.popupMenu([
    { id: "pin", label: b.pinned ? t("menu.unpin") : t("menu.pin") },
    { id: "rename", label: t("menu.rename") },
    ...(b.default ? [] : [{ separator: true as const }, { id: "delete", label: t("menu.delete") }]),
  ]);
  if (id === "pin" || id === "rename" || id === "delete") runBotAction(st, her, b, id);
}

export function runBotAction(st: Store, her: Her, b: BotSummary, id: BotAction) {
  switch (id) {
    case "pin":
      return void her.run(() => her.call("bots/update", { bot_id: b.id, pinned: !b.pinned }));
    case "rename":
      return her.setRenaming(b.id);
    case "delete":
      return st.set({
        sheet: {
          kind: "confirm",
          title: t("delete.title", { name: b.name }),
          message: t("delete.message"),
          button: t("delete.button"),
          run: async () => {
            await her.call("bots/delete", { bot_id: b.id });
            // Its settings go with it, not over to the assistant shown next.
            her.setPanel(null);
          },
          done: t("delete.done", { name: b.name }),
        },
      });
  }
}

/** Clearing an assistant's chat, from its settings: asked first, and what stays is said. */
export function clearBotChat(st: Store, her: Her, b: BotSummary) {
  st.set({
    sheet: {
      kind: "confirm",
      title: t("clear.title", { name: b.name }),
      message: t("clear.message"),
      button: t("clear.button"),
      run: () => her.call("bots/clear", { bot_id: b.id }),
      done: t("clear.done"),
    },
  });
}
