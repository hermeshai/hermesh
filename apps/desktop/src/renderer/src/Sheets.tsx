import { useLayoutEffect, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { AddDeviceSheet, AddMachineSheet, AddProjectSheet } from "./sheets/Add";
import { ConfirmSheet } from "./sheets/Confirm";
import { NeedSignInSheet } from "./sheets/NeedSignIn";
import { SignInFlow } from "./firstrun/SignInFlow";
import { EraseSheet } from "./sheets/Erase";
import { HerOffSheet } from "./sheets/HerOff";
import { NewGrantSheet } from "./sheets/NewGrant";
import { EnterCodeSheet, PairingSheet } from "./sheets/Pairing";
import { ScopeSheet } from "./sheets/Scope";
import { WakeSheet } from "./sheets/Wake";
import { useLeaving } from "./components";
import { useStore, type Sheet, type Store } from "./store";
import "./sheets/sheets.css";

// Sheets over the window: connect an agent, add machine/project/device, pairing code,
// pairing approval, scope, an agent's wake, confirm, erase everything, turning Her off. One at a time, in st.sheet (the app
// menu's commands open them through commands.ts).

const widths: Record<Sheet["kind"], number> = { newGrant: 620, addMachine: 560, addProject: 540, addDevice: 520, enterCode: 420, pairing: 500, scope: 560, wake: 480, confirm: 440, erase: 520, herOff: 460, needSignIn: 300, signIn: 480 };

/** Whether this computer belongs to an owner and is signed in. */
export function signedIn(st: Store): boolean {
  const s = st.status;
  return !!s && (s.initialized || s.machineOnly) && !s.signedOut;
}

/** What needs an account, without one, says so instead (docs/cold-start-prd.md §6.2). */
function gate(sheet: Sheet | null, inside: boolean): Sheet | null {
  if (sheet && !inside && (sheet.kind === "newGrant" || sheet.kind === "addMachine" || sheet.kind === "enterCode")) return { kind: "needSignIn", what: sheet.kind };
  return sheet;
}

export function SheetHost() {
  const st = useStore();
  const { set } = st;
  const inside = signedIn(st);
  const sheet = useMemo(() => gate(st.sheet, inside), [st.sheet, inside]);
  // Before the sheet is painted: an Escape pressed as soon as it shows closes it.
  useLayoutEffect(() => {
    if (!sheet) return;
    // Erasing everything is not left half way: its sheet has its own Cancel.
    const esc = (e: KeyboardEvent) => e.key === "Escape" && sheet.kind !== "erase" && set({ sheet: null });
    window.addEventListener("keydown", esc);
    return () => window.removeEventListener("keydown", esc);
  }, [sheet, set]);
  // Closed, it fades out as it was.
  const { shown, leaving } = useLeaving(sheet);
  if (!shown) return null;
  return (
    <>
      <div className={`sheet-scrim ${leaving ? "leaving" : ""}`} />
      <div className={`sheet ${leaving ? "leaving" : ""}`} inert={leaving} role="dialog" aria-modal="true" aria-labelledby="sheet-title" style={{ width: widths[shown.kind], ...(shown.kind === "signIn" ? { padding: 0 } : {}) }}>
        <Body key={shown.kind === "pairing" ? `pairing:${shown.code}` : shown.kind} sheet={shown} />
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
      return <AddProjectSheet machineId={sheet.machineId} agent={sheet.agent} then={sheet.then} />;
    case "addDevice":
      return <AddDeviceSheet />;
    case "enterCode":
      return <EnterCodeSheet />;
    case "pairing":
      return <PairingSheet code={sheet.code} details={sheet.details} />;
    case "scope":
      return <ScopeSheet clientId={sheet.clientId} renew={sheet.renew} />;
    case "wake":
      return <WakeSheet sheet={sheet} />;
    case "confirm":
      return <ConfirmSheet sheet={sheet} />;
    case "erase":
      return <EraseSheet />;
    case "herOff":
      return <HerOffSheet sheet={sheet} />;
    case "needSignIn":
      return <NeedSignInSheet what={sheet.what} />;
    case "signIn":
      return <SignInSheet way={sheet.way} />;
  }
}

/** Signing in from the main window or Settings: the first run's pages, in a sheet; a toast once done. */
function SignInSheet({ way }: { way: "official" | "own" | "join" }) {
  const st = useStore();
  const { t } = useTranslation("settings");
  return <SignInFlow way={way} close={() => st.set({ sheet: null })} done={(kind) => (st.set({ sheet: null }), st.show(kind === "official" ? t("account.signedInToast") : t("account.ownToast")))} />;
}
