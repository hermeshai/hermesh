// Pure view-model for the sidebar's single-line account footer and its hover card.

export type AccountFootStatus = {
  initialized?: boolean;
  machineOnly?: boolean;
  signedOut?: boolean;
  hosted?: boolean;
  account?: string;
  accountAvatarUrl?: string;
  relayUrl?: string;
  relayConnected?: boolean;
  relayHealth?: { phase?: string };
};

export type AccountLinkState = "connected" | "reconnecting" | "offline" | "local";

export type AccountFootModel = {
  signedIn: boolean;
  hosted: boolean;
  /** Short label for the single-line footer: email prefix before @ on hosted accounts, else local username. */
  displayName: string;
  /** Full account email on hosted accounts, empty otherwise. */
  email: string;
  /** Validated https avatar URL on hosted accounts, empty otherwise. */
  avatarUrl: string;
  /** Single-character fallback initial. */
  initial: string;
  /** Hostname (and port if present) parsed from relayUrl when signed in. */
  serverHost: string;
  linkState: AccountLinkState;
};

/** Prefix before `@` for an email address; returns the trimmed string as-is when there is no `@`. */
export function emailPrefix(account: string): string {
  const s = account.trim();
  const at = s.indexOf("@");
  return at > 0 ? s.slice(0, at) : s;
}

function safeHttpsUrl(raw: string | undefined): string {
  const s = (raw ?? "").trim();
  if (!s || !URL.canParse(s)) return "";
  return new URL(s).protocol === "https:" ? s : "";
}

function hostOf(raw: string | undefined): string {
  const s = (raw ?? "").trim();
  if (!s) return "";
  return URL.canParse(s) ? new URL(s).host : s;
}

export function accountFootModel(status: AccountFootStatus | null | undefined, userName?: string): AccountFootModel {
  const signedIn = !!(status && (status.initialized || status.machineOnly) && !status.signedOut);
  const hosted = signedIn && status?.hosted === true;
  const localUser = (userName ?? "").trim();
  const email = hosted ? (status?.account ?? "").trim() : "";
  const displayName = !signedIn ? "" : email ? emailPrefix(email) : localUser || "Hermesh";
  const avatarUrl = hosted ? safeHttpsUrl(status?.accountAvatarUrl) : "";
  const initial = (displayName || localUser || "H").slice(0, 1).toUpperCase();
  const serverHost = signedIn ? hostOf(status?.relayUrl) : "";

  let linkState: AccountLinkState = "local";
  if (signedIn) {
    if (status?.relayConnected) linkState = "connected";
    else if (status?.relayHealth?.phase === "connecting" || status?.relayHealth?.phase === "reconnecting") linkState = "reconnecting";
    else linkState = "offline";
  }

  return { signedIn, hosted, displayName, email, avatarUrl, initial, serverHost, linkState };
}
