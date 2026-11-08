import { useState } from "react";
import { useTranslation } from "react-i18next";
import { PageTop } from "../components";
import { errorText } from "../format";
import { useHer } from "../her";
import { Icon } from "../icons";
import { exportData } from "../routes";

// Data: this environment's data as one .zip, downloaded to the device the console is open on
// (her's data/export, through hermeshd's export route). What is in it, and what is left out:
// the keys and sign-ins, which stay on the Her Server. Backup and restore come later.

/** Hands the file to the browser, which saves it where downloads go or asks where. */
function save(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

export function DataPage() {
  const { t } = useTranslation();
  const her = useHer();
  const [busy, setBusy] = useState(false);

  const run = async () => {
    setBusy(true);
    try {
      const { blob, name } = await exportData();
      save(blob, name);
      her.show(t("data.exported", { name }));
    } catch (e) {
      her.show(errorText(e), undefined, true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <PageTop title={t("nav.data")} />
      <div className="set-body">
        <div className="form">
          <section>
            <div className="fgroup">
              <div className="frow">
                <div className="ft">
                  <span className="fn">{t("data.title")}</span>
                  <div className="fdesc">{t("data.desc")}</div>
                </div>
                <button className="btn" disabled={busy} onClick={() => void run()}>
                  {busy ? <span className="mini-spin" /> : <Icon name="download" />}
                  {busy ? t("data.exporting") : t("data.export")}
                </button>
              </div>
            </div>
            <p className="set-note">{t("data.holds")}</p>
            <p className="set-note">{t("data.leaves")}</p>
          </section>
        </div>
      </div>
    </>
  );
}
