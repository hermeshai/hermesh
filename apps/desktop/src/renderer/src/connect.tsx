import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { PairingKind } from "@hermesh/proto/identity";
import type { PairingDetails } from "@hermesh/proto/local";
import { dateOf, scopeSpec, type ScopeChoice } from "@hermesh/core";
import { kindNamed, personalAgents } from "@hermesh/ui";
import { local } from "./api";
import { connectMessage } from "./personal";
import { useAction, useWaitForPairings } from "./sheets/common";
import { newScope } from "./sheets/Scope";
import { useStore } from "./store";

/**
 * "Connect Your Agent" (the first run's page and the sheet): the agent picked
 * only names it in the message to send; every agent's request that arrives
 * takes the page, whichever is picked, oldest first, each with what it may
 * use. "Connect Another" goes back to the message; a new request comes back.
 */
export function useConnect() {
  const st = useStore();
  const [kindId, setKindId] = useState(personalAgents[0].id);
  const kind = personalAgents.find((k) => k.id === kindId) ?? personalAgents[0];
  const other = kind.id === "other";
  const [otherName, setOtherName] = useState("My Agent");
  const [copied, setCopied] = useState(false);
  const [focus, setFocus] = useState<string>();
  // Requests waiting when "Connect Another" was chosen; undefined when not.
  const [aside, setAside] = useState<string[]>();
  // Approved here: gone from the list at the next refresh.
  const [answered, setAnswered] = useState<string[]>([]);
  const [held, setHeld] = useState<PairingDetails>();
  const [scopes, setScopes] = useState<Record<string, ScopeChoice>>({});
  const act = useAction();
  const label = (other ? otherName.trim() : kind.name) || "My Agent";

  const requests = st.pairings
    .filter((p) => p.kind === PairingKind.CLIENT && !answered.includes(p.code))
    .sort((a, b) => (dateOf(a.expiresAt)?.getTime() ?? 0) - (dateOf(b.expiresAt)?.getTime() ?? 0));
  const fresh = requests.filter((p) => !aside?.includes(p.code));
  const request = held ?? (fresh.length ? (fresh.find((p) => p.code === focus) ?? fresh[0]) : undefined);
  const show = (code: string) => {
    setAside(undefined);
    setFocus(code);
  };

  // A notification's request: shown here (openPairing leaves it to this page).
  useWaitForPairings();
  useEffect(() => window.hermesh?.onCommand((c) => c.startsWith("pairing:") && show(c.slice("pairing:".length))), []);

  return {
    kind,
    other,
    otherName,
    label,
    copied,
    message: connectMessage(st.status, label),
    pick(id: string) {
      setKindId(id);
      setCopied(false);
    },
    rename(name: string) {
      setOtherName(name);
      setCopied(false);
    },
    copy() {
      navigator.clipboard.writeText(connectMessage(st.status, label)).catch(() => {});
      setCopied(true);
    },
    request,
    /** The request's own kind: its icon. */
    requestKind: request && (kindNamed(request.label) ?? personalAgents[personalAgents.length - 1]),
    /** The other requests waiting. */
    waiting: requests.filter((p) => p.code !== request?.code),
    show,
    another() {
      setAside(requests.map((p) => p.code));
    },
    scope: request ? (scopes[request.code] ?? newScope(st)) : newScope(st),
    setScope(s: ScopeChoice) {
      if (request) setScopes({ ...scopes, [request.code]: s });
    },
    act,
    /** Approves the request; true when it was. */
    async approve(p: PairingDetails): Promise<boolean> {
      setHeld(p);
      const spec = scopeSpec(scopes[p.code] ?? newScope(st));
      spec.label = p.label;
      const ok = await act.go(() => local.approveClientPairing({ code: p.code, spec }));
      if (ok) {
        setAnswered((a) => [...a, p.code]);
        setAside(undefined);
        st.refresh();
      }
      setHeld(undefined); // approved: left out from now on; rejected or failed: it may still be waiting
      return ok;
    },
    deny(p: PairingDetails, toast: string) {
      st.set({ dismissed: [...st.dismissed, p.code] });
      st.show(toast);
    },
  };
}

/** The other requests waiting, each a button to it; and, beside a request, "Connect Another". */
export function Waiting({ c, another }: { c: ReturnType<typeof useConnect>; another?: boolean }) {
  const { t } = useTranslation("sheets");
  if (!c.waiting.length && !another) return null;
  return (
    <div className="pa-queue">
      {c.waiting.length > 0 && (
        <span>
          {t("newGrant.waitingFor")}
          {c.waiting.map((p) => (
            <button key={p.code} className="link" onClick={() => c.show(p.code)}>
              {p.label}
            </button>
          ))}
        </span>
      )}
      {another && (
        <button className="link end" onClick={c.another}>
          {t("newGrant.another")}
        </button>
      )}
    </div>
  );
}
