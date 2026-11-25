import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Device } from "@hermesh/proto/local";
import { local } from "../api";
import { useComputerName } from "../computer";
import { herDataKept } from "../her/environments";
import { Icon } from "../icons";
import { useStore } from "../store";
import { copy, More } from "./common";

/**
 * Account & Devices: who this Mac belongs to and where it connects, the
 * trusted devices (each can approve), and signing out. Addresses and
 * commands are folded under Advanced.
 */
export function Account() {
  const { t } = useTranslation("settings");
  const st = useStore();
  const s = st.status;
  if (s?.machineOnly) return <MachineAccount />;
  const user = window.hermesh?.userName || "—";
  const relay = s?.relayUrl ?? "";
  const host = URL.canParse(relay) ? new URL(relay).host : relay;
  const addr = s?.connectAddress ?? "";
  const silent = useElevatesSilently();
  const [advanced, setAdvanced] = useState(false);
  const removeDevice = (d: Device) => {
    // Its own machine goes with it, and Her's data there can go too (docs/her-server.md §5.6).
    const m = st.machines.find((x) => x.machineId === d.machineId);
    st.set({
      sheet: {
        kind: "confirm",
        title: t("removeDeviceTitle", { name: d.label }),
        message: t("account.removeDeviceMessage"),
        button: t("removeButton"),
        run: (eraseHer) => local.removeDevice({ keyId: d.keyId, eraseHer }),
        done: t("removed", { name: d.label }),
        herData: m && (m.her?.enabled || herDataKept(m)) ? { name: m.machineName, offline: !m.online } : undefined,
      },
    });
  };
  // Signing out keeps this Mac's machine, its sessions and Her (docs/cold-start-prd.md §7.4); a
  // device the others removed leaves instead.
  const signOut = () =>
    s?.removed
      ? st.set({
          sheet: {
            kind: "confirm",
            title: t("account.signOutTitle"),
            message: t("account.signOutRemovedMessage"),
            button: t("account.signOutButton"),
            run: async () => {
              await local.leave({});
              window.close();
            },
          },
        })
      : st.set({
          sheet: {
            kind: "confirm",
            title: t("account.signOutAsk"),
            message: t("account.signOutBody"),
            button: t("account.signOutButton"),
            safe: true,
            run: () => local.signOut({}),
            done: t("account.signedOutToast"),
          },
        });
  const signedIn = !!s?.initialized && !s.signedOut;
  const name = useComputerName();
  const others = signedIn ? st.devices.filter((d) => !d.thisDevice) : [];
  return (
    <div className="form">
      <div>
        <div className="fhead">{t("account.sectionAccount")}</div>
        <div className="fgroup">
          {signedIn ? (
            <div className="frow acct-row">
              <span className="av" aria-hidden="true">{user.slice(0, 1).toUpperCase()}</span>
              <div className="ft">
                <div className="fn">{user}</div>
                <div className="fdesc">{s?.hosted ? t("account.hostedLine", { host }) : t("account.ownLine", { host: host || "—" })}</div>
              </div>
              <span className="ok">{s?.hosted ? t("account.signedIn") : t("account.connected")}</span>
              <button className="btn sm" onClick={signOut}>{t("account.signOut")}</button>
            </div>
          ) : (
            <div className="frow acct-row">
              <span className="av out" aria-hidden="true"><Icon name="user" /></span>
              <div className="ft">
                <div className="fn">{t("account.notSignedIn")}</div>
                <div className="fdesc">{t("account.signedOutBody")}</div>
              </div>
              <button className="btn sm primary" onClick={() => st.set({ sheet: { kind: "signIn", way: "official" } })}>{t("account.signIn")}</button>
            </div>
          )}
        </div>
        {!signedIn && (
          <p className="set-note">
            {t("account.localNote")}{" "}
            <button className="link" onClick={() => st.set({ sheet: { kind: "signIn", way: "own" } })}>{t("account.useOwnServer")}</button>
          </p>
        )}
      </div>
      <div>
        <div className="fhead">{t("account.sectionDevices")}</div>
        <div className="fgroup" role="list" aria-label={t("account.sectionDevices")}>
          <div className="frow" role="listitem">
            <span className="acct-tile"><Icon name="laptop" /></span>
            <div className="ft">
              <div className="fn">{name}</div>
              <div className="fdesc">{t("thisMac")}</div>
            </div>
          </div>
          {others.map((d) => (
            <div className="frow" role="listitem" key={d.keyId}>
              <span className="acct-tile"><Icon name={d.platform === "ios" ? "phone" : "laptop"} /></span>
              <div className="ft">
                <div className="fn">{d.label}</div>
                <div className="fdesc">{t("account.viaAccount")}</div>
              </div>
              <button className="btn sm" onClick={() => removeDevice(d)}>{t("account.removeDevice")}</button>
            </div>
          ))}
        </div>
        <p className="set-note">{t("account.devicesNote")}</p>
        {silent && (
          <p className="set-note">
            <span className="warn">{t("account.elevatesSilently")}</span>{" "}
            <button className="link" onClick={() => window.hermesh?.openElevationSettings()}>{t("account.elevationSettings")}</button>
          </p>
        )}
        <div className="set-acts">
          <button className="btn push" onClick={() => st.set({ sheet: { kind: "addDevice" } })}>{t("account.addDevice")}</button>
        </div>
      </div>
      <div>
        <div className="fhead">
          {t("account.sectionNetwork")}
          {!signedIn && <small>{t("account.needsSignIn")}</small>}
        </div>
        <div className="fgroup">
          <div className="frow">
            <span className="acct-tile"><Icon name="robot" /></span>
            <div className="ft">
              <div className="fn">{t("account.agents")}</div>
              <div className="fdesc">{t("account.agentsBody")}</div>
            </div>
            <button className="btn sm" onClick={() => st.set({ sheet: { kind: "newGrant" } })}>{t("account.connectAgent")}</button>
          </div>
          <div className="frow">
            <span className="acct-tile"><Icon name="laptop" /></span>
            <div className="ft">
              <div className="fn">{t("account.computers")}</div>
              <div className="fdesc">{t("account.computersBody")}</div>
            </div>
            <button className="btn sm" onClick={() => st.set({ sheet: { kind: "addMachine" } })}>{t("account.addComputer")}</button>
          </div>
        </div>
      </div>
      {signedIn && (
        <div className="set-acts">
          <button className="btn push" onClick={() => setAdvanced(true)}>{t("account.advancedButton")}</button>
        </div>
      )}
      {advanced && (
        <>
          <div className="sheet-scrim" />
          <div className="sheet set-sheet" role="dialog" aria-label={t("account.advanced")}>
            <h3>{t("account.advanced")}</h3>
            <p className="sub">{t("account.advancedSub")}</p>
            <div className="fgroup">
          <div className="frow">
            <span className="fl">{t(s?.hosted ? "account.server" : "account.relay")}</span>
            <span className="fv">
              {host || "—"} <i className={`dotc ${s?.relayConnected ? "on" : "warn"}`} />
              {t(s?.relayConnected ? "account.connected" : "account.notConnected")}
            </span>
          </div>
          <CopyRow k={t("account.ownerId")} text={s?.ownerId ?? ""} />
          {/* With the official service, agents sign in through a link; only a machine's command carries the address. */}
          {addr && !s?.hosted && <CopyRow k={t("account.connectAddress")} text={addr} />}
          {addr && <CopyRow k={t("account.addMachine")} text={`hermeshd pair ${addr}`} />}
          {addr && !s?.hosted && <CopyRow k={t("account.agentLogin")} text={`hermesh auth login ${addr}`} />}
          {/* An own server's MCP address carries its key; the official one is on hermesh.io. */}
          {s?.mcpUrl && !s?.hosted && <CopyRow k={t("account.mcpUrl")} text={s.mcpUrl} />}
          {s?.consoleUrl && (
            <div className="frow">
              <span className="fl">{t("account.console")}</span>
              <span className="fv">
                {t("account.consoleDesc")}
                <button className="btn sm" onClick={() => window.hermesh?.openExternal(s.consoleUrl)}>
                  <Icon name="ext" />
                  {t("account.open")}
                </button>
              </span>
            </div>
          )}
        </div>
            <div className="sheet-foot">
              {s && !s.removed && (
                <button className="btn danger" onClick={() => (setAdvanced(false), st.set({ sheet: { kind: "erase" } }))}>{t("account.eraseAll")}</button>
              )}
              <span className="push" />
              <button className="btn primary" onClick={() => setAdvanced(false)}>{t("account.done")}</button>
            </div>
          </div>
        </>
      )}
    </div>
  );
}

