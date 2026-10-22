import { useLayoutEffect } from "react";
import { AddDeviceSheet, AddMachineSheet, AddProjectSheet } from "./sheets/Add";
import { ConfirmSheet } from "./sheets/Confirm";
import { NewGrantSheet } from "./sheets/NewGrant";
import { EnterCodeSheet, PairingSheet } from "./sheets/Pairing";
import { ScopeSheet } from "./sheets/Scope";
import { useStore, type Sheet } from "./store";
import "./sheets/sheets.css";

// Sheets over the window: connect an agent, add machine/project/device, pairing code,
// pairing approval, scope, confirm. One at a time, in st.sheet (the app
// menu's commands open them through commands.ts).

const widths: Record<Sheet["kind"], number> = { newGrant: 620, addMachine: 560, addProject: 540, addDevice: 520, enterCode: 420, pairing: 500, scope: 560, confirm: 440 };

export function SheetHost() {
  const st = useStore();
  const { sheet, set } = st;
  // Before the sheet is painted: an Escape pressed as soon as it shows closes it.
  useLayoutEffect(() => {
    if (!sheet) return;
    const esc = (e: KeyboardEvent) => e.key === "Escape" && set({ sheet: null });
    window.addEventListener("keydown", esc);
    return () => window.removeEventListener("keydown", esc);
  }, [sheet, set]);
  if (!sheet) return null;
  return (
    <>
      <div className="sheet-scrim" />
      <div className="sheet" role="dialog" aria-modal="true" aria-labelledby="sheet-title" style={{ width: widths[sheet.kind] }}>
        <Body key={sheet.kind === "pairing" ? `pairing:${sheet.code}` : sheet.kind} sheet={sheet} />
      </div>
    </>
  );
}

function Body({ sheet }: { sheet: Sheet }) {
  switch (sheet.kind) {
    case "newGrant":
      return <NewGrantSheet />;
    case "addMachine":
      return <AddMachineSheet />;
    case "addProject":
      return <AddProjectSheet machineId={sheet.machineId} agent={sheet.agent} />;
    case "addDevice":
      return <AddDeviceSheet />;
    case "enterCode":
      return <EnterCodeSheet />;
    case "pairing":
      return <PairingSheet code={sheet.code} details={sheet.details} />;
    case "scope":
      return <ScopeSheet clientId={sheet.clientId} renew={sheet.renew} />;
    case "confirm":
      return <ConfirmSheet sheet={sheet} />;
  }
}
