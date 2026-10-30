import { i18n } from "../../../shared/i18n";
import { keys } from "../platform";
import type { Store } from "../store";

// The palette's actions: everything that adds something.

export type AddAction = "newGrant" | "addMachine" | "addProject" | "enterCode";

/** Each action's label is looked up when read, in the language of the run. */
export const addActions: { id: AddAction; readonly label: string; shortcut: string }[] = (
  [
    { id: "newGrant", shortcut: keys("Shift+CmdOrCtrl+N") },
    { id: "addMachine", shortcut: "" },
    { id: "addProject", shortcut: "" },
    { id: "enterCode", shortcut: keys("Shift+CmdOrCtrl+P") },
  ] as const
).map((a) => ({
  ...a,
  get label() {
    return i18n.t(`overview:add.${a.id}`);
  },
}));

export function openAdd(st: Store, id: AddAction) {
  st.set({ sheet: { kind: id } });
}
