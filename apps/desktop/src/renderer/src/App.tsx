import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { DaemonInfo } from "../../shared/daemon";
import { AboutWindow, UpdatesWindow, WhatsNewWindow } from "./update";
import { FirstRun, useFirstRunMarks, useFirstRunPending } from "./FirstRun";
import { Mark } from "@hermesh/ui";
import { Join } from "./firstrun/SignIn";
import { MainWindow } from "./MainWindow";
import { SettingsWindow } from "./Settings";
import { HermeshProvider, useStore } from "./store";
import { TrayPanel } from "./TrayPanel";

export function App() {
  // The menu bar panel gets what it shows from the main process, not from hermeshd.
  if (location.hash === "#tray") return <TrayPanel />;
  if (location.hash === "#updates") return <UpdatesWindow />;
  if (location.hash === "#about") return <AboutWindow />;
  if (location.hash === "#whats-new") return <WhatsNewWindow />;
  const settings = location.hash.startsWith("#settings");
  return <HermeshProvider>{settings ? <SettingsWindow /> : <Root />}</HermeshProvider>;
}

/** The first run until it is done (an account is not needed), then the main window. */
function Root() {
  const st = useStore();
  const firstRun = useFirstRunPending();
  useFirstRunMarks();
  const [daemon, setDaemon] = useState<DaemonInfo | null>(null);
  useEffect(() => {
    window.hermesh?.daemonState().then(setDaemon);
    return window.hermesh?.onDaemonState(setDaemon);
  }, []);
  // A machine only has the main window too; becoming a trusted device, the join pages. Without
  // an account, this computer's own machine is enough (docs/cold-start-tech.md §3.1).
  const member = st.status?.initialized === true || st.status?.machineOnly === true || st.status?.localMachineEnabled === true;
  const upgrading = st.upgrading && st.status?.machineOnly === true;
  const ready = member && !st.status?.removed && !firstRun && !upgrading;
  // The window takes its size once hermeshd answers, and keeps it if it stops answering later.
  useEffect(() => {
    if (st.status) window.hermesh?.setWindowMode(ready ? "main" : "onboarding");
  }, [ready, st.status]);
  if (daemon && (daemon.state === "stopped" || daemon.state === "stopping" || daemon.state === "starting")) {
    return <DaemonUnavailable daemon={daemon} />;
  }
  if (st.status === undefined) return <div className="empty drag"><Mark size={40} variant="seq" /></div>;
  if (st.status === null) return <DaemonUnavailable daemon={daemon} />;
  if (upgrading)
    return <Join owner={st.status.ownerId} center={st.status.relayUrl} relay={st.status.relayUrl} machine={false} cancel={() => st.set({ upgrading: false })} />;
  return ready ? <MainWindow /> : <FirstRun />;
}

function DaemonUnavailable({ daemon }: { daemon: DaemonInfo | null }) {
  const { t } = useTranslation("common");
  // The installed app's background service waits until the person allows it in System Settings › Login Items.
  const [approval, setApproval] = useState(false);
  // It usually answers within moments of starting; the explanation only if it doesn't.
  const [late, setLate] = useState(false);
  useEffect(() => {
    window.hermesh?.daemonNeedsApproval().then(setApproval);
    const timer = setTimeout(() => setLate(true), 8000);
    return () => clearTimeout(timer);
  }, []);
  if (daemon?.state === "stopped") {
    return (
      <div className="empty drag" style={{ height: "100%" }}>
        <Mark size={40} />
        <b>{t("daemon.stopped")}</b>
        <span>{t("daemon.stoppedDesc")}</span>
        <button className="btn primary" onClick={() => void window.hermesh?.startDaemon()}>{t("daemon.start")}</button>
      </div>
    );
  }
  if (daemon?.state === "stopping") {
    return (
      <div className="empty drag" style={{ height: "100%" }}>
        <Mark size={40} variant="wait" />
        <b>{t("daemon.stopping")}</b>
      </div>
    );
  }
  if (daemon?.state === "starting") {
    return (
      <div className="empty drag" style={{ height: "100%" }}>
        <Mark size={40} variant="wait" />
        <b>{t("daemon.starting")}</b>
      </div>
    );
  }
  return (
    <div className="empty drag" style={{ height: "100%" }}>
      <Mark size={40} variant="wait" />
      <b>{t("daemon.connecting")}</b>
      {approval ? (
        <>
          <span>{t("daemon.needsApproval")}</span>
          <button className="btn" onClick={() => window.hermesh?.openLoginItems()}>{t("daemon.openLoginItems")}</button>
        </>
      ) : (
        late && <span>{t("daemon.noResponse")}</span>
      )}
    </div>
  );
}
