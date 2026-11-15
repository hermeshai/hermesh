import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { StorageUsage } from "@hermesh/her-protocol";
import { PageTop } from "../components";
import { bytesText, errorText } from "../format";
import { useHer } from "../her";
import { Icon } from "../icons";

// Storage: where this environment keeps Her's data, that it is the only copy, and how much
// of each kind there is (storage/usage, counted each time the page opens). Only to know:
// nothing here changes, moves or deletes anything.

export function StoragePage() {
  const { t } = useTranslation("settings");
  const { t: tc } = useTranslation();
  const { rpc, show } = useHer();
  const [usage, setUsage] = useState<StorageUsage | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  useEffect(() => {
    if (!rpc) return;
    let current = true;
    rpc.request("storage/usage", {}).then(
      (u) => current && (setUsage(u), setFailed(null)),
      (e: unknown) => current && setFailed(errorText(e)),
    );
    return () => {
      current = false;
    };
  }, [rpc]);
  const copy = () => {
    if (!usage) return;
    navigator.clipboard?.writeText(usage.home).then(
      () => show(t("storage.copied")),
      (e: unknown) => show(errorText(e), undefined, true),
    );
  };
  const waiting = <div className="frow">{failed ? <span className="fdesc warn">{tc("failedRead", { error: failed })}</span> : <span className="mini-spin" />}</div>;

  return (
    <>
      <PageTop title={tc("nav.storage")} />
      <div className="set-body">
        <div className="form storage">
          <section>
            <h3 className="fhead">{t("storage.location")}</h3>
            <div className="fgroup">
              {usage ? (
                <div className="frow">
                  <div className="ft">
                    <div className="st-path">{usage.home}</div>
                    <div className="fdesc">{t(usage.custom ? "storage.custom" : "storage.default")}</div>
                  </div>
                  <button className="btn sm" onClick={copy}>
                    <Icon name="copy" />
                    {t("storage.copy")}
                  </button>
                </div>
              ) : (
                waiting
              )}
            </div>
            <p className="set-note">{t("storage.onlyCopy")}</p>
          </section>
          <section>
            <h3 className="fhead">
              {t("storage.usage")}
              {usage && <span className="st-total">{t("storage.total", { size: bytesText(usage.total) })}</span>}
            </h3>
            <div className="fgroup">
              {usage
                ? usage.parts.map((p) => (
                    <div key={p.kind} className="frow st-row" data-kind={p.kind}>
                      <div className="ft">
                        <div className="fn">{t(`storage.kind.${p.kind}.name`)}</div>
                        <div className="fdesc">{t(`storage.kind.${p.kind}.desc`)}</div>
                        <div className="st-meter" aria-hidden>
                          <span style={{ width: `${usage.total ? (p.bytes / usage.total) * 100 : 0}%` }} />
                        </div>
                      </div>
                      <span className="st-size">{bytesText(p.bytes)}</span>
                    </div>
                  ))
                : waiting}
            </div>
            <p className="set-note">{t("storage.countedNote")}</p>
          </section>
        </div>
      </div>
    </>
  );
}
