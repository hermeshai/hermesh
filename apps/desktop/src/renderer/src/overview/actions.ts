import { i18n } from "../../../shared/i18n";
import type { Store } from "../store";

// What "＋" offers, and the palette's actions: everything that adds something.

export type AddAction = "newGrant" | "addMachine" | "addProject" | "enterCode";

/** Each action's label is looked up when read, in the language of the run. */
export const addActions: { id: AddAction; readonly label: string; shortcut: string }[] = (
  [
    { id: "newGrant", shortcut: "⇧⌘N" },
    { id: "addMachine", shortcut: "" },
    { id: "addProject", shortcut: "" },
    { id: "enterCode", shortcut: "⇧⌘P" },
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

/** The native "＋" menu; the pairing code comes after a separator, as in the File menu. */
export async function addMenu(st: Store) {
  const [grant, machine, project, code] = addActions;
  const id = await window.hermesh?.popupMenu([
    { id: grant.id, label: grant.label },
    { id: machine.id, label: machine.label },
    { id: project.id, label: project.label },
    { separator: true },
    { id: code.id, label: code.label },
  ]);
  const a = addActions.find((x) => x.id === id);
  if (a) openAdd(st, a.id);
}
