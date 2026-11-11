import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { SessionView } from "@hermesh/her-client";
import type { AvatarHue, AvatarInput, BotMemoryResult, BotSummary, PermissionMode } from "@hermesh/her-protocol";
import { local } from "../api";
import { Icon } from "../icons";
import { useStore } from "../store";
import type { SendOp } from "./Approval";
import { BotAvatar } from "./Avatar";
import { useHer } from "./context";
import { permissionDesc, permissionTitle } from "./format";
import { GrantLine } from "./GrantLine";
import { isEmpty, MemoryPage, useMemory } from "./Memory";
import { MenuButton, MenuItem, ModelRows } from "./menus";
import type { FileScope } from "./Messages";
import { AvatarChoices, NAME_MAX, pickPicture } from "./sheets";

const modes: PermissionMode[] = ["manual", "accept-edits", "bypass-permissions"];

/**
 * Her's settings, beside the conversation: its avatar and
 * name, changed where they are; the model it answers with, from those its environment can use, and how
 * hard it thinks; what it may do without asking (full access is signed, docs/her-server.md §8); whether
 * it alerts; what it remembers (docs/her-memory.md), on a page of its own; clearing its chat or its memory;
 * turning Her off on its machine (docs/her-server.md §5.6).
 * What the whole environment does is in its console.
 */
export function AssistantPanel({ bot, view, op, files, onClose }: { bot: BotSummary; view: SessionView | null; op: SendOp; files: FileScope; onClose: () => void }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const memoryShown = her.panel?.kind === "settings" && her.panel.page === "memory";
  const { memory, reload } = useMemory(bot);
  // Clearing the chat is asked first, and what stays is said (bot-mode-design §6.5.1).
  const clear = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("clear.title", { name: bot.name }),
        message: t("clear.message"),
        button: t("clear.button"),
        run: () => her.call("bots/clear", { bot_id: bot.id }),
        done: t("clear.done"),
      },
    });
  // The memory goes to the trash, and the assistant stops remembering at once; the chat stays.
  const clearMemory = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("memoryClear.title", { name: bot.name }),
        message: t("memoryClear.message"),
        button: t("memoryClear.button"),
        run: () => her.call("bots/memory_clear", { bot_id: bot.id }).then(reload),
        done: t("memoryClear.done"),
      },
    });
  if (memoryShown) return <MemoryPage bot={bot} files={files} onBack={() => her.setPanel({ kind: "settings" })} onClose={onClose} />;
  return (
    <aside className="her-panel bpn" aria-label={t("head.settings")}>
      <div className="her-panel-top drag">
        <button className="tb" title={t("panel.close")} aria-label={t("panel.close")} onClick={onClose}>
          <Icon name="x" />
        </button>
      </div>
      <div className="her-panel-body">
        <BotHero key={bot.id} bot={bot} />
        <div className="form">
          {view && <ModelSection view={view} op={op} />}
          {view && <PermissionSection bot={bot} mode={view.info.permission_mode} />}
          <section>
            <h3 className="fhead">{t("panel.notifications")}</h3>
            <div className="fgroup">
              <div className="frow">
                <span className="fl">{t("panel.muted")}</span>
                <div className="fv">
                  <button
                    className={`sw ${bot.muted ? "on" : ""}`}
                    role="switch"
                    aria-checked={!!bot.muted}
                    aria-label={t("panel.muted")}
                    onClick={() => void her.run(() => her.call("bots/update", { bot_id: bot.id, muted: !bot.muted }))}
                  />
                </div>
              </div>
            </div>
            <p className="set-note">{t("panel.mutedNote")}</p>
          </section>
          <MemorySection memory={memory} />
          <section className="fgroup">
            <button className="frow bpn-end" onClick={clear}>
              {t("panel.clear")}
            </button>
            <button className="frow bpn-end" onClick={clearMemory}>
              {t("panel.clearMemory")}
            </button>
          </section>
          <section className="fgroup">
            <button className="frow bpn-end" onClick={() => st.set({ sheet: { kind: "herOff", machineId: her.machine.machineId, machineName: her.machine.machineName } })}>
              {t("panel.turnOff", { machine: her.machine.machineName })}
            </button>
          </section>
        </div>
      </div>
    </aside>
  );
}

/** What it remembers: one row to its page, which says only when there is nothing yet. */
function MemorySection({ memory }: { memory: BotMemoryResult | null }) {
  const { t } = useTranslation("her");
  const her = useHer();
  return (
    <section>
      <h3 className="fhead">{t("panel.memory")}</h3>
      <div className="fgroup">
        <button className="frow bpn-go" onClick={() => her.setPanel({ kind: "settings", page: "memory" })}>
          <span className="fl">{t("panel.memoryRow")}</span>
          <span className="fv">
            {memory && isEmpty(memory) && t("panel.memoryNone")}
            <Icon name="chevR" />
          </span>
        </button>
      </div>
      <p className="set-note">{t("panel.memoryNote")}</p>
    </section>
  );
}

