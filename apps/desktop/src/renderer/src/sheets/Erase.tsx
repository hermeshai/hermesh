import { useEffect, useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import type { EraseItem } from "@hermesh/proto/local";
import { agentName } from "@hermesh/ui";
import { local } from "../api";
import { GrantAvatar } from "../components";
import { Icon } from "../icons";
import { errorText, useStore } from "../store";
import { ErrorLine, SignerWaiting, useAction } from "./common";

// Erase Everything (docs/erase-everything.md): what it erases, by name, and
// what stays, confirmed by typing the account's email (a server of one's
// own: its address) and approved with Touch ID; then each device, machine
// and agent ticked as it reports, and this Mac erased last. Those offline
// are reset (or paired again) when next used.

/** How long this Mac waits for those online to report before it erases itself. */
const waitOnline = 15_000;

export function EraseSheet() {
  const [erase, setErase] = useState<{ id: string; items: EraseItem[] } | null>(null);
  return erase ? <Progress id={erase.id} first={erase.items} /> : <Confirm started={(id, items) => setErase({ id, items })} />;
}

function Confirm({ started }: { started: (id: string, items: EraseItem[]) => void }) {
  const { t } = useTranslation("sheets");
  const st = useStore();
  const [items, setItems] = useState<EraseItem[] | null>(null);
  const [typed, setTyped] = useState("");
  const { busy, error, go } = useAction();
  const [loadError, setLoadError] = useState("");
  useEffect(() => {
    local.eraseStatus({}).then((r) => setItems(r.items), (e) => setLoadError(errorText(e)));
  }, []);
  const relay = st.status?.relayUrl ?? "";
  const host = URL.canParse(relay) ? new URL(relay).host : relay;
  const hosted = st.status?.hosted === true;
  // What is typed to confirm: the account's email, or a server of one's own's address.
  const expected = (hosted && st.status?.account) || host;
  const match = expected !== "" && typed.trim().toLowerCase() === expected.toLowerCase();
  const of = (kind: string) => (items ?? []).filter((it) => it.kind === kind);
  const erase = () => go(async () => started((await local.eraseEverything({})).eraseId, items ?? []));
  return (
    <>
      <h3 id="sheet-title">{t("erase.title")}</h3>
      <p className="sub">{t("erase.sub")}</p>
      <div className="fhead">{t("erase.erases")}</div>
      <div className="fgroup erase-what" aria-busy={items === null}>
        <Group icon="laptop" title={t("erase.devices")} desc={t("erase.devicesDesc")} items={of("device")} />
        <Group icon="server" title={t("erase.machines")} desc={t("erase.machinesDesc")} items={of("machine")} />
        <Group icon="sparkle" title={t("erase.agents")} desc={t("erase.agentsDesc")} items={of("agent")} />
        <div className="frow">
          <Icon name="web" />
          <div className="ft">
            <div className="fn">{t(hosted ? "erase.hosted" : "erase.server", { host })}</div>
            <div className="fdesc">{t(hosted ? "erase.hostedDesc" : "erase.serverDesc")}</div>
          </div>
        </div>
      </div>
      <div className="fhead erase-stays">{t("erase.stays")}</div>
      <ul className="erase-stay">
        <li>{t("erase.stayProjects")}</li>
        <li>{t("erase.stayWorktrees")}</li>
        {hosted && <li>{t("erase.stayAccount")}</li>}
        <li>{t("erase.stayProfiles")}</li>
        <li>{t("erase.stayOffline")}</li>
      </ul>
      <label className="erase-lab" htmlFor="erase-confirm">
        <Trans t={t} i18nKey="erase.confirm" values={{ text: expected }} components={{ b: <b className="sel-text" /> }} />
      </label>
      <input id="erase-confirm" className="field" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} disabled={busy} />
      {busy && <SignerWaiting detail={t("erase.approve")} />}
      <ErrorLine text={loadError || error} />
      <div className="sheet-foot">
        <button className="btn" onClick={() => st.set({ sheet: null })} disabled={busy}>{t("cancel")}</button>
        <button className="btn primary destructive" disabled={!match || items === null || busy} onClick={erase}>{t("erase.button")}</button>
      </div>
    </>
  );
}

