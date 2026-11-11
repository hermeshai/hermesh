import { useState } from "react";
import { useTranslation } from "react-i18next";
import { platformTitle } from "@hermesh/core";
import type { Machine } from "@hermesh/proto/local";
import { Mark } from "@hermesh/ui";
import { local } from "../api";
import { Icon } from "../icons";
import { SidebarToggle } from "../MainWindow";
import { ErrorLine, SignerWaiting, useAction } from "../sheets/common";
import { useStore } from "../store";
import { herChoices, newServer, type Choice } from "./environments";

/**
 * Where Her runs (docs/her-server.md §9.2): this Mac, or another of the owner's Macs and Linux machines;
 * Windows machines show, not to be chosen yet; a new server is added as Add Machine does, and offered
 * first once it is paired (fresh). Touch ID turns Her on there (SetHer, signed by the root key), which also
 * lets her use the owner's computers (docs/her-mesh.md §3.3): the line under the choices says so. The first
 * run of the Her entry, and Add Environment.
 */
export function Setup({ onDone, onNewServer, fresh }: { onDone: (machineId: string) => void; onNewServer: () => void; fresh?: string }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const act = useAction();
  const [chosen, setChosen] = useState<Machine | null>(null);
  const choices = herChoices(st.machines).sort((a, b) => Number(b.machine.machineId === fresh) - Number(a.machine.machineId === fresh));
  const turnOn = async (m: Machine) => {
    setChosen(m);
    if (await act.go(() => local.setHer({ machineId: m.machineId, enabled: true }))) onDone(m.machineId);
  };
  return (
    <div className="her-setup">
      <div className="her-choices" role="list" aria-label={t("setup.title")}>
        {choices.map((c) => (
          <ChoiceRow key={c.machine.machineId} c={c} fresh={c.machine.machineId === fresh} busy={act.busy} onTurnOn={() => void turnOn(c.machine)} />
        ))}
        <div className="her-choice" role="listitem">
          <span className="her-choice-i">
            <Icon name="plus" />
          </span>
          <div className="her-choice-t">
            <b>{t("setup.newServer")}</b>
            <span>{t("setup.newServerDesc")}</span>
          </div>
          <button className="btn" disabled={act.busy} onClick={onNewServer}>
            {t("setup.add")}
          </button>
        </div>
      </div>
      <p className="her-setup-note">{t("grant.desc")}</p>
      {act.busy && chosen && <SignerWaiting detail={t("setup.signer", { name: chosen.local ? t("setup.thisMac") : chosen.machineName })} />}
      <ErrorLine text={act.error} />
    </div>
  );
}

function ChoiceRow({ c, fresh, busy, onTurnOn }: { c: Choice<Machine>; fresh: boolean; busy: boolean; onTurnOn: () => void }) {
  const { t } = useTranslation("her");
  const m = c.machine;
  const platform = m.status?.platform ?? "";
  const desc =
    c.why === "windows" ? t("setup.windows")
    : c.why === "offline" ? t("setup.offline")
    : c.why === "unsupported" ? t("setup.unsupported", { detail: c.detail })
    : fresh ? t("setup.paired")
    : m.local ? t("setup.thisMacDesc")
    : platform ? platformTitle(platform)
    : "";
  return (
    <div className={`her-choice ${c.why ? "off" : ""}`} role="listitem">
      <span className="her-choice-i">
        <Icon name={platform.startsWith("linux") ? "server" : "laptop"} />
      </span>
      <div className="her-choice-t">
        <b>{m.local ? t("setup.thisMac") : m.machineName}</b>
        {desc && <span>{desc}</span>}
      </div>
      {!c.why && (
        <button className="btn primary" disabled={busy} onClick={onTurnOn}>
          {t("setup.turnOn")}
        </button>
      )}
    </div>
  );
}

/** The Her entry while Her runs nowhere: where it should run, then the console for a model, then Her says hello. */
export function FirstRun({ onDone }: { onDone: (machineId: string) => void }) {
  const { t } = useTranslation("her");
  const st = useStore();
  // Turned on: the machine says so through Watch in a moment, and the page becomes the environment's.
  const [waiting, setWaiting] = useState(false);
  // The machines there when A New Server was chosen: one paired since is offered first.
  const [before, setBefore] = useState<ReadonlySet<string>>();
  const fresh = before && newServer(st.machines, before);
  return (
    <div className="her-first">
      <div className="ctool drag">
        <SidebarToggle />
      </div>
      <div className="her-first-in">
        <Mark size={44} variant={waiting ? "pulse" : undefined} />
        <h1>{t("setup.title")}</h1>
        <p className="lede">{t("setup.sub")}</p>
        {waiting ? (
          <p className="her-first-wait">
            <span className="mini-spin" />
            {t("setup.turningOn")}
          </p>
        ) : (
          <Setup
            onDone={(id) => {
              setWaiting(true);
              onDone(id);
            }}
            onNewServer={() => {
              setBefore(new Set(st.machines.map((m) => m.machineId)));
              st.set({ sheet: { kind: "addMachine" } });
            }}
            fresh={fresh?.machineId}
          />
        )}
      </div>
    </div>
  );
}
