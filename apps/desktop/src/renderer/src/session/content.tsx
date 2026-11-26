import { useContext, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { MenuItem } from "../../../shared/bridge";
import { i18n } from "../../../shared/i18n";
import { Icon } from "../icons";
import { useStore, type Store } from "../store";
import { fileOfCode, fileOfLink } from "@hermesh/her-client";
import { Code, PlainWipeCode } from "./Code";
import {
  markInlineRunSent,
  startInlineRun,
  stopInlineRun,
  toggleInlineRunFold,
  useInlineRun,
  type InlineRunState,
} from "./codeBlockRuns";
import { FileTag } from "./files/FileTag";
import { FilesHomeContext, type FilesHome } from "./files/files";
import { useWipeLines } from "./markdown/stream";
import { DraftContext, SessionRunContext, StreamContext, type SessionCodeActions } from "./scope";
import {
  cleanShellCommand,
  effectiveCodeLang,
  findSecretSpans,
  hiddenChars,
  inspectRunnableShell,
  maskSecrets,
  parseTerminalLines,
  stripTerminalEscapes,
  type AnsiSpanStyle,
  type RunnableShellPlan,
} from "./terminalText";

// What a reply's links, inline code and code blocks show, and their native menus. Nothing here
// reaches the network or reads a file directly; links open in the default browser, http and https only.

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

/** A code block's context menu: copy (prompt-stripped for shells), run in place, or copy & open local terminal. */
async function codeMenu(
  st: Store,
  copyTarget: string,
  shell: boolean,
  plan: RunnableShellPlan | null,
  onTriggerRun: (() => void) | null,
  home: FilesHome | null,
) {
  const items: MenuItem[] = [
    { id: "copy", label: t(shell ? "content.copyCommand" : "content.copyCode") },
  ];
  if (plan && onTriggerRun) {
    items.push({ id: "run", label: t("content.runInSession") });
  }
  if (shell && home?.local && home.session) {
    items.push({ id: "terminal", label: t("content.copyAndOpenTerminal") });
  }
  const id = await popup(st, items);
  if (id === "copy") copy(st, copyTarget, t("content.copied"));
  else if (id === "run" && onTriggerRun) onTriggerRun();
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

function formatSec(ms: number): string {
  const s = Math.max(0, ms) / 1000;
  return s < 10 ? `${s.toFixed(1)}s` : `${Math.round(s)}s`;
}

function spanClass(s: AnsiSpanStyle | undefined): string | undefined {
  if (!s) return undefined;
  const cls: string[] = [];
  if (s.fgClass) cls.push(s.fgClass);
  if (s.bold) cls.push("ansi-b");
  if (s.dim) cls.push("ansi-d");
  if (s.italic) cls.push("ansi-i");
  if (s.underline) cls.push("ansi-u");
  return cls.length > 0 ? cls.join(" ") : undefined;
}

function useRunningElapsed(startedAt: number, running: boolean): string {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!running) return;
    setNow(Date.now());
    const id = setInterval(() => setNow(Date.now()), 100);
    return () => clearInterval(id);
  }, [running, startedAt]);
  return formatSec(now - startedAt);
}

/**
 * Attached bottom terminal output window inside a CodeBlock:
 * - Left status area (`▾ ● 退出码 0 · 0.1s`) folds/unfolds the output body;
 * - Right actions hold only `↑ 发入对话` (Send to Chat) and an icon-only Copy Output button.
 */
