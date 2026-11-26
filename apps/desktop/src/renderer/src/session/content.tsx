import { useContext, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { MenuItem } from "../../../shared/bridge";
import { i18n } from "../../../shared/i18n";
import { Icon } from "../icons";
import { useStore, type Store } from "../store";
import { fileOfCode, fileOfLink } from "@hermesh/her-client";
import { Code } from "./Code";
import { FileTag } from "./files/FileTag";
import { FilesHomeContext, type FilesHome } from "./files/files";
import { DraftContext, SessionRunContext, type SessionShellRunner } from "./scope";
import {
  cleanShellCommand,
  effectiveCodeLang,
  findSecretSpans,
  hiddenChars,
  maskSecrets,
  runnableShellCommand,
} from "./terminalText";

// What a reply's links, inline code and code blocks show, and their native menus. Nothing here
// reaches the network or reads a file; links open in the default browser, http and https only.

const t = i18n.getFixedT(null, "session");

export const copyText = (text: string) => navigator.clipboard.writeText(text);

function copy(st: Store, text: string, said: string) {
  void copyText(text).then(() => st.show(said));
}

/** The text selected in the conversation (not in a field), to copy first in any menu there. */
function selection(): string {
  const sel = window.getSelection();
  if (!sel || sel.isCollapsed) return "";
  const at = sel.anchorNode instanceof Element ? sel.anchorNode : sel.anchorNode?.parentElement;
  return at?.closest(".dwrap") && !at.closest("[contenteditable], input, textarea") ? sel.toString() : "";
}

/** A menu with Copy first when text is selected; resolves to what was picked ("copySel" is handled here). */
async function popup(st: Store, items: MenuItem[]): Promise<string | null> {
  const sel = selection();
  const id = await window.hermesh?.popupMenu(sel ? [{ id: "copySel", label: t("content.copy") }, ...(items.length ? [{ separator: true } as const] : []), ...items] : items);
  if (id === "copySel") copy(st, sel, t("content.copied"));
  return id ?? null;
}

/** A code block's menu: copy (prompt-stripped for shells), run in session, or copy & open local terminal in the session's worktree. */
async function codeMenu(
  st: Store,
  copyTarget: string,
  shell: boolean,
  runnableCmd: string | null,
  runInSession: SessionShellRunner | null,
  home: FilesHome | null,
) {
  const items: MenuItem[] = [
    { id: "copy", label: t(shell ? "content.copyCommand" : "content.copyCode") },
  ];
  if (runnableCmd && runInSession) {
    items.push({ id: "run", label: t("content.runInSession") });
  }
  if (shell && home?.local && home.session) {
    items.push({ id: "terminal", label: t("content.copyAndOpenTerminal") });
  }
  const id = await popup(st, items);
  if (id === "copy") copy(st, copyTarget, t("content.copied"));
  else if (id === "run" && runnableCmd && runInSession) runInSession(runnableCmd);
  else if (id === "terminal" && home?.session) {
    void copyText(copyTarget).then(() => {
      st.show(t("content.copiedAndOpenedTerminal"));
      void window.hermesh?.openSessionFolder(home.session, "terminal");
    });
  }
}

/** A table's menu: to paste into a sheet or a document, or as the Markdown it was written in. */
export async function tableMenu(st: Store, copyTable: () => void, markdown: string) {
  const id = await popup(st, [
    { id: "table", label: t("markdown.copyTable") },
    { id: "markdown", label: t("markdown.copyAsMarkdown") },
  ]);
  if (id === "table") copyTable();
  else if (id === "markdown") copy(st, markdown, t("content.copied"));
}

const web = /^https?:\/\//i;

/**
 * A Markdown link: a web page (http and https only); in a session's reply, a file of the session as a
 * tag (files/FileTag); else, or while it is still being written, its words alone.
 */
export function Link({ href, children }: { href?: string; children?: ReactNode }) {
  const draft = useContext(DraftContext);
  const files = useContext(FilesHomeContext);
  const file = !draft && files && href ? fileOfLink(href, window.hermesh?.platform === "win32") : null;
  if (file) return <FileTag target={file}>{children}</FileTag>;
  if (!draft && href && web.test(href)) {
    return (
      <a href={href} target="_blank" rel="noreferrer">
        {children}
      </a>
    );
  }
  return <span>{children}</span>;
}

/** Inline code, with a swatch before a colour written alone (`#1F6BFF`); in a session's reply, a path that is there as a file tag. */
export function InlineCode({ children }: { children?: ReactNode }) {
  const draft = useContext(DraftContext);
  const files = useContext(FilesHomeContext);
  const file = !draft && files && typeof children === "string" ? fileOfCode(children, window.hermesh?.platform === "win32") : null;
  if (file && typeof children === "string") return <FileTag target={file} code={children} />;
  const color = typeof children === "string" && /^#(?:[\da-f]{3,4}|[\da-f]{6}|[\da-f]{8})$/i.test(children.trim()) ? children.trim() : null;
  return (
    <code>
      {color && <span className="md-color" aria-hidden="true" style={{ backgroundColor: color }} />}
      {children}
    </code>
  );
}

/**
 * A fenced block: a bar with its language (a shell's with a terminal mark, auto-sniffed when untagged),
 * secret masking toggle, 1-click Run in session workspace, and prompt-clean Copy.
 */
export function CodeBlock({ lang, text }: { lang: string; text: string }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const draft = useContext(DraftContext);
  const home = useContext(FilesHomeContext);
  const runInSession = useContext(SessionRunContext);
  const [done, setDone] = useState(false);
  const [revealed, setRevealed] = useState(false);

  const eff = useMemo(() => effectiveCodeLang(lang, text), [lang, text]);
  const hasSecrets = useMemo(() => findSecretSpans(text).length > 0, [text]);
  const shownText = useMemo(() => (hasSecrets && !revealed ? maskSecrets(text) : text), [text, hasSecrets, revealed]);
  const copyTarget = useMemo(() => (eff.shell ? cleanShellCommand(text) : text), [eff.shell, text]);
  const runnable = useMemo(
    () => (!hasSecrets || revealed ? runnableShellCommand(lang, text) : null),
    [lang, text, hasSecrets, revealed],
  );
  const copyBlock = () => void copyText(copyTarget).then(() => (setDone(true), setTimeout(() => setDone(false), 1500)));

  return (
    <div
      className="cblock"
      onContextMenu={(e) => {
        if (draft) return;
        e.preventDefault();
        e.stopPropagation();
        void codeMenu(st, copyTarget, eff.shell, runnable, runInSession, home);
      }}
    >
      <div className="cb-h">
        {eff.lang && (
          <span className="cb-lang">
            {eff.shell && <Icon name="terminal" />}
            {eff.lang}
          </span>
        )}
        {hiddenChars(text) && (
          <span className="cb-warn" title={t("content.hiddenTip")}>
            <Icon name="warn" />
            {t("content.hidden")}
          </span>
        )}
        {!draft && (
          <span className="cb-acts">
            {hasSecrets && (
              <button
                type="button"
                className="cb-b"
                data-testid="codeblock-secret-toggle"
                onClick={() => setRevealed(!revealed)}
                title={t(revealed ? "content.hideSecrets" : "content.showSecrets")}
                aria-label={t(revealed ? "content.hideSecrets" : "content.showSecrets")}
              >
                <Icon name={revealed ? "eyeOff" : "eye"} />
              </button>
            )}
            {runnable && runInSession && (
              <button
                type="button"
                className="cb-b run"
                data-testid="codeblock-run-btn"
                onClick={() => runInSession(runnable)}
                title={t("content.runTip")}
                aria-label={t("content.run")}
              >
                <Icon name="play" />
                {t("content.run")}
              </button>
            )}
            <button type="button" className="cb-b" onClick={copyBlock} aria-label={t("content.copy")}>
              <Icon name={done ? "check" : "copy"} />
              {t("content.copy")}
            </button>
          </span>
        )}
      </div>
      <pre>
        <code className={eff.lang ? `language-${eff.lang}` : undefined}>
          {eff.lang ? <Code text={shownText} lang={eff.lang} /> : shownText}
        </code>
      </pre>
    </div>
  );
}
