import { SessionStatus, type Session } from "@hermesh/proto/session";
import { codingAgentShort, codingAgentTitle, isActive, isFailed, statusTitle } from "@hermesh/core";
import { AgentLogo, AgentTile, grantColor, kindNamed, type PersonalAgentKind } from "@hermesh/ui";
import { useTranslation } from "react-i18next";
import { Icon } from "./icons";

/** The session's state as a small dot: orange waits on you, red failed, green runs. */
export function StatusDot({ s }: { s: Session }) {
  const { t } = useTranslation("common");
  const c =
    s.status === SessionStatus.WAITING_INPUT ? "w"
    : isFailed(s) ? "c"
    : s.status === SessionStatus.RUNNING ? "r"
    : isActive(s) ? "p"
    : s.status === SessionStatus.ARCHIVED ? "a"
    : "i";
  return <span className={`sdot ${c}`} role="img" aria-label={isFailed(s) && s.status !== SessionStatus.CRASHED ? t("status.failed") : statusTitle(s.status)} />;
}

export function GrantAvatar({ label, size = "" }: { label: string; size?: "" | "md" | "lg" | "xl" | "sm" }) {
  const style = size === "sm" ? { width: 16, height: 16, fontSize: 9 } : undefined;
  const logo = kindNamed(label)?.logo;
  if (logo) return <AgentTile agent={logo} size={{ sm: 16, md: 28, lg: 40, xl: 52, "": 22 }[size]} />;
  return (
    <span className={`av ${size === "sm" ? "" : size}`} style={{ background: grantColor(label), ...style }} aria-hidden="true">
      {label.slice(0, 1).toUpperCase()}
    </span>
  );
}

/**
 * A code compared with what the other side shows, as on the signer's panel:
 * a tile per group on a pale aurora block, what it is under its left end and
 * what to do under its right end.
 */
export function CodeTiles({ code, label: given, hint }: { code: string; label?: string; hint: string }) {
  const { t } = useTranslation("common");
  const label = given ?? t("codeTiles.label");
  return (
    <div className="ctiles" role="group" aria-label={label} data-code={code}>
      <div className="ctiles-row">
        {code.split("-").map((g, i) => (
          <span key={i}>{g}</span>
        ))}
      </div>
      <div className="ctiles-cap">
        <span>{label}</span>
        <b>{hint}</b>
      </div>
    </div>
  );
}

/** A personal agent kind's icon: its app's, or for Other Agent a plain tile. */
export function PersonalAgentIcon({ kind, size }: { kind: PersonalAgentKind; size: number }) {
  if (kind.logo) return <AgentTile agent={kind.logo} size={size} />;
  return (
    <span className="pa-icon other" style={{ width: size, height: size }} aria-hidden="true">
      <i>
        <Icon name="terminal" />
      </i>
    </span>
  );
}

/** A coding agent: its logo and (short or full) name. */
export function AgentName({ agent, full }: { agent: string; full?: boolean }) {
  return (
    <span className="agc">
      <AgentLogo agent={agent} size={15} />
      {full ? codingAgentTitle(agent) : codingAgentShort(agent)}
    </span>
  );
}
