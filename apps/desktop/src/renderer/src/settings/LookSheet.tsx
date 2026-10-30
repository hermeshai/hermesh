import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Client } from "@hermesh/proto/local";
import { agentName, grantColor, iconApps, type GrantLook } from "@hermesh/ui";
import { local } from "../api";
import { grantFace } from "../components";
import type { Store } from "../store";

/** A grant's picture or logo at any size, else its initial on its colour. */
function Face({ c, size }: { c: GrantLook; size: number }) {
  return (
    grantFace(c, size) ?? (
      <span className="av" style={{ width: size, height: size, borderRadius: size * 0.28, fontSize: size * 0.45, background: grantColor(c.label) }} aria-hidden="true">
        {(c.label.trim() || "?").slice(0, 1).toUpperCase()}
      </span>
    )
  );
}

/**
 * A picture as a grant keeps it: its middle square at 128 px, as a PNG, or
 * as a JPEG on white when the PNG would be large (a photo). undefined when
 * it can't be read.
 */
async function shrink(f: File): Promise<Uint8Array | undefined> {
  try {
    const bmp = await createImageBitmap(f);
    const s = Math.min(bmp.width, bmp.height);
    const cv = document.createElement("canvas");
    cv.width = cv.height = 128;
    const g = cv.getContext("2d")!;
    g.imageSmoothingQuality = "high";
    const draw = () => g.drawImage(bmp, (bmp.width - s) / 2, (bmp.height - s) / 2, s, s, 0, 0, 128, 128);
    const blob = (type: string, q?: number) => new Promise<Blob | null>((r) => cv.toBlob(r, type, q));
    draw();
    let b = await blob("image/png");
    if (b && b.size > 40_000) {
      g.fillStyle = "#fff";
      g.fillRect(0, 0, 128, 128);
      draw();
      b = await blob("image/jpeg", 0.88);
    }
    return b ? new Uint8Array(await b.arrayBuffer()) : undefined;
  } catch {
    return undefined;
  }
}

/**
 * Name and icon, changed together and signed once: the name the owner knows
 * an agent by, and what it is drawn with — its app's (自动), its initial, an
 * app's logo, or a picture of the owner's.
 */
export function LookSheet({ c, st, close }: { c: Client; st: Store; close: () => void }) {
  const { t } = useTranslation("settings");
  const shown = agentName(c.label);
  const [name, setName] = useState(shown);
  const [typed, setTyped] = useState(false);
  const [icon, setIcon] = useState(c.icon);
  const [image, setImage] = useState(c.icon === "image" ? c.image : undefined);
  const [bad, setBad] = useState(false);
  const [busy, setBusy] = useState(false);
  const [over, setOver] = useState(false);
  const file = useRef<HTMLInputElement>(null);
  // An unchanged name keeps its label whole: "Cursor (cursor.com)" stays vouched for by its domain.
  const label = name.trim() === shown ? c.label : name.trim();
  const look = (i: string): GrantLook => ({ label, icon: i, image });
  const take = async (f?: File) => {
    if (!f) return;
    const b = await shrink(f);
    setBad(!b);
    if (b) {
      setImage(b);
      setIcon("image");
    }
  };
  const save = async () => {
    if (!name.trim() || busy) return;
    if (label === c.label && icon === c.icon && (icon !== "image" || image === c.image)) return close();
    setBusy(true);
    await st.run(() => local.renameClient({ grantId: c.grantId, label, icon, image: icon === "image" ? image : undefined })); // failures show as a toast
    close();
  };
  const tile = (key: string, title: string, face: GrantLook, onPick: () => void) => (
    <button key={key} className={`gl-tile ${icon === key ? "on" : ""}`} aria-pressed={icon === key} title={title} onClick={onPick}>
      <span>
        <Face c={face} size={36} />
      </span>
      {title}
    </button>
  );
  return (
    <>
      <div className="sheet-scrim" />
      <div
        className={`sheet ${over ? "gl-over" : ""}`}
        role="dialog"
        aria-label={t("grants.renameTitle")}
        style={{ width: 460 }}
        onDragOver={(e) => (e.preventDefault(), setOver(true))}
        onDragLeave={() => setOver(false)}
        onDrop={(e) => (e.preventDefault(), setOver(false), take(e.dataTransfer.files[0]))}
      >
        <h3>{t("grants.renameTitle")}</h3>
        <div className="gl-id">
          <Face c={look(icon)} size={52} />
          <label>
            {t("grants.agentName")}
            <input
              className="field"
              value={name}
              maxLength={64}
              autoFocus
              spellCheck={false}
              onChange={(e) => (setName(e.target.value), setTyped(true))}
              onKeyDown={(e) => (e.key === "Enter" ? save() : e.key === "Escape" && close())}
            />
          </label>
        </div>
        <div className="fhead gl-head">{t("grants.icon")}</div>
        <div className="gl-grid" role="group" aria-label={t("grants.icon")}>
          {tile("", t("grants.iconAuto"), look(""), () => setIcon(""))}
          {tile("letter", t("grants.iconLetter"), look("letter"), () => setIcon("letter"))}
          {iconApps.map((a) =>
            tile(`app:${a.logo}`, a.name, look(`app:${a.logo}`), () => {
              setIcon(`app:${a.logo}`);
              if (!typed) setName(a.name);
            }),
          )}
          {image && tile("image", t("grants.iconMine"), look("image"), () => setIcon("image"))}
          <button className="gl-tile" onClick={() => file.current?.click()}>
            <span>
              <i className="gl-plus">+</i>
            </span>
            {t("grants.iconUpload")}
          </button>
          <input ref={file} type="file" accept="image/png,image/jpeg,image/webp,image/gif" hidden onChange={(e) => (take(e.target.files?.[0]), (e.target.value = ""))} />
        </div>
        <p className={`sheet-note ${bad ? "sheet-err" : "muted"}`}>{bad ? t("grants.iconBad") : icon === "" ? t("grants.iconAutoHint") : t("grants.renameMessage")}</p>
        <div className="sheet-foot">
          <button className="btn" onClick={close}>{t("cancel")}</button>
          <button className="btn primary" disabled={busy || !name.trim()} onClick={save}>{t("grants.renameButton")}</button>
        </div>
      </div>
    </>
  );
}
