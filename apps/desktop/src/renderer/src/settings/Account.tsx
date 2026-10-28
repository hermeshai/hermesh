import { useTranslation } from "react-i18next";
import type { Device } from "@hermesh/proto/local";
import { platformTitle } from "@hermesh/core";
import { local } from "../api";
import { Icon } from "../icons";
import { useStore } from "../store";
import { copy, More, type Ask } from "./common";

/**
 * Account & Devices: who this Mac belongs to and where it connects, the
 * trusted devices (each can approve), and signing out. Addresses and
 * commands are folded under Advanced.
 */
export function Account({ ask }: { ask: (a: Ask) => void }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  const s = st.status;
  if (s?.machineOnly) return <MachineAccount />;
  const user = window.hermesh?.userName || "—";
  const relay = s?.relayUrl ?? "";
  const host = URL.canParse(relay) ? new URL(relay).host : relay;
  const addr = s?.connectAddress ?? "";
  const removeDevice = (d: Device) =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("removeTitle", { name: d.label }),
        message: t("account.removeDeviceMessage"),
        button: t("removeButton"),
        run: () => local.removeDevice({ keyId: d.keyId }),
        done: t("removed", { name: d.label }),
      },
    });
  const leave = () =>
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
      : ask({
          title: t("account.signOutTitle"),
          message: t("account.signOutFirstMessage"),
        });
  return (
    <div className="form">
      <div className="dhero">
        <span className="av xl me" aria-hidden="true">{user.slice(0, 1).toUpperCase()}</span>
        <div>
          <b>{user}</b>
          <span>
            {s?.hosted ? t("account.hosted", { host }) : t("account.ownServer", { host: host || "—" })}
            {s && !s.relayConnected && <span className="warn"> · {t("account.notConnected")}</span>}
          </span>
        </div>
      </div>
      <div>
        <div className="fhead">{t("account.trustedDevices")}</div>
        <div className="fgroup" role="list" aria-label={t("account.trustedDevices")}>
          {st.devices.map((d) => (
            <div className="frow" role="listitem" key={d.keyId}>
              <Icon name="laptop" />
              <div className="ft">
                <div className="fn">
                  {d.label}
                  {d.thisDevice && <span className="faint"> {t("thisMac")}</span>}
                </div>
                <div className="fdesc">
                  {[platformTitle(d.platform), d.machineId ? t("account.alsoMachine", { status: t(d.machineOnline ? "online" : "offline") }) : ""].filter(Boolean).join(" · ")}
                </div>
              </div>
              {!d.thisDevice && (
                <button className="btn sm danger" onClick={() => removeDevice(d)}>{t("remove")}</button>
              )}
            </div>
          ))}
          <div className="frow">
            <button className="link" onClick={() => st.set({ sheet: { kind: "addDevice" } })}>{t("account.addDevice")}</button>
          </div>
        </div>
        <p className="set-note">{t("account.devicesNote")}</p>
      </div>
      <More label={t("account.advanced")}>
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
      </More>
      <div className="set-acts">
        {s && !s.removed && (
          <button className="btn danger" onClick={() => st.set({ sheet: { kind: "erase" } })}>{t("account.eraseAll")}</button>
        )}
        <button className="btn danger push" onClick={leave}>{t("account.signOut")}</button>
      </div>
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