/** Its avatar, which on hover says it can be changed: a click opens the characters and colours in place, and a choice applies at once. */
function BotHero({ bot }: { bot: BotSummary }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const [picking, setPicking] = useState(false);
  const spec = bot.avatar;
  const hue: AvatarHue = spec.kind === "char" || spec.kind === "mono" ? spec.hue : "slate";
  const save = (avatar: AvatarInput) => her.run(() => her.call("bots/update", { bot_id: bot.id, avatar }));
  return (
    <div className="bpn-hero">
      <button className="bpn-av" title={t("panel.avatar")} aria-label={t("panel.avatar")} aria-expanded={picking} onClick={() => setPicking(!picking)}>
        <BotAvatar bot={bot} size={72} />
        <span className="bpn-av-edit" aria-hidden="true">
          <Icon name="edit" />
        </span>
      </button>
      <BotName bot={bot} />
      {picking && (
        <div className="fgroup bpn-pick">
          <AvatarChoices avatar={spec} hue={hue} name={bot.name} mark={bot.default} onChoose={(a) => void save(a)} />
          <button
            className="link"
            onClick={async () => {
              const got = await pickPicture();
              if (!got) return;
              if ("error" in got) return st.show(t(got.error === "too_large" ? "sheet.tooLarge" : "sheet.unreadable"));
              await save({ kind: "image", png: got.png });
            }}
          >
            {t("sheet.upload")}
          </button>
        </div>
      )}
    </div>
  );
}

/** Its name; a double click makes it a field. ↩ or leaving it keeps what is written, Esc what was there. */
function BotName({ bot }: { bot: BotSummary }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const [editing, setEditing] = useState(false);
  if (editing) {
    return (
      <NameField
        initial={bot.name}
        className="bpn-rename"
        end={(name) => {
          setEditing(false);
          const v = name?.trim();
          if (v && v !== bot.name) void her.run(() => her.call("bots/update", { bot_id: bot.id, name: v }));
        }}
      />
    );
  }
  return (
    <h3 className="bpn-name" title={t("panel.nameHint")} onDoubleClick={() => setEditing(true)}>
      {bot.name}
    </h3>
  );
}

/** A name edited in place: ↩ or leaving keeps what is written (end gets it), Esc puts it back (end gets null). */
function NameField({ initial, className, end }: { initial: string; className: string; end: (name: string | null) => void }) {
  const { t } = useTranslation("her");
  const [value, setValue] = useState(initial);
  const done = useRef(false);
  const finish = (keep: boolean) => {
    if (done.current) return;
    done.current = true;
    end(keep ? value : null);
  };
  return (
    <input
      className={className}
      aria-label={t("menu.rename")}
      autoFocus
      maxLength={NAME_MAX}
      value={value}
      spellCheck={false}
      onFocus={(e) => e.currentTarget.select()}
      onChange={(e) => setValue(e.target.value)}
      onClick={(e) => e.stopPropagation()}
      onDoubleClick={(e) => e.stopPropagation()}
      onKeyDown={(e) => {
        e.stopPropagation();
        if (e.nativeEvent.isComposing) return;
        if (e.key === "Enter") finish(true);
        else if (e.key === "Escape") finish(false);
      }}
      onBlur={() => finish(true)}
    />
  );
}

/** The model this assistant answers with and how hard it thinks: its conversation's own. */
function ModelSection({ view, op }: { view: SessionView; op: SendOp }) {
  const { t } = useTranslation("her");
  const { model, thinking } = view.info;
  return (
    <section>
      <h3 className="fhead">{t("panel.model")}</h3>
      <ModelRows below model={model} thinking={thinking} onModel={(ref) => void op({ type: "set_model", model: ref })} onThinking={(l) => void op({ type: "set_model", model, thinking: l })} />
      <p className="set-note">{t("panel.modelNote")}</p>
    </section>
  );
}

/**
 * What this assistant may do without asking. Asking more is set at once; full access asks first
 * (AccessSheet). What it starts follows it, and the note says so.
 */
function PermissionSection({ bot, mode }: { bot: BotSummary; mode: PermissionMode }) {
  const { t } = useTranslation("her");
  const her = useHer();
  const choose = (m: PermissionMode) => {
    if (m === mode) return;
    if (m === "bypass-permissions") return her.setSheet({ kind: "access", bot });
    void her.run(() => local.setHerAssistantMode({ machineId: her.machine.machineId, botId: bot.id, permissionMode: m }));
  };
  return (
    <section className="bpn-perm">
      <h3 className="fhead">{t("panel.permission")}</h3>
      <div className="fgroup">
        <div className="frow">
          <span className="fl">{t("panel.permissionMode")}</span>
          <div className="fv">
            <MenuButton className="mb" align="right" below title={t("panel.permissionMode")} label={<><span className="lbl strong">{permissionTitle(mode)}</span><Icon name="chevD" /></>}>
              {(close) =>
                modes.map((m) => <MenuItem key={m} checked={m === mode} label={permissionTitle(m)} desc={permissionDesc(m)} onClick={() => (close(), choose(m))} />)
              }
            </MenuButton>
          </div>
        </div>
      </div>
      <p className="set-note">{t("panel.permissionNote", { desc: permissionDesc(mode) })}</p>
      <GrantLine m={her.machine} />
    </section>
  );
}
