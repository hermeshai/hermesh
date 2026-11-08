import { useContext, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { MenuItem } from "../../../shared/bridge";
import { i18n } from "../../../shared/i18n";
import { Icon } from "../icons";
import { useStore, type Store } from "../store";
import { Code } from "./Code";
import { DraftContext } from "./scope";

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

/** A code block's menu: its code, or its command for a shell's. */
async function codeMenu(st: Store, text: string, shell: boolean) {
  const id = await popup(st, [{ id: "copy", label: t(shell ? "content.copyCommand" : "content.copyCode") }]);
  if (id === "copy") copy(st, text, t("content.copied"));
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

const shells = new Set(["bash", "sh", "zsh", "shell", "shellscript", "console", "terminal", "fish", "ksh", "csh", "tcsh", "powershell", "pwsh", "ps1", "cmd", "bat", "batch"]);

/** Whether a fence's language is a shell's: a block of commands to run, not code to read. */
function shellLanguage(lang: string): boolean {
  return shells.has(lang.toLowerCase());
}

/**
 * Whether text holds characters that do not show but change what runs or reads: controls
 * (but tab and newline), zero-width ones and bidirectional overrides.
 */
function hiddenChars(text: string): boolean {
  return /[\u0000-\u0008\u000B-\u001F\u007F​-‏‪-‮⁠-⁤⁦-⁩﻿]/.test(text);
}

const web = /^https?:\/\//i;

/** A Markdown link: a web page (http and https only), else, or while it is still being written, its words alone. */
export function Link({ href, children }: { href?: string; children?: ReactNode }) {
  const draft = useContext(DraftContext);
  if (!draft && href && web.test(href)) {
    return (
      <a href={href} target="_blank" rel="noreferrer">
        {children}
      </a>
    );
  }
  return <span>{children}</span>;
}

/** Inline code, with a swatch before a colour written alone (`#1F6BFF`). */
export function InlineCode({ children }: { children?: ReactNode }) {
  const color = typeof children === "string" && /^#(?:[\da-f]{3,4}|[\da-f]{6}|[\da-f]{8})$/i.test(children.trim()) ? children.trim() : null;
  return (
    <code>
      {color && <span className="md-color" aria-hidden="true" style={{ backgroundColor: color }} />}
      {children}
    </code>
  );
}

/**
 * A fenced block: a bar with its language (a shell's with a terminal mark) and Copy; a warning
 * when it holds characters that do not show. The block still being written has no buttons yet.
 */
export function CodeBlock({ lang, text }: { lang: string; text: string }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const draft = useContext(DraftContext);
  const [done, setDone] = useState(false);
  const shell = shellLanguage(lang);
  const copyBlock = () => void copyText(text).then(() => (setDone(true), setTimeout(() => setDone(false), 1500)));
  return (
    <div
      className="cblock"
      onContextMenu={(e) => {
        if (draft) return;
        e.preventDefault();
        e.stopPropagation();
        void codeMenu(st, text, shell);
      }}
    >
      <div className="cb-h">
        {lang && (
          <span className="cb-lang">
            {shell && <Icon name="terminal" />}
            {lang}
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
            <button className="cb-b" onClick={copyBlock} aria-label={t("content.copy")}>
              <Icon name={done ? "check" : "copy"} />
              {t("content.copy")}
            </button>
          </span>
        )}
      </div>
      <pre>
        <code className={lang ? `language-${lang}` : undefined}>{lang ? <Code text={text} lang={lang} /> : text}</code>
      </pre>
    </div>
  );
}
