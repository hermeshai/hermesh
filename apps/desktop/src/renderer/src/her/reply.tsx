import { createContext, useContext, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { plain, type ReplyQuote } from "@hermesh/her-client";
import { Icon } from "../icons";

// Replying to a message of the conversation, or to a part of one (docs/her-reply.md): from a message's buttons, or
// from words selected in it. The composer shows what the next message replies to; a message that replied shows it
// at the top of its bubble, and a click there brings the message replied to into view.

/** How much of a message a reply quotes, in characters (the core cuts at the same length). */
const MAX = 2000;

/** The reply to the message `ref`: the words `selected` in it, or all of `whole`. */
export function quoteOf(ref: string, own: boolean, whole: string, selected?: string): ReplyQuote {
  const full = whole.trim();
  let text = (selected ?? full).trim();
  let partial = text !== full;
  const chars = Array.from(text);
  if (chars.length > MAX) {
    text = `${chars.slice(0, MAX).join("").trimEnd()}…`;
    partial = true;
  }
  return { ref, own, text, partial };
}

/** A quote's words as a card shows them: without the marks of the Markdown a whole message is quoted in. */
export const quoteText = (text: string) => text.split("\n").map(plain).filter(Boolean).join("\n");

/**
 * What the conversation on screen does for a reply: `start` puts one in the composer, `show` brings the message
 * replied to into view; `name` is the assistant's.
 */
export const Replying = createContext<{ start: (q: ReplyQuote) => void; show: (ref: string) => void; name: string } | null>(null);

/** Among a message's buttons: reply to all of it. */
export function ReplyButton({ quote }: { quote: () => ReplyQuote }) {
  const { t } = useTranslation("her");
  const replying = useContext(Replying);
  if (!replying) return null;
  return (
    <button className="tb" aria-label={t("reply.reply")} title={t("reply.reply")} onClick={() => replying.start(quote())}>
      <Icon name="reply" className="sm" />
    </button>
  );
}

/** What a message replies to, at the top of its bubble: who said it and the words. A click brings that message into view. */
export function QuoteCard({ reply }: { reply: ReplyQuote }) {
  const { t } = useTranslation("her");
  const replying = useContext(Replying);
  const who = reply.own ? t("reply.you") : (replying?.name ?? "");
  return (
    <button className="rq" data-testid="her-quote" aria-label={`${who}: ${reply.text}`} onClick={() => replying?.show(reply.ref)}>
      <span className="rq-t">
        <b>{who}</b>
        <span>{quoteText(reply.text)}</span>
      </span>
    </button>
  );
}

/** What is selected, when it is words of one message that can be replied to (`[data-reply-key]`), and where. */
function selected(root: HTMLElement): { quote: ReplyQuote; x: number; y: number } | null {
  const sel = getSelection();
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null;
  const within = (n: Node | null) => (n instanceof Element ? n : n?.parentElement) ?? null;
  const from = within(sel.anchorNode), to = within(sel.focusNode);
  const message = from?.closest<HTMLElement>("[data-reply-key]");
  // One message's own words: not what it quotes, its buttons or its answers to tap.
  const words = (el: Element | null) => el?.closest<HTMLElement>(".bub-t, .bbub, .bub") ?? null;
  if (!message || !root.contains(message) || message !== to?.closest("[data-reply-key]") || !words(from) || !words(to)) return null;
  if ([from, to].some((el) => el?.closest(".rq, .msg-act, .bq-opts"))) return null;
  const text = sel.toString().trim();
  const box = sel.getRangeAt(0).getBoundingClientRect(), seen = root.getBoundingClientRect();
  if (!text || box.bottom < seen.top || box.top > seen.bottom) return null;
  const quote = quoteOf(message.dataset.replyKey!, message.dataset.replyOwn !== undefined, words(from)!.innerText, text);
  return { quote, x: Math.min(Math.max(box.left + box.width / 2, 48), innerWidth - 48), y: Math.max(box.top, seen.top) - 38 };
}

/**
 * Reply, over words selected in one message of `root` (the conversation's scroller): it shows once the selection
 * settles, and goes when the selection does, on a scroll, and on a press elsewhere.
 */
export function ReplyPill({ root }: { root: HTMLElement | null }) {
  const { t } = useTranslation("her");
  const replying = useContext(Replying);
  const [at, setAt] = useState<ReturnType<typeof selected>>(null);
  const pressed = useRef(false);
  useEffect(() => {
    if (!root) return;
    let frame = 0;
    const look = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => setAt(pressed.current ? null : selected(root)));
    };
    const down = (e: MouseEvent) => {
      if (e.target instanceof Element && e.target.closest(".reply-pill")) return;
      pressed.current = true;
      setAt(null);
    };
    const up = () => {
      pressed.current = false;
      look();
    };
    const hide = () => setAt(null);
    document.addEventListener("mousedown", down, true);
    document.addEventListener("mouseup", up, true);
    document.addEventListener("selectionchange", look);
    root.addEventListener("scroll", hide, { passive: true });
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener("mousedown", down, true);
      document.removeEventListener("mouseup", up, true);
      document.removeEventListener("selectionchange", look);
      root.removeEventListener("scroll", hide);
    };
  }, [root]);
  if (!at || !replying) return null;
  return createPortal(
    <button
      className="reply-pill"
      data-testid="her-reply-pill"
      style={{ left: at.x, top: Math.max(at.y, 6) }}
      // The press must not take the selection it replies to.
      onMouseDown={(e) => e.preventDefault()}
      onClick={() => {
        getSelection()?.removeAllRanges();
        setAt(null);
        replying.start(at.quote);
      }}
    >
      <Icon name="reply" />
      {t("reply.reply")}
    </button>,
    document.body,
  );
}
