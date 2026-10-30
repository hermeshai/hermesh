import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { PairingKind } from "@hermesh/proto/identity";
import type { PairingDetails } from "@hermesh/proto/local";
import { Icon } from "./icons";
import { pairingTitle } from "./sheets/Pairing";
import type { Store } from "./store";
import { useStore } from "./store";

/**
 * The requests waiting for the owner, as a card at the bottom right of the
 * main window, the others stacked under it: they stay until answered or
 * put off and take nothing else from the window. View opens the request's
 * sheet, where it is approved (with Touch ID or Windows Hello); Deny puts
 * it off, as the sheet's Deny does. The overview lists them under Needs
 * You instead.
 */
export function RequestCards() {
  const { t } = useTranslation("sheets");
  const st = useStore();
  const bottom = useAboveBottomBar();
  if (st.sheet || st.selection.kind === "overview" || st.status?.machineOnly || st.pairings.length === 0) return null;
  const list = [...st.pairings].sort((a, b) => Number(b.code === st.frontRequest) - Number(a.code === st.frontRequest));
  const p = list[0];
  const who = p.platform || p.hostname;
  const putOff = () => {
    st.set({ dismissed: [...st.dismissed, p.code] });
    st.show(t("pairing.putOff"));
  };
  return (
    <section className="reqs" style={{ bottom }} aria-label={t("requests.label")}>
      {list.length > 2 && <div className="req-back b2" />}
      {list.length > 1 && <div className="req-back b1" />}
      <div className="req" key={p.code}>
        <div className="req-h">
          <span className="req-i">
            <Icon name={p.kind === PairingKind.DEVICE ? "shield" : p.kind === PairingKind.CLIENT ? "key" : "server"} />
          </span>
          <div className="req-t">
            <b>{pairingTitle(p)}</b>
            <span>
              {p.confirmed ? t("pairing.confirmed", { who }) : (
                <>
                  {who && `${who} · `}
                  {t("requests.code")} <span className="mono">{p.verificationCode}</span>
                </>
              )}
            </span>
          </div>
          {list.length > 1 && <span className="req-n">{t("requests.count", { n: 1, total: list.length })}</span>}
        </div>
        <div className="req-acts">
          <button className="btn" onClick={putOff}>{t("deny")}</button>
          <button className="btn primary" onClick={() => st.set({ sheet: { kind: "pairing", code: p.code } })}>{t("requests.view")}</button>
        </div>
      </div>
    </section>
  );
}

/** A request from a notification or the menu bar: its card in front, no longer put off. */
export function frontRequest(st: Store, code: string) {
  // "Connect Your Agent" shows its agent's request itself.
  if (st.sheet?.kind === "newGrant" && st.pairings.some((p: PairingDetails) => p.code === code && p.kind === PairingKind.CLIENT)) return;
  st.set({ frontRequest: code, dismissed: st.dismissed.filter((c) => c !== code) });
}

/** How far up the cards sit: over a session's bar at the bottom, when one shows. */
function useAboveBottomBar(): number {
  const [bottom, setBottom] = useState(20);
  useEffect(() => {
    const content = document.querySelector(".content");
    if (!content) return;
    let bar: Element | null = null;
    const sizes = new ResizeObserver(() => measure());
    const measure = () => {
      const now = content.querySelector(".cbar");
      if (now !== bar) {
        if (bar) sizes.unobserve(bar);
        if (now) sizes.observe(now);
        bar = now;
      }
      setBottom(bar ? Math.round(content.getBoundingClientRect().bottom - bar.getBoundingClientRect().top) + 12 : 20);
    };
    const changes = new MutationObserver(measure);
    changes.observe(content, { childList: true, subtree: true });
    measure();
    return () => {
      changes.disconnect();
      sizes.disconnect();
    };
  }, []);
  return bottom;
}
