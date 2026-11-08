import { catchRedirect, caught, type Caught } from "./routes.ts";

/** A sign-in her started: where the browser goes, and the id that finishes or cancels it. */
export type Started = { authorize_url: string; login_id: string };

/** What the redirect carried when the provider let the person in. */
export type Granted = Extract<Caught, { code: string }>;

export type SignIn<T> = {
  /** The catcher's path on the owner's device. */
  path: string;
  /** Asks her to start signing in, the browser coming back to `redirect_uri`. */
  start: (redirect_uri: string) => Promise<Started>;
  /** Opens the authorization page. */
  open: (url: string) => void;
  /** Hands her what the redirect carried; her exchanges it and keeps the tokens. */
  complete: (login_id: string, granted: Granted) => Promise<T>;
  /** Ends the sign-in her keeps waiting. */
  cancel: (login_id: string) => Promise<unknown>;
  signal?: AbortSignal;
};

/**
 * A sign-in that ends on the owner's device (docs/her-server.md §5.4): a catcher there takes
 * the browser's redirect; her makes the authorization request for it and keeps the PKCE
 * verifier; the browser opens it; what the redirect carried goes back to her. The provider's
 * refusal, or stopping it here, cancels her's side too.
 */
export async function signInRemotely<T>(s: SignIn<T>): Promise<T> {
  const catcher = await catchRedirect(s.path);
  const started = await s.start(catcher.redirect_uri);
  let got: Caught;
  try {
    s.signal?.throwIfAborted();
    s.open(started.authorize_url);
    got = await caught(catcher.id, s.signal);
  } catch (e) {
    await s.cancel(started.login_id).catch(() => {});
    throw e;
  }
  if ("error" in got) {
    await s.cancel(started.login_id).catch(() => {});
    throw new Error(got.error_description || got.error);
  }
  return s.complete(started.login_id, got);
}

/** Opens a page in a new tab or, in the app, in the browser. */
export function openPage(url: string) {
  window.open(url, "_blank", "noopener");
}