/** One kind of what is erased: what happens to it, and each one by name. */
function Group({ icon, title, desc, items }: { icon: string; title: string; desc: string; items: EraseItem[] }) {
  const { t } = useTranslation("sheets");
  if (!items.length) return null;
  return (
    <div className="frow">
      <Icon name={icon} />
      <div className="ft">
        <div className="fn">{title}</div>
        <div className="fdesc">{desc}</div>
        <div className="erase-names">
          {items.map((it, i) => (
            <span key={i} className="erase-name">
              {it.kind === "agent" ? <GrantAvatar label={it.name} size="sm" /> : <i className={`dotc ${it.online || it.thisDevice ? "on" : ""}`} />}
              {it.kind === "agent" ? agentName(it.name) : it.name}
              {it.thisDevice ? (
                <span className="faint"> {t("erase.thisMac")}</span>
              ) : (
                !it.online && <span className="faint"> {t(later[it.kind] ?? "erase.laterDevice")}</span>
              )}
            </span>
          ))}
        </div>
      </div>
    </div>
  );
}

/** Each one ticked as it reports; once those online did (or after a while), this Mac erases itself and the app opens on its first run. */
function Progress({ id, first }: { id: string; first: EraseItem[] }) {
  const { t } = useTranslation("sheets");
  const [items, setItems] = useState(first);
  const [error, setError] = useState("");
  const finishing = useRef(false);
  const finish = async () => {
    if (finishing.current) return;
    finishing.current = true;
    setError("");
    try {
      await local.finishErase({});
      window.close();
    } catch (e) {
      setError(t("erase.finishFailed", { error: errorText(e) }));
      finishing.current = false;
    }
  };
  useEffect(() => {
    const start = Date.now();
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const tick = async () => {
      let now = first;
      try {
        now = (await local.eraseStatus({ eraseId: id })).items;
        setItems(now);
      } catch {
        // The next tick asks again; the wait ends either way.
      }
      if (stopped) return;
      if (Date.now() - start > waitOnline || now.every((it) => it.thisDevice || !it.online || it.done)) void finish();
      else timer = setTimeout(tick, 1000);
    };
    void tick();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [id]); // once, for this erase
  const rows = [...items.filter((it) => !it.thisDevice), ...items.filter((it) => it.thisDevice)];
  return (
    <>
      <h3 id="sheet-title">{t("erase.progressTitle")}</h3>
      <p className="sub">{t("erase.progressSub")}</p>
      <div className="fgroup" role="list">
        {rows.map((it, i) => (
          <div className="frow" role="listitem" key={i}>
            <div className="ft">
              <div className="fn">{it.thisDevice ? t("erase.thisMac") : it.kind === "agent" ? agentName(it.name) : it.name}</div>
            </div>
            <State it={it} />
          </div>
        ))}
      </div>
      {error && (
        <>
          <ErrorLine text={error} />
          <div className="sheet-foot">
            <button className="btn primary destructive" onClick={() => void finish()}>{t("erase.retry")}</button>
          </div>
        </>
      )}
    </>
  );
}

function State({ it }: { it: EraseItem }) {
  const { t } = useTranslation("sheets");
  if (it.done) return <span className="erase-st ok"><Icon name="check" />{t(it.kind === "agent" ? "erase.disconnected" : "erase.erased")}</span>;
  if (it.thisDevice || it.online) return <span className="erase-st"><i className="erase-spin" />{t(it.thisDevice ? "erase.last" : "erase.erasing")}</span>;
  return <span className="erase-st">{t(state[it.kind] ?? "erase.stateDevice")}</span>;
}

/** What becomes of one offline: a device offers a reset, a machine is paired again, an agent signs in again. */
const later: Record<string, "erase.laterDevice" | "erase.laterMachine" | "erase.laterAgent"> = { device: "erase.laterDevice", machine: "erase.laterMachine", agent: "erase.laterAgent" };
const state: Record<string, "erase.stateDevice" | "erase.stateMachine" | "erase.stateAgent"> = { device: "erase.stateDevice", machine: "erase.stateMachine", agent: "erase.stateAgent" };
