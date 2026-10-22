import { useState } from "react";
import { useTranslation } from "react-i18next";
import { setupItems, type SetupAction } from "@hermesh/core";
import { Icon } from "../icons";
import { useStore } from "../store";

const hiddenKey = "setupHidden";

function savedHidden(): boolean {
  try {
    return localStorage.getItem(hiddenKey) === "1";
  } catch {
    return false;
  }
}

/** "Finish Setup": the first steps on a hosted center, until done or hidden. */
export function Setup() {
  const { t } = useTranslation("overview");
  const st = useStore();
  const [hidden, setHidden] = useState(savedHidden);
  if (hidden || !st.status?.hosted) return null;
  const local = st.machines.find((m) => m.local);
  const items = setupItems({
    localMachine: local,
    projects: st.projects.length,
    grant: st.clients.find((c) => !c.revoked)?.label,
    finishedTask: st.sessions.some((s) => (s.progress?.turnCount ?? 0) > 0),
    machines: st.machines.length,
  });
  const left = items.filter((i) => !i.done).length;
  if (left === 0) return null;
  const hide = () => {
    try {
      localStorage.setItem(hiddenKey, "1");
    } catch {
      // Without storage it comes back next time.
    }
    setHidden(true);
  };
  const run = (a: SetupAction) => {
    switch (a) {
      case "thisMac":
        return window.hermesh?.openSettings("general");
      case "addProject":
        return st.set({ sheet: { kind: "addProject", machineId: local?.machineId } });
      case "newGrant":
        return st.set({ sheet: { kind: "newGrant" } });
      case "loginCodex":
        return window.hermesh?.runInTerminal("codexLogin");
      case "addMachine":
        return st.set({ sheet: { kind: "addMachine" } });
    }
  };
  return (
    <section aria-label={t("setup.title")}>
      <div className="sec-h">
        {t("setup.title")}<span className="n">{t("setup.left", { count: left })}</span>
        <button className="link" onClick={hide}>{t("setup.hide")}</button>
      </div>
      <div className="card setup">
        <div className="steps">
          {items.map((i) => (
            <div key={i.title} className={`st-item ${i.done ? "done" : ""}`}>
              <span className="c" role="img" aria-label={i.done ? t("setup.done") : t("setup.notDone")}>{i.done && <Icon name="check" />}</span>
              {!i.done && i.action !== "none" ? <button className="link" onClick={() => run(i.action)}>{i.title}</button> : i.title}
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}
