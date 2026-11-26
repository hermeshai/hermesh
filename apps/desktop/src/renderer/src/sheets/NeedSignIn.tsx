import { useTranslation } from "react-i18next";
import { Icon } from "../icons";
import { useStore } from "../store";

/**
 * What needs an account, while there is none (docs/cold-start-prd.md §6.2): it says so, and signs
 * in from here. What was asked for is not taken up again once signed in.
 */
export function NeedSignInSheet({ what }: { what: "newGrant" | "addMachine" | "enterCode" }) {
  const { t } = useTranslation("sheets");
  const st = useStore();
  return (
    <div className="need-in">
      <span className="need-tile" aria-hidden="true">
        <Icon name="user" />
      </span>
      <h3 id="sheet-title">{t(`needSignIn.${what}`)}</h3>
      <p className="sub">{t("needSignIn.body")}</p>
      <div className="need-acts">
        <button className="btn primary" autoFocus onClick={() => st.set({ sheet: { kind: "signIn", way: "official" } })}>
          {t("needSignIn.signIn")}
        </button>
        <button className="btn" onClick={() => st.set({ sheet: null })}>
          {t("cancel")}
        </button>
      </div>
    </div>
  );
}