/**
 * A machine only's account: whose it is and where it connects, folded its
 * owner id; it approves nothing and has no devices to list. It can become a
 * trusted device (in the main window), or leave.
 */
function MachineAccount() {
  const { t } = useTranslation("settings");
  const { t: f } = useTranslation("firstrun");
  const st = useStore();
  const s = st.status!;
  const user = window.hermesh?.userName || "—";
  const host = URL.canParse(s.relayUrl) ? new URL(s.relayUrl).host : s.relayUrl;
  const leave = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: f("machineOnly.leaveTitle"),
        message: f("machineOnly.leaveMessage"),
        button: f("machineOnly.leaveButton"),
        run: async () => {
          await local.leave({});
          window.close();
        },
      },
    });
  return (
    <div className="form">
      <div className="dhero">
        <span className="av xl me" aria-hidden="true">{user.slice(0, 1).toUpperCase()}</span>
        <div>
          <b>{user}</b>
          <span>
            {s.hosted ? t("account.hosted", { host }) : t("account.ownServer", { host: host || "—" })}
            {!s.relayConnected && <span className="warn"> · {t("account.notConnected")}</span>}
          </span>
        </div>
      </div>
      <p className="set-note">{t("account.machineOnlyNote")}</p>
      <More label={t("account.advanced")}>
        <div className="fgroup">
          <div className="frow">
            <span className="fl">{t(s.hosted ? "account.server" : "account.relay")}</span>
            <span className="fv">
              {host || "—"} <i className={`dotc ${s.relayConnected ? "on" : "warn"}`} />
              {t(s.relayConnected ? "account.connected" : "account.notConnected")}
            </span>
          </div>
          <CopyRow k={t("account.ownerId")} text={s.ownerId} />
        </div>
      </More>
      <div className="set-acts">
        <button className="btn" onClick={() => (window.hermesh?.showUpgrade(), window.close())}>{f("machineOnly.upgrade")}</button>
        <button className="btn danger push" onClick={leave}>{f("machineOnly.leave")}</button>
      </div>
    </div>
  );
}

function CopyRow({ k, text }: { k: string; text: string }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  return (
    <div className="frow">
      <span className="fl">{k}</span>
      <span className="fv">
        <span className="mono sel-text cut">{text || "—"}</span>
        <button className="btn sm" aria-label={t("copyLabel", { label: k })} disabled={!text} onClick={() => copy(st, text)}>
          <Icon name="copy" />
          {t("copy")}
        </button>
      </span>
    </div>
  );
}

/** Whether programs become administrator here without asking, with the signer set up (Windows). */
function useElevatesSilently(): boolean {
  const [silent, setSilent] = useState(false);
  useEffect(() => {
    window.hermesh?.elevatesSilently().then(setSilent, () => {});
  }, []);
  return silent;
}
