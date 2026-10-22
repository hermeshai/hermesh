import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Mark } from "@hermesh/ui";
import { errorText } from "../store";

/** While shown, the page waits for pairing requests: their notifications, clicked, come to its window (main/ipc.ts). */
export function useWaitForPairings() {
  useEffect(() => {
    window.hermesh?.waitForPairings(true);
    return () => window.hermesh?.waitForPairings(false);
  }, []);
}

/** An action a sheet waits on; its error stays in the sheet. Resolves to whether it worked. */
export function useAction() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const go = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await action();
      return true;
    } catch (e) {
      setError(errorText(e));
      return false;
    } finally {
      setBusy(false);
    }
  };
  return { busy, error, go };
}

/** While an RPC waits for the signer: the owner checks and touches Touch ID in its window. */
export function SignerWaiting({ detail }: { detail: string }) {
  const { t } = useTranslation("sheets");
  return (
    <div className="waitline">
      <Mark size={40} variant="wait" />
      <div>
        <b>{t("signerWaiting")}</b>
        <span>{detail}</span>
      </div>
    </div>
  );
}

export function ErrorLine({ text }: { text: string }) {
  return text ? <p className="sheet-err" role="alert">{text}</p> : null;
}
