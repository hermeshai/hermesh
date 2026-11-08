import { useLayoutEffect, useMemo, useState, type CSSProperties, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import type { Avatar as AvatarSpec, AvatarHue, AvatarInput, AvatarShape, BotSummary, ThinkingLevel } from "@hermesh/her-protocol";
import { local } from "../api";
import { ErrorLine, SignerWaiting, useAction } from "../sheets/common";
import { useStore } from "../store";
import { Avatar, hues, shapes } from "./Avatar";
import { useHer } from "./context";
import { draftThinking, offersThinking } from "./format";
import { ModelBlocked, ModelRows } from "./menus";
import "../sheets/sheets.css";

// The Her page's sheets: a new assistant, full access for one (signed), and
// another environment (Setup.tsx). They are the app's sheets in look and keys.

/** A sheet over the Her page; Esc closes it unless it waits (`busy`). */
export function HerDialog({ label, width, busy = false, onClose, children }: { label: string; width: number; busy?: boolean; onClose: () => void; children: ReactNode }) {
  useLayoutEffect(() => {
    const esc = (e: KeyboardEvent) => e.key === "Escape" && !busy && onClose();
    window.addEventListener("keydown", esc);
    return () => window.removeEventListener("keydown", esc);
  }, [busy, onClose]);
  // Over the whole window, as the app's other sheets are.
  return createPortal(
    <>
      <div className="sheet-scrim" />
      <div className="sheet" role="dialog" aria-modal="true" aria-label={label} style={{ width }}>
        {children}
      </div>
    </>,
    document.querySelector(".mainwrap") ?? document.body,
  );
}

const hueNames = Object.keys(hues) as AvatarHue[];
const pick = <T,>(list: T[]) => list[Math.floor(Math.random() * list.length)];

/** At most 20 characters, as her keeps names. */
export const NAME_MAX = 20;

/**
 * Making an assistant: a name, an avatar, and the model it
 * answers with and how hard it thinks, from those its environment can use. It comes with a character
 * and a colour picked at random and the environment's default model on show, so a name and ↩ make one.
 * New assistants ask before risky steps (docs/her-server.md §9.1).
 */
export function NewAssistantSheet() {
  const { t } = useTranslation("her");
  const her = useHer();
  const initial = useMemo<AvatarSpec>(() => ({ kind: "char", shape: pick(shapes), hue: pick(hueNames) }), []);
  const [name, setName] = useState("");
  const [avatar, setAvatar] = useState<AvatarSpec>(initial);
  const [png, setPng] = useState<string | null>(null);
  const [chosenModel, setChosenModel] = useState<string | null>(null);
  const [chosenLevel, setChosenLevel] = useState<ThinkingLevel | null>(null);
  const act = useAction();
  const [pictureError, setPictureError] = useState("");
  const models = her.env.models;
  const model = chosenModel ?? models.defaults?.model ?? models.models.find((m) => m.available && m.shown)?.id ?? null;
  const entry = models.models.find((m) => m.id === model);
  const thinking = draftThinking(entry, chosenLevel);
  const close = () => her.setSheet(null);
  const hue = avatar.kind === "char" || avatar.kind === "mono" ? avatar.hue : initial.kind === "char" ? initial.hue : "slate";
  const trimmed = name.trim();
  const ready = trimmed !== "" && Array.from(trimmed).length <= NAME_MAX && !!entry?.available && !act.busy && her.env.rpc !== null;
  const upload = async () => {
    const got = await pickPicture();
    if (!got) return;
    setPictureError("error" in got ? t(got.error === "too_large" ? "sheet.tooLarge" : "sheet.unreadable") : "");
    if ("error" in got) return;
    setPng(got.png);
    setAvatar({ kind: "image", rev: 0 });
  };
  const submit = async () => {
    if (!ready) return;
    const given: AvatarInput = png ? { kind: "image", png } : avatar.kind === "image" ? { kind: "char", shape: pick(shapes), hue } : avatar;
    const ok = await act.go(async () => {
      const r = await her.call("bots/create", { name: trimmed, avatar: given, model, thinking });
      her.selectBot(r.bot.id);
    });
    if (ok) close();
  };
  return (
    <HerDialog label={t("sheet.newTitle")} width={440} busy={act.busy} onClose={close}>
      <form
        className="bsheet"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <h3 id="sheet-title">{t("sheet.newTitle")}</h3>
        <p className="sub">{t("sheet.sub")}</p>
        <div className="bs-top">
          <Avatar spec={avatar} name={trimmed} size={64} preview={png ? `data:image/png;base64,${png}` : undefined} />
          <label className="bs-name">
            <span>{t("sheet.name")}</span>
            <input className="field" value={name} autoFocus maxLength={NAME_MAX} placeholder={t("sheet.placeholder")} autoComplete="off" spellCheck={false} onChange={(e) => setName(e.target.value)} />
          </label>
        </div>
        <div className="bs-k">{t("sheet.avatar")}</div>
        <AvatarChoices
          avatar={avatar}
          hue={hue}
          name={trimmed}
          onChoose={(spec) => {
            setAvatar(spec);
            setPng(null);
          }}
        />
        <div className="bs-k bs-model">{t("panel.model")}</div>
        <ModelRows
          model={model}
          thinking={thinking}
          onModel={(ref) => {
            const next = models.models.find((m) => m.id === ref);
            const kept = thinking !== "default" && offersThinking(next, thinking) ? thinking : null;
            setChosenModel(ref);
            setChosenLevel(kept);
            return draftThinking(next, kept);
          }}
          onThinking={setChosenLevel}
        />
        <ModelBlocked model={model} />
        <ErrorLine text={act.error || pictureError} />
        <div className="sheet-foot">
          <button type="button" className="link left" onClick={() => void upload()}>
            {t("sheet.upload")}
          </button>
          <button type="button" className="btn" onClick={close}>
            {t("sheet.cancel")}
          </button>
          <button type="submit" className="btn primary" disabled={!ready}>
            {t("sheet.create")}
          </button>
        </div>
      </form>
    </HerDialog>
  );
}

/** The characters, the first character of its name, and the colours; `mark` adds the Hermesh mark, Her's. */
export function AvatarChoices({ avatar, hue, name, mark = false, onChoose }: { avatar: AvatarSpec; hue: AvatarHue; name: string; mark?: boolean; onChoose: (spec: Exclude<AvatarSpec, { kind: "image" }>) => void }) {
  const { t } = useTranslation("her");
  return (
    <>
      <div className="bs-shapes" role="radiogroup" aria-label={t("sheet.avatar")}>
        {mark && (
          <button type="button" role="radio" aria-checked={avatar.kind === "mark"} title={t("sheet.mark")} className={`bs-shape ${avatar.kind === "mark" ? "on" : ""}`} onClick={() => onChoose({ kind: "mark" })}>
            <Avatar spec={{ kind: "mark" }} name="" size={36} />
          </button>
        )}
        {shapes.map((shape: AvatarShape) => {
          const on = avatar.kind === "char" && avatar.shape === shape;
          return (
            <button key={shape} type="button" role="radio" aria-checked={on} title={t(`sheet.shapes.${shape}`)} className={`bs-shape ${on ? "on" : ""}`} onClick={() => onChoose({ kind: "char", shape, hue })}>
              <Avatar spec={{ kind: "char", shape, hue }} name="" size={36} />
            </button>
          );
        })}
        <button type="button" role="radio" aria-checked={avatar.kind === "mono"} title={t("sheet.mono")} className={`bs-shape ${avatar.kind === "mono" ? "on" : ""}`} onClick={() => onChoose({ kind: "mono", hue })}>
          <Avatar spec={{ kind: "mono", hue }} name={name || t("sheet.monoSample")} size={36} />
        </button>
      </div>
      <div className="bs-hues" role="radiogroup" aria-label={t("sheet.avatar")}>
        {hueNames.map((h) => {
          const on = (avatar.kind === "char" || avatar.kind === "mono") && avatar.hue === h;
          return (
            <button
              key={h}
              type="button"
              role="radio"
              aria-checked={on}
              title={t(`sheet.hues.${h}`)}
              className={`bs-hue ${on ? "on" : ""}`}
              style={{ "--h": hues[h] } as CSSProperties}
              onClick={() => onChoose(avatar.kind === "mono" ? { kind: "mono", hue: h } : { kind: "char", shape: avatar.kind === "char" ? avatar.shape : pick(shapes), hue: h })}
            />
          );
        })}
      </div>
    </>
  );
}

/** A picture for an avatar, from the system's picker: cropped to a square and scaled to 256 px, as a PNG (base64). */
export function pickPicture(): Promise<{ png: string } | { error: "too_large" | "unreadable" } | null> {
  return new Promise((resolve) => {
    const el = document.createElement("input");
    el.type = "file";
    el.accept = "image/png,image/jpeg";
    el.onchange = () => {
      const file = el.files?.[0];
      if (!file) return resolve(null);
      if (file.size > 10 * 1024 * 1024) return resolve({ error: "too_large" });
      const img = new Image();
      img.onload = () => {
        const side = Math.min(img.width, img.height);
        const canvas = document.createElement("canvas");
        canvas.width = canvas.height = 256;
        canvas.getContext("2d")!.drawImage(img, (img.width - side) / 2, (img.height - side) / 2, side, side, 0, 0, 256, 256);
        resolve({ png: canvas.toDataURL("image/png").split(",")[1] });
        URL.revokeObjectURL(img.src);
      };
      img.onerror = () => resolve({ error: "unreadable" });
      img.src = URL.createObjectURL(file);
    };
    el.click();
  });
}

/**
 * Full access for an assistant (docs/her-server.md §8): it acts without asking on its machine, and
 * so do the agents and routines it starts. A Her Server reads web pages and messages from anyone, so
 * this is signed with the root key: Touch ID in the signer's window.
 */
export function AccessSheet({ bot }: { bot: BotSummary }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const act = useAction();
  const close = () => her.setSheet(null);
  const go = async () => {
    const ok = await act.go(() => local.setHerAssistantMode({ machineId: her.machine.machineId, botId: bot.id, permissionMode: "bypass-permissions" }));
    if (!ok) return;
    close();
    st.show(t("access.done", { name: bot.name }));
  };
  return (
    <HerDialog label={t("access.title", { name: bot.name })} width={460} busy={act.busy} onClose={close}>
      <h3 id="sheet-title">{t("access.title", { name: bot.name })}</h3>
      <p className="sub">{t("access.body", { environment: her.machine.machineName })}</p>
      {act.busy && <SignerWaiting detail={t("access.signer", { name: bot.name, environment: her.machine.machineName })} />}
      <ErrorLine text={act.error} />
      <div className="sheet-foot">
        <button className="btn" onClick={close} disabled={act.busy}>
          {t("sheet.cancel")}
        </button>
        <button className="btn primary destructive" disabled={act.busy} onClick={() => void go()}>
          {t("access.button")}
        </button>
      </div>
    </HerDialog>
  );
}
