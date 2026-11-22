import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { LoginItem, Preferences } from "../../../shared/bridge";
import { useStore } from "../store";
import { SRow, Switch } from "./common";

/**
 * Stay online (docs/settings-environments.md §5.4): this computer reachable while Hermesh's
 * window is closed or the app quit, and after a restart. It says what follows either way;
 * turning it off asks first.
 */
export function StayOnline() {
  const { t } = useTranslation("settings");
  const st = useStore();
  const [prefs, setPrefs] = useState<Preferences>();
  const [login, setLogin] = useState<LoginItem>();
  useEffect(() => {
    window.hermesh?.getPreferences().then(setPrefs);
    window.hermesh?.loginItem().then(setLogin);
  }, []);
  const on = prefs?.stayOnline ?? false;
  const apply = async (v: boolean) => {
    const p = await window.hermesh?.setPreferences({ stayOnline: v });
    if (p) setPrefs(p);
    setLogin(await window.hermesh?.loginItem());
  };
  const set = (v: boolean) =>
    v
      ? void apply(true)
      : st.set({
          sheet: { kind: "confirm", title: t("stayOnline.offTitle"), message: t("stayOnline.offMessage"), button: t("stayOnline.offButton"), safe: true, run: () => apply(false) },
        });
  return (
    <div>
      <div className="fgroup">
        <SRow title={t("stayOnline.title")}>
          <Switch on={on} label={t("stayOnline.title")} disabled={!prefs} set={set} />
        </SRow>
      </div>
      <p className="set-foot">
        {t(on ? "stayOnline.onDesc" : "stayOnline.offDesc")}
        {on && login?.needsApproval && (
          <>
            {" "}
            <span className="warn">{t("general.loginItemsApproval")}</span>{" "}
            <button className="link" onClick={() => window.hermesh?.openLoginItems()}>{t("general.openSystemSettings")}</button>
          </>
        )}
      </p>
    </div>
  );
}