function InlineTerminalDrawer({
  run,
  elapsed,
  actions,
  blockText,
}: {
  run: InlineRunState;
  elapsed: string;
  actions: SessionCodeActions;
  blockText: string;
}) {
  const { t } = useTranslation("session");
  const bodyRef = useRef<HTMLPreElement>(null);
  const stickRef = useRef(true);
  const [copied, setCopied] = useState(false);

  const isRunning = run.status === "running";
  const lines = useMemo(() => (run.output ? parseTerminalLines(run.output) : []), [run.output]);

  useLayoutEffect(() => {
    const el = bodyRef.current;
    if (el && isRunning && stickRef.current) {
      el.scrollTop = el.scrollHeight;
    }
  }, [run.output, isRunning]);

  const durText = isRunning ? elapsed : formatSec(run.durationMs);
  let dotCls = "running";
  let statusCls = "";
  let statusLabel = t("content.termRunning", { time: durText });

  if (run.status === "stopped") {
    dotCls = "stop";
    statusCls = "stop";
    statusLabel = t("content.termStopped", { time: durText });
  } else if (run.status === "error") {
    dotCls = "err";
    statusCls = "err";
    statusLabel = run.errorMessage || t("content.termFailed");
  } else if (run.status === "done") {
    if (run.timedOut) {
      dotCls = "err";
      statusCls = "err";
      statusLabel = t("content.termTimedOut", { time: durText });
    } else if (run.exitCode === 0) {
      dotCls = "ok";
      statusCls = "ok";
      statusLabel = t("content.termExitOk", { time: durText });
    } else {
      dotCls = "err";
      statusCls = "err";
      statusLabel = t("content.termExitErr", { code: run.exitCode, time: durText });
    }
  }

  const canSend = run.status === "done" || run.status === "stopped";
  const copyOut = () => {
    const plain = stripTerminalEscapes(run.output || run.errorMessage || "");
    void copyText(plain).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <div className={`cb-term ${run.folded ? "folded" : ""}`} data-testid="codeblock-terminal-drawer">
      <div className="cb-term-bar">
        <button
          type="button"
          className="term-toggle"
          data-testid="codeblock-term-toggle"
          aria-expanded={!run.folded}
          onClick={() => toggleInlineRunFold(actions.sessionId, blockText)}
          title={t(run.folded ? "content.expandOutput" : "content.collapseOutput")}
        >
          <Icon name="caret" className="term-chev" />
          <span className={`status-dot ${dotCls}`} />
          <span className={`term-exit ${statusCls}`}>{statusLabel}</span>
        </button>
        <span className="term-acts">
          {canSend && (
            <button
              type="button"
              className={`term-ask ${run.sentToChat ? "sent" : ""}`}
              data-testid="codeblock-send-to-chat"
              disabled={run.sentToChat}
              onClick={() => {
                markInlineRunSent(actions.sessionId, blockText);
                actions.sendOutputToChat(run.command, run.exitCode, run.output, run.status === "stopped");
              }}
            >
              <Icon name={run.sentToChat ? "check" : "arrowUp"} />
              <span>{t(run.sentToChat ? "content.sentToChat" : "content.sendToChat")}</span>
            </button>
          )}
          <button
            type="button"
            className={`term-icon-btn ${copied ? "copied" : ""}`}
            data-testid="codeblock-copy-output"
            onClick={copyOut}
            title={t("content.copyOutput")}
            aria-label={t("content.copyOutput")}
          >
            <Icon name={copied ? "check" : "copy"} />
          </button>
        </span>
      </div>
      {!run.folded && (
        <pre
          ref={bodyRef}
          className="cb-term-body sel-text"
          data-testid="codeblock-term-body"
          onScroll={(e) => {
            const el = e.currentTarget;
            stickRef.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 40;
          }}
        >
          {lines.length > 0 ? (
            lines.map((line, i) => (
              <span key={i} className={line.diff ? `tline diff-${line.diff}` : "tline"}>
                {i > 0 && "\n"}
                {line.spans.map((sp, j) => {
                  const cls = spanClass(sp.style);
                  const color = sp.style?.fgColor;
                  return cls || color ? (
                    <span key={j} className={cls} style={color ? { color } : undefined}>
                      {sp.text}
                    </span>
                  ) : (
                    sp.text
                  );
                })}
              </span>
            ))
          ) : (
            <span className="ansi-gray">
              {isRunning ? t("content.waitingOutput") : run.errorMessage || t("content.noOutput")}
            </span>
          )}
        </pre>
      )}
    </div>
  );
}

/**
 * A fenced block: keeps the original code/script intact on top, morphs Run -> Spinner/Stop -> Rerun,
 * guards destructive scripts with an inline confirmation strip, and opens an attached terminal output drawer below.
 */
