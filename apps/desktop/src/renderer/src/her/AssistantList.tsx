import { memo, useRef } from "react";
import { useTranslation } from "react-i18next";
import { botLine, botWorking } from "@hermesh/her-client";
import type { BotSummary } from "@hermesh/her-protocol";
import { Icon } from "../icons";
import { useNow, useStore, type Store } from "../store";
import { botContextMenu } from "./actions";
import { NameField } from "./AssistantPanel";
import { BotAvatar } from "./Avatar";
import { useHer, type Her } from "./context";
import { reachable } from "./environments";
import { shortTime } from "./format";

/**
 * An environment's assistants, like a chat app's list: each
 * one's avatar, name, time, last message (or what it does, or what it waits for) and unread count.
 * Pinned ones first, then the latest message first.
 */
export function AssistantList() {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  // The rows act with the store and the page as of a click, so a change elsewhere doesn't render them again.
  const latest = useRef({ st, her });
  latest.current = { st, her };
  return (
    <div className="her-list" role="list" aria-label={t("list.label")}>
      {her.env.bots.map((b) => (
        <BotRow key={b.id} b={b} sel={b.id === her.bot?.id} renaming={her.renaming === b.id} page={latest} />
      ))}
      {!her.env.botsLoaded && her.env.bots.length === 0 && reachable(her.machine) && (
        <div className="loadrow">
          <span className="mini-spin" />
          {t("list.loading")}
        </div>
      )}
    </div>
  );
}

const BotRow = memo(function BotRow({ b, sel, renaming, page }: { b: BotSummary; sel: boolean; renaming: boolean; page: { current: { st: Store; her: Her } } }) {
  const now = useNow(30_000);
  const busy = botWorking(b);
  return (
    <div
      className={`brow ${sel ? "sel" : ""}`}
      role="listitem"
      aria-current={sel || undefined}
      tabIndex={0}
      onClick={() => !renaming && page.current.her.selectBot(b.id)}
      onKeyDown={(e) => !renaming && e.key === "Enter" && page.current.her.selectBot(b.id)}
      onDoubleClick={() => !renaming && page.current.her.setRenaming(b.id)}
      onContextMenu={(e) => {
        e.preventDefault();
        void botContextMenu(page.current.st, page.current.her, b);
      }}
    >
      <BotAvatar bot={b} size={36} busy={busy} />
      <div className="bmain">
        <div className="bl1">
          {renaming ? (
            <NameField
              initial={b.name}
              className="srename"
              end={(name) => {
                const { her } = page.current;
                her.setRenaming(null);
                const v = name?.trim();
                if (v && v !== b.name) void her.run(() => her.call("bots/update", { bot_id: b.id, name: v }));
              }}
            />
          ) : (
            <>
              <span className="bname" title={b.name}>{b.name}</span>
              {b.pinned && <Icon name="pin" className="bpin" />}
              <span className="btime">{b.last ? shortTime(new Date(b.last.ts), now) : ""}</span>
            </>
          )}
        </div>
        <div className="bl2">
          <RowLine b={b} />
          {!sel && (b.unread ?? 0) > 0 && <span className="bbadge">{b.unread}</span>}
        </div>
      </div>
    </div>
  );
});

/** The row's second line: an approval waiting, else that it answers, else its last message. */
function RowLine({ b }: { b: BotSummary }) {
  const { t } = useTranslation("her");
  const line = botLine(b);
  switch (line.kind) {
    case "waiting":
      return (
        <span className="bprev">
          <span className="btag">{t("line.waiting")}</span> {line.text}
        </span>
      );
    case "agents":
    case "typing":
      return <span className="bprev">{t("line.typing")}</span>;
    case "empty":
      return <span className="bprev">{t("line.empty")}</span>;
    case "last": {
      const text = line.text || (line.images > 0 ? t("line.image") : line.files.length ? t("line.file", { name: line.files[0] }) : "");
      return <span className="bprev">{text}</span>;
    }
  }
}