export function CodeBlock({ lang, text }: { lang: string; text: string }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const draft = useContext(DraftContext);
  const home = useContext(FilesHomeContext);
  const actions = useContext(SessionRunContext);
  const animate = useContext(StreamContext);
  const lineCount = useMemoCount(text);
  const wipeFrom = useWipeLines(lineCount, animate);
  const [done, setDone] = useState(false);
  const [revealed, setRevealed] = useState(false);
  const [confirming, setConfirming] = useState(false);

  const eff = useMemo(() => effectiveCodeLang(lang, text), [lang, text]);
  const hasSecrets = useMemo(() => findSecretSpans(text).length > 0, [text]);
  const shownText = useMemo(() => (hasSecrets && !revealed ? maskSecrets(text) : text), [text, hasSecrets, revealed]);
  const copyTarget = useMemo(() => (eff.shell ? cleanShellCommand(text) : text), [eff.shell, text]);
  const plan = useMemo(
    () => (!hasSecrets || revealed ? inspectRunnableShell(lang, text) : null),
    [lang, text, hasSecrets, revealed],
  );

  const runState = useInlineRun(actions?.sessionId ?? "", text);
  const isRunning = runState?.status === "running";
  const elapsed = useRunningElapsed(runState?.startedAt ?? 0, isRunning);

  const triggerRun = () => {
    if (!actions || !plan) return;
    if (plan.destructive) {
      setConfirming(true);
      return;
    }
    setConfirming(false);
    startInlineRun(actions.sessionId, text, plan.command);
  };

  const copyBlock = () => void copyText(copyTarget).then(() => (setDone(true), setTimeout(() => setDone(false), 1500)));

  return (
    <div
      className={`cblock ${isRunning ? "is-running" : ""}`}
      onContextMenu={(e) => {
        if (draft) return;
        e.preventDefault();
        e.stopPropagation();
        void codeMenu(st, copyTarget, eff.shell, plan, actions && plan ? triggerRun : null, home);
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
            {actions && isRunning ? (
              <button
                type="button"
                className="cb-b running"
                data-testid="codeblock-stop-btn"
                onClick={() => stopInlineRun(actions.sessionId, text)}
                title={t("content.stop")}
                aria-label={t("content.stop")}
              >
                <span className="spin-view">
                  <span className="cb-ring" aria-hidden="true" />
                  <span>{elapsed}</span>
                </span>
                <span className="stop-view">
                  <Icon name="stop" />
                  <span>{t("content.stop")}</span>
                </span>
              </button>
            ) : (
              actions &&
              plan && (
                <button
                  type="button"
                  className={`cb-b ${plan.destructive && !runState ? "warn" : "run"}`}
                  data-testid="codeblock-run-btn"
                  onClick={triggerRun}
                  title={t(plan.destructive ? "content.runConfirmTip" : "content.runTip")}
                  aria-label={t(runState ? "content.rerun" : "content.run")}
                >
                  <Icon name={runState ? "retry" : "play"} />
                  <span>{t(runState ? "content.rerun" : plan.destructive ? "content.runConfirm" : "content.run")}</span>
                </button>
              )
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
          {eff.lang ? <Code text={shownText} lang={eff.lang} wipeFrom={wipeFrom} /> : wipeFrom < lineCount ? <PlainWipeCode text={shownText} wipeFrom={wipeFrom} /> : shownText}
        </code>
      </pre>
      {confirming && actions && plan && (
        <div className="cb-confirm" data-testid="codeblock-confirm-bar">
          <span>{t("content.confirmDestructive")}</span>
          <span className="cb-confirm-acts">
            <button type="button" className="cb-confirm-cancel" onClick={() => setConfirming(false)}>
              {t("content.cancel")}
            </button>
            <button
              type="button"
              className="cb-confirm-go"
              data-testid="codeblock-confirm-run"
              onClick={() => {
                setConfirming(false);
                startInlineRun(actions.sessionId, text, plan.command);
              }}
            >
              {t("content.confirmRun")}
            </button>
          </span>
        </div>
      )}
      {runState && actions && (
        <InlineTerminalDrawer run={runState} elapsed={elapsed} actions={actions} blockText={text} />
      )}
    </div>
  );
}

function useMemoCount(text: string): number {
  let n = 1;
  for (let i = 0; i < text.length; i++) if (text.charCodeAt(i) === 10) n++;
  return n;
}
